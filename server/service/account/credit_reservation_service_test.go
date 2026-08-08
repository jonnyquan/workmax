package account

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"server/config"
	"server/globals"
	"server/model"
	"server/utils/testutil"

	"gorm.io/gorm"
)

// Why these tests exist:
// CreditReservation is the money-path's idempotency + safety contract.
// A regression here either:
//   - double-charges (if the (uid, key) lookup window races); or
//   - leaves credits stuck in `reserved` state (if Finalize/Release
//     skip refund); or
//   - lets workers operate on zombie rows the sweeper has already
//     reclaimed (if FindActive ignores expiry).
// All six tests pin a specific contract that costs real money to
// regress. They share a tiny seedFreePackOf helper to keep the setup
// noise low — the contract is the point, not the seed.

// seedFreePackOf creates a free user with a single never-expiring
// purchase pack of `credits` available credits. Returns the uid + pack
// id for assertions on credits_used.
func seedFreePackOf(t *testing.T, db *gorm.DB, credits int) (uid int, packID uint) {
	t.Helper()
	user := model.User{Member: 0, Nickname: "tester", Email: "t@example.com"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	pack := model.CreditsPack{
		UID:          int(user.Id),
		SourceType:   model.CreditsSourcePurchase,
		SourceID:     "test-pack",
		CreditsTotal: credits,
		CreditsUsed:  0,
	}
	if err := db.Create(&pack).Error; err != nil {
		t.Fatalf("seed pack: %v", err)
	}
	return int(user.Id), pack.Id
}

func packUsed(t *testing.T, db *gorm.DB, packID uint) int {
	t.Helper()
	var p model.CreditsPack
	if err := db.First(&p, packID).Error; err != nil {
		t.Fatalf("reload pack: %v", err)
	}
	return p.CreditsUsed
}

func bindReservationToAgentTurnForSweepTest(t *testing.T, db *gorm.DB, reservation model.CreditReservation) {
	t.Helper()
	turnID := fmt.Sprintf("turn-sweep-%d", reservation.Id)
	principalID := fmt.Sprintf("principal-sweep-%d", reservation.Id)
	commandDigest := fmt.Sprintf("command-digest-%d", reservation.Id)
	now := time.Now().UTC()
	if err := db.Exec(`
		INSERT INTO w_agent_turn (
			turn_id, principal_id, thread_id, idempotency_key,
			command_digest, plugin_snapshot_json, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		turnID, principalID, fmt.Sprintf("thread-sweep-%d", reservation.Id),
		fmt.Sprintf("idempotency-sweep-%d", reservation.Id), commandDigest, `{}`, now,
	).Error; err != nil {
		t.Fatalf("seed bound Agent Turn: %v", err)
	}

	hexID := fmt.Sprintf("%064x", reservation.Id)
	if err := db.Exec(`
		INSERT INTO w_agent_turn_reservation_binding (
			binding_id, turn_id, principal_id, turn_command_digest,
			reservation_id, reservation_uid, reservation_request_digest,
			reservation_tool, reserved_units, project_id,
			pricing_snapshot_digest, binding_digest
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		hexID, turnID, principalID, commandDigest,
		reservation.Id, reservation.UID, reservation.RequestDigest,
		reservation.Tool, reservation.Reserved, reservation.ProjectID,
		"sha256:"+strings.Repeat("a", 64), "sha256:"+hexID,
	).Error; err != nil {
		t.Fatalf("seed Agent reservation binding: %v", err)
	}
}

func TestReserve_IdempotentReturnsExistingRowOnReplay(t *testing.T) {
	// Same (uid, key) called twice → second call returns the original
	// row with Created=false and does NOT debit credits a second time.
	// This is the core "client retry doesn't double-charge" guarantee.
	db := testutil.NewTestDB(t)
	uid, packID := seedFreePackOf(t, db, 100)
	svc := NewCreditReservationService()

	var firstID, secondID uint
	var firstCreated, secondCreated bool

	if err := db.Transaction(func(tx *gorm.DB) error {
		res, err := svc.Reserve(tx, ReservationRequest{
			UID: uid, Tool: "test", IdempotencyKey: "same-key", Reserved: 10,
		})
		if err != nil {
			return err
		}
		firstID = res.Reservation.Id
		firstCreated = res.Created
		return nil
	}); err != nil {
		t.Fatalf("first Reserve: %v", err)
	}

	if err := db.Transaction(func(tx *gorm.DB) error {
		res, err := svc.Reserve(tx, ReservationRequest{
			UID: uid, Tool: "test", IdempotencyKey: "same-key", Reserved: 10,
		})
		if err != nil {
			return err
		}
		secondID = res.Reservation.Id
		secondCreated = res.Created
		return nil
	}); err != nil {
		t.Fatalf("second Reserve: %v", err)
	}

	if !firstCreated {
		t.Error("first call should be Created=true")
	}
	if secondCreated {
		t.Error("second call must be Created=false (idempotent replay)")
	}
	if firstID != secondID {
		t.Errorf("idempotent replay returned different rows: %d vs %d", firstID, secondID)
	}
	// Credits debited exactly once.
	if got := packUsed(t, db, packID); got != 10 {
		t.Errorf("pack credits_used = %d, want 10 (replay must not double-charge)", got)
	}
}

func TestReserve_InsufficientCreditsReturnsError(t *testing.T) {
	// Asking for more credits than the user owns must fail without
	// creating a reservation row. Otherwise we'd create a row that
	// claims to have debited credits that never existed.
	//
	// The error must be the ErrInsufficientCredits sentinel so HTTP
	// handlers can errors.Is() it and return 402, not a generic 5xx.
	// Every Reserve call site branches on this — see fix(credits): split
	// insufficient-credits from server errors via sentinel.
	db := testutil.NewTestDB(t)
	uid, _ := seedFreePackOf(t, db, 5)
	svc := NewCreditReservationService()

	err := db.Transaction(func(tx *gorm.DB) error {
		_, e := svc.Reserve(tx, ReservationRequest{
			UID: uid, Tool: "test", IdempotencyKey: "k", Reserved: 100,
		})
		return e
	})
	if !errors.Is(err, ErrInsufficientCredits) {
		t.Fatalf("Reserve should fail with ErrInsufficientCredits sentinel, got: %v", err)
	}
	var count int64
	db.Model(&model.CreditReservation{}).Count(&count)
	if count != 0 {
		t.Errorf("no reservation row should be created on failure, got %d", count)
	}
}

func TestFinalize_PartialUsedRefundsDifference(t *testing.T) {
	// Worker reserved 10 but only burned 7 → 3 credits should flow
	// back to the originating pack, status=finalized.
	db := testutil.NewTestDB(t)
	uid, packID := seedFreePackOf(t, db, 100)
	svc := NewCreditReservationService()

	var rid uint
	if err := db.Transaction(func(tx *gorm.DB) error {
		res, err := svc.Reserve(tx, ReservationRequest{
			UID: uid, Tool: "test", IdempotencyKey: "k", Reserved: 10,
		})
		if err != nil {
			return err
		}
		rid = res.Reservation.Id
		return nil
	}); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if got := packUsed(t, db, packID); got != 10 {
		t.Fatalf("pre-finalize credits_used = %d, want 10", got)
	}

	if err := db.Transaction(func(tx *gorm.DB) error {
		return svc.Finalize(tx, rid, 7)
	}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	if got := packUsed(t, db, packID); got != 7 {
		t.Errorf("post-finalize credits_used = %d, want 7 (3 refunded)", got)
	}
	var r model.CreditReservation
	db.First(&r, rid)
	if r.Status != model.CreditReservationStatusFinalized {
		t.Errorf("status = %q, want finalized", r.Status)
	}
	if r.Used != 7 {
		t.Errorf("used = %d, want 7", r.Used)
	}
}

func TestRelease_RefundsFullAmount(t *testing.T) {
	// Failed operation → Release returns 100% of reserved credits to
	// the originating pack, status=released. This is the "task failed,
	// undo the debit" path.
	db := testutil.NewTestDB(t)
	uid, packID := seedFreePackOf(t, db, 100)
	svc := NewCreditReservationService()

	var rid uint
	db.Transaction(func(tx *gorm.DB) error {
		res, _ := svc.Reserve(tx, ReservationRequest{
			UID: uid, Tool: "test", IdempotencyKey: "k", Reserved: 25,
		})
		rid = res.Reservation.Id
		return nil
	})

	if err := db.Transaction(func(tx *gorm.DB) error {
		return svc.Release(tx, rid)
	}); err != nil {
		t.Fatalf("Release: %v", err)
	}

	if got := packUsed(t, db, packID); got != 0 {
		t.Errorf("post-release credits_used = %d, want 0 (full refund)", got)
	}
	var r model.CreditReservation
	db.First(&r, rid)
	if r.Status != model.CreditReservationStatusReleased {
		t.Errorf("status = %q, want released", r.Status)
	}
}

func TestFinalize_ExactReplaySucceedsAndDifferentOutcomeConflicts(t *testing.T) {
	// Unknown-commit recovery may present the exact settlement twice. The same
	// used amount is a no-op success; a different commercial assertion fails.
	db := testutil.NewTestDB(t)
	uid, _ := seedFreePackOf(t, db, 100)
	svc := NewCreditReservationService()

	var rid uint
	db.Transaction(func(tx *gorm.DB) error {
		res, _ := svc.Reserve(tx, ReservationRequest{
			UID: uid, Tool: "test", IdempotencyKey: "k", Reserved: 5,
		})
		rid = res.Reservation.Id
		return nil
	})
	db.Transaction(func(tx *gorm.DB) error { return svc.Finalize(tx, rid, 5) })

	err := db.Transaction(func(tx *gorm.DB) error { return svc.Finalize(tx, rid, 5) })
	if err != nil {
		t.Errorf("exact repeat Finalize: got %v, want nil", err)
	}
	err = db.Transaction(func(tx *gorm.DB) error { return svc.Finalize(tx, rid, 4) })
	if !errors.Is(err, ErrReservationSettlementConflict) {
		t.Errorf("different repeat Finalize: got %v, want ErrReservationSettlementConflict", err)
	}
	err = db.Transaction(func(tx *gorm.DB) error { return svc.Release(tx, rid) })
	if !errors.Is(err, ErrReservationAlreadyFinalized) {
		t.Errorf("Release after Finalize: got %v, want ErrReservationAlreadyFinalized", err)
	}
}

func TestSettlementResultAPIsReturnExactDurableReplayState(t *testing.T) {
	db := testutil.NewTestDB(t)
	uid, packID := seedFreePackOf(t, db, 100)
	svc := NewCreditReservationService()

	var finalizeID uint
	if err := db.Transaction(func(tx *gorm.DB) error {
		result, err := svc.Reserve(tx, ReservationRequest{
			UID: uid, Tool: "agent", IdempotencyKey: "result-finalize", Reserved: 10,
		})
		if err != nil {
			return err
		}
		finalizeID = result.Reservation.Id
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	var first, replay ReservationSettlementSnapshot
	if err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		first, err = svc.FinalizeWithResult(tx, finalizeID, 7)
		return err
	}); err != nil {
		t.Fatalf("FinalizeWithResult: %v", err)
	}
	if first.ReservationID != finalizeID || first.UID != uid || first.Tool != "agent" ||
		first.IdempotencyKey != "result-finalize" || first.Status != model.CreditReservationStatusFinalized ||
		first.Reserved != 10 || first.Used != 7 || first.FinalizedAt == nil || first.ReleasedAt != nil ||
		first.RefundTargetStatus != "" || first.RefundTargetUsed != nil || first.RefundDue != 0 ||
		first.StateVersion == 0 || first.StateChangedAt == nil {
		t.Fatalf("finalized snapshot = %+v", first)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		replay, err = svc.FinalizeWithResult(tx, finalizeID, 7)
		return err
	}); err != nil {
		t.Fatalf("FinalizeWithResult replay: %v", err)
	}
	if replay.Status != first.Status || replay.Reserved != first.Reserved || replay.Used != first.Used ||
		replay.StateVersion != first.StateVersion || replay.RefundTargetUsed != nil {
		t.Fatalf("finalize replay snapshot = %+v, first = %+v", replay, first)
	}
	if got := packUsed(t, db, packID); got != 7 {
		t.Fatalf("Pack used after exact finalize replay = %d, want 7", got)
	}

	var releaseID uint
	if err := db.Transaction(func(tx *gorm.DB) error {
		result, err := svc.Reserve(tx, ReservationRequest{
			UID: uid, Tool: "agent", IdempotencyKey: "result-release", Reserved: 5,
		})
		if err == nil {
			releaseID = result.Reservation.Id
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var released, releasedReplay ReservationSettlementSnapshot
	if err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		released, err = svc.ReleaseWithResult(tx, releaseID)
		return err
	}); err != nil {
		t.Fatalf("ReleaseWithResult: %v", err)
	}
	if released.Status != model.CreditReservationStatusReleased || released.Used != 0 ||
		released.ReleasedAt == nil || released.FinalizedAt != nil || released.RefundTargetUsed != nil {
		t.Fatalf("released snapshot = %+v", released)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		releasedReplay, err = svc.ReleaseWithResult(tx, releaseID)
		return err
	}); err != nil {
		t.Fatalf("ReleaseWithResult replay: %v", err)
	}
	if releasedReplay.Status != released.Status || releasedReplay.StateVersion != released.StateVersion {
		t.Fatalf("release replay snapshot = %+v, first = %+v", releasedReplay, released)
	}
}

func TestFinalizeWithResultReportsRefundPendingAndExactPendingReplay(t *testing.T) {
	db := testutil.NewTestDB(t)
	uid, packID := seedFreePackOf(t, db, 100)
	svc := NewCreditReservationService()

	var reservationID uint
	if err := db.Transaction(func(tx *gorm.DB) error {
		result, err := svc.Reserve(tx, ReservationRequest{
			UID: uid, Tool: "agent", IdempotencyKey: "result-pending", Reserved: 10,
		})
		if err == nil {
			reservationID = result.Reservation.Id
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Where("reservation_id = ?", reservationID).
		Delete(&model.CreditReservationAllocation{}).Error; err != nil {
		t.Fatalf("remove allocation evidence: %v", err)
	}

	var pending ReservationSettlementSnapshot
	if err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		pending, err = svc.FinalizeWithResult(tx, reservationID, 7)
		return err
	}); err != nil {
		t.Fatalf("FinalizeWithResult pending: %v", err)
	}
	if pending.Status != model.CreditReservationStatusRefundPending ||
		pending.RefundTargetStatus != model.CreditReservationStatusFinalized ||
		pending.RefundTargetUsed == nil || *pending.RefundTargetUsed != 7 || pending.RefundDue != 3 ||
		pending.RefundAttempts != 1 || pending.NextRefundAt == nil ||
		pending.LastRefundErrorCode != "allocation_incomplete" || pending.Used != 0 ||
		pending.FinalizedAt != nil || pending.ReleasedAt != nil {
		t.Fatalf("pending snapshot = %+v", pending)
	}
	if got := packUsed(t, db, packID); got != 10 {
		t.Fatalf("failed refund changed Pack used to %d, want 10", got)
	}

	var replay ReservationSettlementSnapshot
	if err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		replay, err = svc.FinalizeWithResult(tx, reservationID, 7)
		return err
	}); err != nil {
		t.Fatalf("FinalizeWithResult pending replay: %v", err)
	}
	if replay.Status != model.CreditReservationStatusRefundPending ||
		replay.RefundTargetUsed == nil || *replay.RefundTargetUsed != 7 ||
		replay.RefundAttempts != pending.RefundAttempts || replay.StateVersion != pending.StateVersion ||
		replay.NextRefundAt == nil || !replay.NextRefundAt.Equal(*pending.NextRefundAt) {
		t.Fatalf("pending replay snapshot = %+v, first = %+v", replay, pending)
	}
}

func TestReleaseWithResultReportsRefundPendingNullableZeroTarget(t *testing.T) {
	db := testutil.NewTestDB(t)
	uid, packID := seedFreePackOf(t, db, 100)
	svc := NewCreditReservationService()

	var reservationID uint
	if err := db.Transaction(func(tx *gorm.DB) error {
		result, err := svc.Reserve(tx, ReservationRequest{
			UID: uid, Tool: "agent", IdempotencyKey: "release-result-pending", Reserved: 8,
		})
		if err == nil {
			reservationID = result.Reservation.Id
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Where("reservation_id = ?", reservationID).
		Delete(&model.CreditReservationAllocation{}).Error; err != nil {
		t.Fatalf("remove allocation evidence: %v", err)
	}

	var pending ReservationSettlementSnapshot
	if err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		pending, err = svc.ReleaseWithResult(tx, reservationID)
		return err
	}); err != nil {
		t.Fatalf("ReleaseWithResult pending: %v", err)
	}
	if pending.Status != model.CreditReservationStatusRefundPending ||
		pending.RefundTargetStatus != model.CreditReservationStatusReleased ||
		pending.RefundTargetUsed == nil || *pending.RefundTargetUsed != 0 || pending.RefundDue != 8 ||
		pending.RefundAttempts != 1 || pending.LastRefundErrorCode != "allocation_incomplete" ||
		pending.Used != 0 || pending.FinalizedAt != nil || pending.ReleasedAt != nil {
		t.Fatalf("release-pending snapshot = %+v", pending)
	}
	if got := packUsed(t, db, packID); got != 8 {
		t.Fatalf("failed release refund changed Pack used to %d, want 8", got)
	}
}

func TestReviewSettlementResultAPIsPreserveExactHoldTuple(t *testing.T) {
	db := testutil.NewTestDB(t)
	uid, _ := seedFreePackOf(t, db, 100)
	svc := NewCreditReservationService()
	hold := ReservationReviewHold{
		ReviewID: "review-result", SettlementKey: "settlement-result",
		RequestDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}

	reserve := func(t *testing.T, key string, credits int) uint {
		t.Helper()
		var reservationID uint
		if err := db.Transaction(func(tx *gorm.DB) error {
			result, err := svc.Reserve(tx, ReservationRequest{
				UID: uid, Tool: "agent", IdempotencyKey: key, Reserved: credits,
			})
			if err == nil {
				reservationID = result.Reservation.Id
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return reservationID
	}

	finalizeID := reserve(t, "review-result-finalize", 10)
	var held, heldReplay ReservationSettlementSnapshot
	if err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		held, err = svc.HoldForReviewWithResult(tx, finalizeID, hold)
		return err
	}); err != nil {
		t.Fatalf("HoldForReviewWithResult: %v", err)
	}
	if held.Status != model.CreditReservationStatusReviewHold || held.HoldReviewID != hold.ReviewID ||
		held.HoldSettlementKey != hold.SettlementKey || held.HoldRequestDigest != hold.RequestDigest ||
		held.ReviewHeldAt == nil {
		t.Fatalf("held snapshot = %+v", held)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		heldReplay, err = svc.HoldForReviewWithResult(tx, finalizeID, hold)
		return err
	}); err != nil {
		t.Fatalf("HoldForReviewWithResult replay: %v", err)
	}
	if heldReplay.Status != held.Status || heldReplay.StateVersion != held.StateVersion ||
		heldReplay.HoldSettlementKey != held.HoldSettlementKey {
		t.Fatalf("hold replay snapshot = %+v, first = %+v", heldReplay, held)
	}

	var finalized ReservationSettlementSnapshot
	if err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		finalized, err = svc.FinalizeReviewWithResult(tx, finalizeID, hold, 10)
		return err
	}); err != nil {
		t.Fatalf("FinalizeReviewWithResult: %v", err)
	}
	if finalized.Status != model.CreditReservationStatusFinalized || finalized.Used != 10 ||
		finalized.HoldSettlementKey != hold.SettlementKey || finalized.FinalizedAt == nil {
		t.Fatalf("review-finalized snapshot = %+v", finalized)
	}

	releaseHold := hold
	releaseHold.ReviewID = "review-release-result"
	releaseHold.SettlementKey = "settlement-release-result"
	releaseID := reserve(t, "review-result-release", 6)
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, err := svc.HoldForReviewWithResult(tx, releaseID, releaseHold)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var released ReservationSettlementSnapshot
	if err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		released, err = svc.ReleaseReviewWithResult(tx, releaseID, releaseHold)
		return err
	}); err != nil {
		t.Fatalf("ReleaseReviewWithResult: %v", err)
	}
	if released.Status != model.CreditReservationStatusReleased || released.Used != 0 ||
		released.HoldSettlementKey != releaseHold.SettlementKey || released.ReleasedAt == nil {
		t.Fatalf("review-released snapshot = %+v", released)
	}
}

func TestSettlementResultAPIsReturnZeroSnapshotOnError(t *testing.T) {
	db := testutil.NewTestDB(t)
	uid, packID := seedFreePackOf(t, db, 100)
	svc := NewCreditReservationService()
	var reservationID uint
	if err := db.Transaction(func(tx *gorm.DB) error {
		result, err := svc.Reserve(tx, ReservationRequest{
			UID: uid, Tool: "agent", IdempotencyKey: "result-error", Reserved: 5,
		})
		if err == nil {
			reservationID = result.Reservation.Id
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}

	var snapshot ReservationSettlementSnapshot
	err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		snapshot, err = svc.FinalizeWithResult(tx, reservationID, 6)
		return err
	})
	if err == nil {
		t.Fatal("FinalizeWithResult above reservation ceiling succeeded")
	}
	if snapshot != (ReservationSettlementSnapshot{}) {
		t.Fatalf("error returned speculative snapshot %+v", snapshot)
	}
	if got := packUsed(t, db, packID); got != 5 {
		t.Fatalf("failed settlement changed Pack used to %d, want 5", got)
	}

	if err := db.Transaction(func(tx *gorm.DB) error {
		locked, err := svc.LockSettlementSnapshot(tx, reservationID)
		if err != nil {
			return err
		}
		if locked.Status != model.CreditReservationStatusReserved || locked.StateVersion != 1 {
			return fmt.Errorf("durable row changed after rejected settlement: %+v", locked)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	missing, err := svc.LockSettlementSnapshot(db, 0)
	if !errors.Is(err, gorm.ErrRecordNotFound) || missing != (ReservationSettlementSnapshot{}) {
		t.Fatalf("missing snapshot = %+v, %v", missing, err)
	}
}

func TestSweepExpiredReservations_RefundsAndMarksExpired(t *testing.T) {
	// The leak-prevention guarantee: a reservation whose handler died
	// before Finalize/Release gets reclaimed by the sweeper. Verifies:
	// (1) credits flow back to the originating pack,
	// (2) status flips to expired (terminal),
	// (3) released_at is set so audit can distinguish from active.
	db := testutil.NewTestDB(t)
	uid, packID := seedFreePackOf(t, db, 100)
	svc := NewCreditReservationService()

	var rid uint
	db.Transaction(func(tx *gorm.DB) error {
		res, _ := svc.Reserve(tx, ReservationRequest{
			UID: uid, Tool: "test", IdempotencyKey: "k", Reserved: 8,
			TTL: 1 * time.Millisecond,
		})
		rid = res.Reservation.Id
		return nil
	})

	// Force the row past TTL deterministically — sleeping would race.
	past := time.Now().Add(-1 * time.Hour)
	if err := db.Model(&model.CreditReservation{}).
		Where("id = ?", rid).
		Update("expires_at", past).Error; err != nil {
		t.Fatalf("force-expire: %v", err)
	}

	swept, failed, err := svc.SweepExpiredReservations(db, 100, 0)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if swept != 1 || failed != 0 {
		t.Errorf("sweep counts: swept=%d failed=%d, want 1 0", swept, failed)
	}
	if got := packUsed(t, db, packID); got != 0 {
		t.Errorf("post-sweep credits_used = %d, want 0 (sweep must refund)", got)
	}
	var r model.CreditReservation
	db.First(&r, rid)
	if r.Status != model.CreditReservationStatusExpired {
		t.Errorf("status = %q, want expired", r.Status)
	}
	if r.ReleasedAt == nil {
		t.Error("released_at not set on expired row")
	}
}

func TestSweepExpiredReservations_ExtendsActiveGenerationTaskReservation(t *testing.T) {
	// A long-running provider task should not become free just because its
	// reservation TTL elapsed before the worker completed.
	db := testutil.NewTestDB(t)
	uid, packID := seedFreePackOf(t, db, 100)
	svc := NewCreditReservationService()

	var rid uint
	if err := db.Transaction(func(tx *gorm.DB) error {
		res, err := svc.Reserve(tx, ReservationRequest{
			UID: uid, Tool: "image_generate", IdempotencyKey: "task-123", Reserved: 12,
			TTL: 1 * time.Millisecond,
		})
		if err != nil {
			return err
		}
		rid = res.Reservation.Id
		return nil
	}); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := db.Create(&model.GenerationTask{
		TaskID:      "task-123",
		UID:         uid,
		ToolID:      "image_generate",
		Model:       "test-model",
		Status:      model.TaskStatusProcessing,
		CreditsUsed: 12,
	}).Error; err != nil {
		t.Fatalf("seed task: %v", err)
	}
	past := time.Now().Add(-1 * time.Hour)
	db.Model(&model.CreditReservation{}).Where("id = ?", rid).Update("expires_at", past)

	swept, failed, err := svc.SweepExpiredReservations(db, 100, 0)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if swept != 1 || failed != 0 {
		t.Errorf("sweep counts: swept=%d failed=%d, want 1 0", swept, failed)
	}
	if got := packUsed(t, db, packID); got != 12 {
		t.Errorf("credits_used = %d, want 12 (active task reservation must remain debited)", got)
	}
	var r model.CreditReservation
	db.First(&r, rid)
	if r.Status != model.CreditReservationStatusReserved {
		t.Errorf("status = %q, want reserved", r.Status)
	}
	if !r.ExpiresAt.After(time.Now()) {
		t.Errorf("expires_at = %s, want extended into the future", r.ExpiresAt)
	}
}

func TestSweep_BrokenAllocationsStayRefundPendingUntilRepair(t *testing.T) {
	// Corrupt allocation evidence must never produce a false terminal outcome.
	// The financial attempt rolls back and only a bounded refund intent remains;
	// after evidence is repaired, the same intent reaches expired exactly once.
	db := testutil.NewTestDB(t)
	uid, packID := seedFreePackOf(t, db, 100)
	svc := NewCreditReservationService()

	var rid uint
	db.Transaction(func(tx *gorm.DB) error {
		res, _ := svc.Reserve(tx, ReservationRequest{
			UID: uid, Tool: "test", IdempotencyKey: "broken", Reserved: 9,
		})
		rid = res.Reservation.Id
		return nil
	})

	// Simulate the corruption: drop the allocation rows so refund can't
	// account for the reserved 9 credits.
	if err := db.Where("reservation_id = ?", rid).
		Delete(&model.CreditReservationAllocation{}).Error; err != nil {
		t.Fatalf("drop allocations: %v", err)
	}
	past := time.Now().Add(-1 * time.Hour)
	db.Model(&model.CreditReservation{}).Where("id = ?", rid).Update("expires_at", past)

	swept, failed, err := svc.SweepExpiredReservations(db, 100, 0)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if swept != 0 || failed != 1 {
		t.Errorf("sweep counts: swept=%d failed=%d, want 0 1", swept, failed)
	}
	var r model.CreditReservation
	db.First(&r, rid)
	if r.Status != model.CreditReservationStatusRefundPending {
		t.Errorf("status = %q, want refund_pending", r.Status)
	}
	if r.LastRefundErrorCode != "allocation_incomplete" || r.RefundAttempts != 1 {
		t.Errorf("refund audit = code:%q attempts:%d, want allocation_incomplete/1", r.LastRefundErrorCode, r.RefundAttempts)
	}
	if got := packUsed(t, db, packID); got != 9 {
		t.Fatalf("failed refund changed pack used to %d, want 9", got)
	}
	swept, failed, err = svc.SweepExpiredReservations(db, 100, 0)
	if err != nil || swept != 0 || failed != 0 {
		t.Fatalf("not-due retry sweep = %d/%d err=%v, want 0/0/nil", swept, failed, err)
	}
	if err := db.First(&r, rid).Error; err != nil || r.RefundAttempts != 1 {
		t.Fatalf("not-due retry attempts=%d err=%v, want 1", r.RefundAttempts, err)
	}

	if err := db.Create(&model.CreditReservationAllocation{
		ReservationID: rid, PackID: packID, Credits: 9,
	}).Error; err != nil {
		t.Fatalf("repair allocation: %v", err)
	}
	due := time.Now().Add(-time.Minute)
	if err := db.Model(&model.CreditReservation{}).Where("id = ?", rid).Update("next_refund_at", due).Error; err != nil {
		t.Fatalf("make retry due: %v", err)
	}
	swept, failed, err = svc.SweepExpiredReservations(db, 100, 0)
	if err != nil || swept != 1 || failed != 0 {
		t.Fatalf("repaired sweep = %d/%d err=%v, want 1/0/nil", swept, failed, err)
	}
	if got := packUsed(t, db, packID); got != 0 {
		t.Fatalf("repaired refund pack used = %d, want 0", got)
	}
	if err := db.First(&r, rid).Error; err != nil {
		t.Fatal(err)
	}
	if r.Status != model.CreditReservationStatusExpired || r.ReleasedAt == nil {
		t.Fatalf("repaired state = %q releasedAt=%v, want expired/non-nil", r.Status, r.ReleasedAt)
	}
}

func TestSweepExpiredReservations_ExcludesAgentBoundRows(t *testing.T) {
	// Agent settlement owns every bound Reservation state. The generic sweeper
	// must leave both an expired hold and a due refund retry untouched, while a
	// normal reservation discovered in the same batch is still reclaimed.
	db := testutil.NewTestDB(t)
	uid, packID := seedFreePackOf(t, db, 100)
	svc := NewCreditReservationService()

	reserve := func(key string) model.CreditReservation {
		t.Helper()
		var reservation model.CreditReservation
		if err := db.Transaction(func(tx *gorm.DB) error {
			result, err := svc.Reserve(tx, ReservationRequest{
				UID: uid, Tool: "agent_test", IdempotencyKey: key, Reserved: 10,
			})
			if err != nil {
				return err
			}
			reservation = *result.Reservation
			return nil
		}); err != nil {
			t.Fatalf("reserve %s: %v", key, err)
		}
		return reservation
	}

	boundExpired := reserve("bound-expired")
	boundPending := reserve("bound-refund-pending")
	ordinaryExpired := reserve("ordinary-expired")

	// Missing allocation evidence makes Finalize persist a due refund intent
	// without changing the pack debit. This row would be retried by the legacy
	// sweeper if the immutable Agent binding were ignored.
	if err := db.Where("reservation_id = ?", boundPending.Id).
		Delete(&model.CreditReservationAllocation{}).Error; err != nil {
		t.Fatalf("drop bound pending allocations: %v", err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, err := svc.FinalizeWithResult(tx, boundPending.Id, 7)
		return err
	}); err != nil {
		t.Fatalf("queue bound pending refund: %v", err)
	}

	past := time.Now().Add(-time.Hour)
	if err := db.Model(&model.CreditReservation{}).
		Where("id IN ?", []uint{boundExpired.Id, ordinaryExpired.Id}).
		Update("expires_at", past).Error; err != nil {
		t.Fatalf("force expired reservations: %v", err)
	}
	if err := db.Model(&model.CreditReservation{}).
		Where("id = ?", boundPending.Id).
		Update("next_refund_at", past).Error; err != nil {
		t.Fatalf("make bound refund retry due: %v", err)
	}

	if err := db.First(&boundPending, boundPending.Id).Error; err != nil {
		t.Fatalf("reload pending reservation: %v", err)
	}
	if boundPending.Status != model.CreditReservationStatusRefundPending || boundPending.RefundAttempts != 1 {
		t.Fatalf("pending setup state = %q attempts=%d, want refund_pending/1", boundPending.Status, boundPending.RefundAttempts)
	}
	bindReservationToAgentTurnForSweepTest(t, db, boundExpired)
	bindReservationToAgentTurnForSweepTest(t, db, boundPending)

	swept, failed, err := svc.SweepExpiredReservations(db, 100, 0)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if swept != 1 || failed != 0 {
		t.Fatalf("sweep counts = %d/%d, want ordinary row only (1/0)", swept, failed)
	}
	if got := packUsed(t, db, packID); got != 20 {
		t.Fatalf("post-sweep credits_used = %d, want 20 from two bound rows", got)
	}

	var gotBoundExpired, gotBoundPending, gotOrdinary model.CreditReservation
	if err := db.First(&gotBoundExpired, boundExpired.Id).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&gotBoundPending, boundPending.Id).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&gotOrdinary, ordinaryExpired.Id).Error; err != nil {
		t.Fatal(err)
	}
	if gotBoundExpired.Status != model.CreditReservationStatusReserved || gotBoundExpired.ReleasedAt != nil {
		t.Errorf("bound expired row = status:%q releasedAt:%v, want reserved/nil", gotBoundExpired.Status, gotBoundExpired.ReleasedAt)
	}
	if gotBoundPending.Status != model.CreditReservationStatusRefundPending || gotBoundPending.RefundAttempts != 1 {
		t.Errorf("bound pending row = status:%q attempts:%d, want refund_pending/1", gotBoundPending.Status, gotBoundPending.RefundAttempts)
	}
	if gotOrdinary.Status != model.CreditReservationStatusExpired || gotOrdinary.ReleasedAt == nil {
		t.Errorf("ordinary row = status:%q releasedAt:%v, want expired/non-nil", gotOrdinary.Status, gotOrdinary.ReleasedAt)
	}
}

func TestCreditsPackService_GetReservedPendingSplitsDebitedFromFinalized(t *testing.T) {
	db := testutil.NewTestDB(t)
	uid, _ := seedFreePackOf(t, db, 100)
	reservationSvc := NewCreditReservationService()
	packSvc := NewCreditsPackService()

	var finalizedID uint
	if err := db.Transaction(func(tx *gorm.DB) error {
		pending, err := reservationSvc.Reserve(tx, ReservationRequest{
			UID: uid, Tool: "test", IdempotencyKey: "pending", Reserved: 10,
		})
		if err != nil {
			return err
		}
		_ = pending
		finalized, err := reservationSvc.Reserve(tx, ReservationRequest{
			UID: uid, Tool: "test", IdempotencyKey: "finalized", Reserved: 7,
		})
		if err != nil {
			return err
		}
		finalizedID = finalized.Reservation.Id
		return reservationSvc.Finalize(tx, finalizedID, 7)
	}); err != nil {
		t.Fatalf("seed reservations: %v", err)
	}

	total, used, remaining, err := packSvc.GetBalanceTx(db, uid)
	if err != nil {
		t.Fatalf("GetBalanceTx: %v", err)
	}
	pending, err := packSvc.GetReservedPendingTx(db, uid)
	if err != nil {
		t.Fatalf("GetReservedPendingTx: %v", err)
	}
	if total != 100 || used != 17 || remaining != 83 {
		t.Fatalf("balance = total:%d used:%d remaining:%d, want 100/17/83", total, used, remaining)
	}
	if pending != 10 {
		t.Fatalf("pending = %d, want 10", pending)
	}
	if finalized := used - pending; finalized != 7 {
		t.Fatalf("finalized used = %d, want 7", finalized)
	}

	// TTL is an authorization boundary, not an accounting transition. Until
	// the sweeper atomically refunds this row and marks it terminal, the Pack
	// debit is still economically pending and must stay out of finalized spend.
	past := time.Now().Add(-time.Hour)
	if err := db.Model(&model.CreditReservation{}).
		Where("uid = ? AND idempotency_key = ?", uid, "pending").
		Update("expires_at", past).Error; err != nil {
		t.Fatalf("expire pending row by clock: %v", err)
	}
	pending, err = packSvc.GetReservedPendingTx(db, uid)
	if err != nil {
		t.Fatalf("GetReservedPendingTx after TTL: %v", err)
	}
	if pending != 10 {
		t.Fatalf("pending after TTL = %d, want 10 until terminal refund", pending)
	}
}

func TestEnsureCurrentSubscriptionCredits_DoesNotDuplicateExistingSubscriptionCyclePack(t *testing.T) {
	db := testutil.NewTestDB(t)
	now := time.Now()
	originalPlans := globals.GraConf.Stripe.Plans
	globals.GraConf.Stripe.Plans = map[string]config.SubscriptionPlan{
		"annual": {MonthlyCredits: 123},
	}
	t.Cleanup(func() {
		globals.GraConf.Stripe.Plans = originalPlans
	})
	memberStart := now.AddDate(0, -1, 0)
	cycleStart, cycleEnd, err := subscriptionCycleBounds(memberStart, now)
	if err != nil {
		t.Fatalf("subscription cycle bounds: %v", err)
	}
	user := model.User{
		Member:          model.MEMBER_SUBSCRIPTION_PRO,
		MemberStartTime: memberStart,
		MemberEndTime:   now.AddDate(0, 1, 0),
		Nickname:        "subscriber",
		Email:           "subscriber@example.com",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	sourceID := "annual:" + cycleStart.UTC().Format("20060102")
	expiresAt := cycleEnd
	if err := db.Create(&model.CreditsPack{
		UID:          int(user.Id),
		SourceType:   model.CreditsSourceSubscription,
		SourceID:     sourceID,
		CreditsTotal: 123,
		CreditsUsed:  0,
		ExpiresAt:    &expiresAt,
		Remark:       "subscription credits (annual)",
	}).Error; err != nil {
		t.Fatalf("seed pack: %v", err)
	}

	svc := NewCreditsPackService()
	if err := svc.ensureCurrentSubscriptionCreditsForUserTx(db, user, int(user.Id), "annual", 123, now); err != nil {
		t.Fatalf("ensureCurrentSubscriptionCreditsForUserTx: %v", err)
	}

	var count int64
	if err := db.Model(&model.CreditsPack{}).
		Where("uid = ? AND source_type = ? AND source_id = ?", user.Id, model.CreditsSourceSubscription, sourceID).
		Count(&count).Error; err != nil {
		t.Fatalf("count packs: %v", err)
	}
	if count != 1 {
		t.Fatalf("subscription cycle pack count = %d, want 1", count)
	}
}

func TestGetBalanceForUserTx_IsReadOnlyForMissingSubscriptionCyclePack(t *testing.T) {
	db := testutil.NewTestDB(t)
	now := time.Now()
	user := model.User{
		Member:          model.MEMBER_SUBSCRIPTION_PRO,
		MemberStartTime: now.AddDate(0, -1, 0),
		MemberEndTime:   now.AddDate(0, 1, 0),
		Nickname:        "subscriber-readonly",
		Email:           "subscriber-readonly@example.com",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}

	svc := NewCreditsPackService()
	total, used, remaining, err := svc.GetBalanceForUserTx(db, user)
	if err != nil {
		t.Fatalf("GetBalanceForUserTx: %v", err)
	}
	if total != 0 || used != 0 || remaining != 0 {
		t.Fatalf("balance = %d/%d/%d, want 0/0/0 without a pre-existing pack", total, used, remaining)
	}

	var count int64
	if err := db.Model(&model.CreditsPack{}).
		Where("uid = ? AND source_type = ?", user.Id, model.CreditsSourceSubscription).
		Count(&count).Error; err != nil {
		t.Fatalf("count packs: %v", err)
	}
	if count != 0 {
		t.Fatalf("GetBalanceForUserTx created %d subscription packs, want pure read path", count)
	}
}

func TestBackfillActiveSubscriptionCreditsAfter_AdvancesCursor(t *testing.T) {
	db := testutil.NewTestDB(t)
	if err := db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_test_credits_pack_source ON w_credits_pack(uid, source_type, source_id)").Error; err != nil {
		t.Fatalf("create test credits pack unique index: %v", err)
	}
	now := time.Now()
	originalPlans := globals.GraConf.Stripe.Plans
	globals.GraConf.Stripe.Plans = map[string]config.SubscriptionPlan{
		"annual": {MonthlyCredits: 80},
	}
	t.Cleanup(func() {
		globals.GraConf.Stripe.Plans = originalPlans
	})

	for i := 0; i < 3; i++ {
		subscriptionID := fmt.Sprintf("sub_cursor_%d", i)
		user := model.User{
			Member: model.MEMBER_SUBSCRIPTION_PRO,
			// This column stores the external billing identity, not planKey.
			MemberSubscription: subscriptionID,
			MemberStartTime:    now.AddDate(0, -1, 0),
			MemberEndTime:      now.AddDate(0, 1, 0),
			Nickname:           fmt.Sprintf("subscriber-cursor-%d", i),
			Email:              fmt.Sprintf("subscriber-cursor-%d@example.com", i),
		}
		if err := db.Create(&user).Error; err != nil {
			t.Fatalf("seed user %d: %v", i, err)
		}
		order := model.Order{
			UID: int(user.Id), No: fmt.Sprintf("annual-order-%d", i),
			ProductID: "annual", Status: model.STATUS_COMPLETE,
			PayMethod: "stripe", PayTime: now.Add(-time.Hour),
			Name: "Annual", SubscriptionID: subscriptionID,
			OrderMode: model.ORDER_MODE_SUBSCRIPTION, OrderType: model.ORDER_TYPE_MEMBER,
		}
		if err := db.Create(&order).Error; err != nil {
			t.Fatalf("seed member order %d: %v", i, err)
		}
	}

	svc := NewCreditsPackService()
	ensured, skipped, failed, lastID, hasMore, err := svc.BackfillActiveSubscriptionCreditsAfter(db, 0, 2)
	if err != nil {
		t.Fatalf("first backfill page: %v", err)
	}
	if ensured != 2 || skipped != 0 || failed != 0 || lastID == 0 || !hasMore {
		t.Fatalf("first page ensured/skipped/failed/last/hasMore = %d/%d/%d/%d/%v, want 2/0/0/>0/true", ensured, skipped, failed, lastID, hasMore)
	}

	ensured, skipped, failed, lastID, hasMore, err = svc.BackfillActiveSubscriptionCreditsAfter(db, lastID, 2)
	if err != nil {
		t.Fatalf("second backfill page: %v", err)
	}
	if ensured != 1 || skipped != 0 || failed != 0 || lastID == 0 || hasMore {
		t.Fatalf("second page ensured/skipped/failed/last/hasMore = %d/%d/%d/%d/%v, want 1/0/0/>0/false", ensured, skipped, failed, lastID, hasMore)
	}

	var count int64
	if err := db.Model(&model.CreditsPack{}).
		Where("source_type = ?", model.CreditsSourceSubscription).
		Count(&count).Error; err != nil {
		t.Fatalf("count packs: %v", err)
	}
	if count != 3 {
		t.Fatalf("subscription pack count = %d, want 3", count)
	}
}

func TestBackfillAnnualSubscription_UsesBillingOrderAndPreservesLegacyPack(t *testing.T) {
	db := testutil.NewTestDB(t)
	if err := db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_test_credits_pack_source ON w_credits_pack(uid, source_type, source_id)").Error; err != nil {
		t.Fatalf("create test Credits Pack unique index: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	originalPlans := globals.GraConf.Stripe.Plans
	globals.GraConf.Stripe.Plans = map[string]config.SubscriptionPlan{
		"annual": {MonthlyCredits: 80},
	}
	t.Cleanup(func() { globals.GraConf.Stripe.Plans = originalPlans })

	memberStart := now.AddDate(0, -1, 0)
	user := model.User{
		Member: model.MEMBER_SUBSCRIPTION_PRO, MemberSubscription: "sub_real_annual",
		MemberStartTime: memberStart, MemberEndTime: now.AddDate(0, 11, 0),
		Nickname: "annual-real-sub", Email: "annual-real-sub@example.com",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed annual user: %v", err)
	}
	order := model.Order{
		UID: int(user.Id), No: "annual-real-order", ProductID: "annual",
		Status: model.STATUS_COMPLETE, PayMethod: "stripe", PayTime: now.Add(-time.Hour),
		Name: "Annual", SubscriptionID: "sub_real_annual",
		OrderMode: model.ORDER_MODE_SUBSCRIPTION, OrderType: model.ORDER_TYPE_MEMBER,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("seed annual billing Order: %v", err)
	}
	legacyExpiry := now.Add(48 * time.Hour)
	legacy := model.CreditsPack{
		UID: int(user.Id), SourceType: model.CreditsSourceSubscription,
		SourceID: "subscription", CreditsTotal: 100, CreditsUsed: 20,
		ExpiresAt: &legacyExpiry, Remark: "legacy subscription credits (annual)",
	}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatalf("seed legacy subscription Pack: %v", err)
	}

	ensured, skipped, failed, _, _, err := NewCreditsPackService().
		BackfillActiveSubscriptionCreditsAfter(db, 0, 10)
	if err != nil || ensured != 1 || skipped != 0 || failed != 0 {
		t.Fatalf("annual backfill = ensured:%d skipped:%d failed:%d err:%v", ensured, skipped, failed, err)
	}

	var unchanged model.CreditsPack
	if err := db.First(&unchanged, legacy.Id).Error; err != nil {
		t.Fatal(err)
	}
	if unchanged.SourceID != "subscription" || unchanged.CreditsTotal != 100 || unchanged.CreditsUsed != 20 ||
		unchanged.ExpiresAt == nil || !unchanged.ExpiresAt.Equal(legacyExpiry) {
		t.Fatalf("backfill mutated legacy Pack: %#v", unchanged)
	}
	var packs []model.CreditsPack
	if err := db.Where("uid = ? AND source_type = ?", user.Id, model.CreditsSourceSubscription).
		Order("id ASC").Find(&packs).Error; err != nil {
		t.Fatalf("load annual subscription Packs: %v", err)
	}
	if len(packs) != 1 || packs[0].Id != legacy.Id {
		t.Fatalf("backfill minted an overlapping current-cycle Pack: %#v", packs)
	}
}

func TestDeferredSubscriptionCycle_LegacyExpiryCannotMintSecondPackInSameCycle(t *testing.T) {
	db := testutil.NewTestDB(t)
	previous := globals.GraConf
	globals.GraConf.Stripe.Plans = map[string]config.SubscriptionPlan{
		"annual": {MonthlyCredits: 80},
	}
	t.Cleanup(func() { globals.GraConf = previous })

	cycleStart := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	cycleEnd := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	legacyExpiry := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)
	user := model.User{
		Member:             model.MEMBER_SUBSCRIPTION_PRO,
		MemberSubscription: "sub_legacy_short_cycle",
		MemberStartTime:    cycleStart,
		MemberEndTime:      cycleEnd.AddDate(0, 11, 0),
		Nickname:           "legacy-short-cycle",
		Email:              "legacy-short-cycle@example.com",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	order := model.Order{
		UID: int(user.Id), No: "legacy-short-cycle-order", ProductID: "annual",
		Status: model.STATUS_COMPLETE, PayMethod: "stripe", PayTime: cycleStart,
		Name: "Annual", SubscriptionID: user.MemberSubscription,
		OrderMode: model.ORDER_MODE_SUBSCRIPTION, OrderType: model.ORDER_TYPE_MEMBER,
		CreditsAmount: 80,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	legacy := model.CreditsPack{
		UID: int(user.Id), SourceType: model.CreditsSourceSubscription,
		SourceID: "legacy:short-cycle", CreditsTotal: 80, ExpiresAt: &legacyExpiry,
	}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}

	svc := NewCreditsPackService()
	for _, at := range []time.Time{
		time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC),
		// Even after the legacy Pack expires, its cycle ownership remains
		// evidence that this allowance was already granted. Losing access is
		// safer than minting a second full allowance inside the same cycle.
		time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC),
	} {
		if err := db.Transaction(func(tx *gorm.DB) error {
			return svc.ensureCurrentSubscriptionCreditsTx(tx, int(user.Id), at)
		}); err != nil {
			t.Fatalf("ensure at %v: %v", at, err)
		}
	}

	var packs []model.CreditsPack
	if err := db.Where("uid = ? AND source_type = ?", user.Id, model.CreditsSourceSubscription).
		Order("id ASC").Find(&packs).Error; err != nil {
		t.Fatal(err)
	}
	if len(packs) != 1 || packs[0].Id != legacy.Id {
		t.Fatalf("same-cycle legacy replay minted a second Pack: %#v", packs)
	}
}

func TestDeferredSubscriptionCycle_RejectsLegacyPackOverlappingNextCycle(t *testing.T) {
	db := testutil.NewTestDB(t)
	previous := globals.GraConf
	globals.GraConf.Stripe.Plans = map[string]config.SubscriptionPlan{
		"annual": {MonthlyCredits: 80},
	}
	t.Cleanup(func() { globals.GraConf = previous })

	cycleStart := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	cycleEnd := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	user := model.User{
		Member:             model.MEMBER_SUBSCRIPTION_PRO,
		MemberSubscription: "sub_legacy_overlap",
		MemberStartTime:    cycleStart,
		MemberEndTime:      cycleEnd.AddDate(0, 11, 0),
		Nickname:           "legacy-overlap",
		Email:              "legacy-overlap@example.com",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	order := model.Order{
		UID: int(user.Id), No: "legacy-overlap-order", ProductID: "annual",
		Status: model.STATUS_COMPLETE, PayMethod: "stripe", PayTime: cycleStart,
		Name: "Annual", SubscriptionID: user.MemberSubscription,
		OrderMode: model.ORDER_MODE_SUBSCRIPTION, OrderType: model.ORDER_TYPE_MEMBER,
		CreditsAmount: 80,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	overlapExpiry := cycleEnd.Add(time.Hour)
	if err := db.Create(&model.CreditsPack{
		UID: int(user.Id), SourceType: model.CreditsSourceSubscription,
		SourceID: "legacy:overlap", CreditsTotal: 80, ExpiresAt: &overlapExpiry,
	}).Error; err != nil {
		t.Fatal(err)
	}

	err := db.Transaction(func(tx *gorm.DB) error {
		return NewCreditsPackService().ensureCurrentSubscriptionCreditsTx(
			tx, int(user.Id), time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC),
		)
	})
	if err == nil || !strings.Contains(err.Error(), "overlaps the next cycle") {
		t.Fatalf("overlapping legacy Pack error = %v", err)
	}
}

func TestSubscriptionCycleBounds_UsesAnchoredClampedHalfOpenMonths(t *testing.T) {
	anchor := time.Date(2025, 1, 31, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		now       time.Time
		wantStart time.Time
		wantEnd   time.Time
	}{
		{
			name:      "before february boundary",
			now:       time.Date(2025, 2, 28, 9, 59, 59, 0, time.UTC),
			wantStart: anchor,
			wantEnd:   time.Date(2025, 2, 28, 10, 0, 0, 0, time.UTC),
		},
		{
			name:      "exact february boundary enters next cycle",
			now:       time.Date(2025, 2, 28, 10, 0, 0, 0, time.UTC),
			wantStart: time.Date(2025, 2, 28, 10, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2025, 3, 31, 10, 0, 0, 0, time.UTC),
		},
		{
			name:      "march preserves original day anchor",
			now:       time.Date(2025, 3, 31, 10, 0, 0, 0, time.UTC),
			wantStart: time.Date(2025, 3, 31, 10, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2025, 4, 30, 10, 0, 0, 0, time.UTC),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			start, end, err := subscriptionCycleBounds(anchor, test.now)
			if err != nil {
				t.Fatal(err)
			}
			if !start.Equal(test.wantStart) || !end.Equal(test.wantEnd) {
				t.Fatalf("bounds = %v..%v, want %v..%v", start, end, test.wantStart, test.wantEnd)
			}
		})
	}
	if _, _, err := subscriptionCycleBounds(time.Time{}, anchor); err == nil {
		t.Fatal("zero cycle anchor must fail closed")
	}
}

func TestDeferredSubscriptionCycle_DerivesStableOrderAnchorAndFrozenCredits(t *testing.T) {
	db := testutil.NewTestDB(t)
	if err := db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_test_credits_pack_source ON w_credits_pack(uid, source_type, source_id)").Error; err != nil {
		t.Fatalf("create test Credits Pack unique index: %v", err)
	}
	originalPlans := globals.GraConf.Stripe.Plans
	globals.GraConf.Stripe.Plans = map[string]config.SubscriptionPlan{
		"annual": {MonthlyCredits: 80},
	}
	t.Cleanup(func() { globals.GraConf.Stripe.Plans = originalPlans })

	anchor := time.Date(2026, 1, 31, 10, 0, 0, 0, time.UTC)
	user := model.User{
		Member: model.MEMBER_SUBSCRIPTION_PRO, MemberSubscription: "sub_anchor",
		// Intentionally zero: legacy rows must derive from durable Order.PayTime,
		// never the current day.
		MemberStartTime: time.Time{}, MemberEndTime: time.Date(2027, 1, 31, 10, 0, 0, 0, time.UTC),
		Nickname: "anchor", Email: "anchor@example.com",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed anchored user: %v", err)
	}
	order := model.Order{
		UID: int(user.Id), No: "annual-anchor-order", ProductID: "annual",
		Status: model.STATUS_COMPLETE, PayMethod: "stripe", PayTime: anchor,
		Name: "Annual", SubscriptionID: "sub_anchor",
		OrderMode: model.ORDER_MODE_SUBSCRIPTION, OrderType: model.ORDER_TYPE_MEMBER,
		CreditsAmount: 80,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("seed anchored Order: %v", err)
	}

	svc := NewCreditsPackService()
	ensureAt := func(now time.Time) {
		t.Helper()
		if err := db.Transaction(func(tx *gorm.DB) error {
			return svc.ensureCurrentSubscriptionCreditsTx(tx, int(user.Id), now)
		}); err != nil {
			t.Fatalf("ensure at %v: %v", now, err)
		}
	}
	ensureAt(time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC))
	// A day later remains the same anchored cycle, not a new date-derived Pack.
	ensureAt(time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC))

	var packs []model.CreditsPack
	if err := db.Where("uid = ? AND source_type = ?", user.Id, model.CreditsSourceSubscription).
		Order("id ASC").Find(&packs).Error; err != nil {
		t.Fatal(err)
	}
	if len(packs) != 1 || packs[0].CreditsTotal != 80 {
		t.Fatalf("same-cycle Packs = %#v, want one 80-credit Pack", packs)
	}

	// Mutable config no longer rewrites a paid Order's frozen entitlement. The
	// next absent cycle still grants the durable 80-credit snapshot.
	globals.GraConf.Stripe.Plans["annual"] = config.SubscriptionPlan{MonthlyCredits: 800}
	ensureAt(time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC))
	if err := db.Where("uid = ? AND source_type = ?", user.Id, model.CreditsSourceSubscription).
		Order("id ASC").Find(&packs).Error; err != nil {
		t.Fatal(err)
	}
	if len(packs) != 2 || packs[0].CreditsTotal != 80 || packs[1].CreditsTotal != 80 {
		t.Fatalf("cross-cycle frozen Packs = %#v, want two 80-credit Packs", packs)
	}
}

func TestFindActive_SkipsExpiredZombieRow(t *testing.T) {
	// Between TTL elapse and the sweep cron firing, a row is
	// reserved+expired but not yet status=expired. FindActive must
	// treat it as gone — otherwise a worker that calls Finalize on
	// the zombie races the sweeper and corrupts state.
	db := testutil.NewTestDB(t)
	uid, packID := seedFreePackOf(t, db, 100)
	svc := NewCreditReservationService()

	var rid uint
	db.Transaction(func(tx *gorm.DB) error {
		res, _ := svc.Reserve(tx, ReservationRequest{
			UID: uid, Tool: "test", IdempotencyKey: "zombie",
			Reserved: 3, TTL: 1 * time.Millisecond,
		})
		rid = res.Reservation.Id
		return nil
	})

	past := time.Now().Add(-1 * time.Hour)
	db.Model(&model.CreditReservation{}).Where("id = ?", rid).Update("expires_at", past)

	got, err := svc.FindActive(db, uid, "zombie")
	if err != nil {
		t.Fatalf("FindActive: %v", err)
	}
	if got != nil {
		t.Errorf("FindActive returned zombie row id=%d, want nil", got.Id)
	}

	// Settlement lookup must still find the same row and release the real Pack
	// debit. Using FindActive here used to silently skip this refund window.
	if err := db.Transaction(func(tx *gorm.DB) error {
		settlement, err := svc.FindForSettlement(tx, uid, "zombie")
		if err != nil {
			return err
		}
		if settlement == nil || settlement.Id != rid {
			return fmt.Errorf("settlement lookup = %+v, want reservation %d", settlement, rid)
		}
		return svc.Release(tx, settlement.Id)
	}); err != nil {
		t.Fatalf("settle expired-by-clock reservation: %v", err)
	}
	if got := packUsed(t, db, packID); got != 0 {
		t.Fatalf("Pack used after settlement release = %d, want 0", got)
	}
}

// ---------------------------------------------------------------------
// P1 #6 slice 2 — project budget gate
// ---------------------------------------------------------------------

// seedProjectWithCap creates a w_global_project row owned by uid
// with the given budget cap. Returns the project id for the
// reservation request to reference.
func seedProjectWithCap(t *testing.T, db *gorm.DB, uid int, cap *int) uint {
	t.Helper()
	p := &model.CanvasProject{
		UID:               uid,
		UUID:              "proj-" + t.Name(),
		Title:             "fixture",
		Visibility:        0,
		BudgetCreditsCap:  cap,
		BudgetCreditsUsed: 0,
	}
	if err := db.Create(p).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	return p.Id
}

func TestReserve_ProjectBudgetCapBlocksOverspend(t *testing.T) {
	// Project cap = 50, reservation asks for 100 → must reject
	// BEFORE the user pack is debited (rollback safety). The
	// transaction's error → outer Tx rolls back the pack debit
	// too.
	cap := 50
	db := testutil.NewTestDB(t)
	uid, packID := seedFreePackOf(t, db, 1000)
	projID := seedProjectWithCap(t, db, uid, &cap)
	svc := NewCreditReservationService()

	err := db.Transaction(func(tx *gorm.DB) error {
		_, e := svc.Reserve(tx, ReservationRequest{
			UID: uid, Tool: "test", IdempotencyKey: "k1",
			Reserved: 100, ProjectID: projID,
		})
		return e
	})
	if err == nil {
		t.Fatal("expected error on project-cap exceeded")
	}
	// User pack must be untouched (Tx rolled back).
	if got := packUsed(t, db, packID); got != 0 {
		t.Errorf("user pack used = %d, want 0 (Tx rollback)", got)
	}
	// No reservation row created.
	var count int64
	db.Model(&model.CreditReservation{}).Count(&count)
	if count != 0 {
		t.Errorf("reservation row count = %d, want 0", count)
	}
}

func TestReserve_ProjectBudgetWithinCapSucceeds(t *testing.T) {
	// Cap = 100, reservation asks for 60 → both user pack and
	// project budget tally update.
	cap := 100
	db := testutil.NewTestDB(t)
	uid, _ := seedFreePackOf(t, db, 1000)
	projID := seedProjectWithCap(t, db, uid, &cap)
	svc := NewCreditReservationService()

	err := db.Transaction(func(tx *gorm.DB) error {
		_, e := svc.Reserve(tx, ReservationRequest{
			UID: uid, Tool: "test", IdempotencyKey: "ok",
			Reserved: 60, ProjectID: projID,
		})
		return e
	})
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	// Project budget reflects the ceiling charge.
	var p model.CanvasProject
	if err := db.First(&p, projID).Error; err != nil {
		t.Fatal(err)
	}
	if p.BudgetCreditsUsed != 60 {
		t.Errorf("project Used = %d, want 60 (full Reserve charge)", p.BudgetCreditsUsed)
	}
}

func TestFinalize_RefundsDiffToProject(t *testing.T) {
	// Reserve 60, Finalize with used=40 → project budget tally
	// drops from 60 → 40 (the 20 diff refunded). User pack
	// mirrors the same flow on its side (existing test pins).
	cap := 100
	db := testutil.NewTestDB(t)
	uid, _ := seedFreePackOf(t, db, 1000)
	projID := seedProjectWithCap(t, db, uid, &cap)
	svc := NewCreditReservationService()

	var rid uint
	if err := db.Transaction(func(tx *gorm.DB) error {
		res, e := svc.Reserve(tx, ReservationRequest{
			UID: uid, Tool: "test", IdempotencyKey: "fin",
			Reserved: 60, ProjectID: projID,
		})
		if e != nil {
			return e
		}
		rid = res.Reservation.Id
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.Transaction(func(tx *gorm.DB) error {
		return svc.Finalize(tx, rid, 40)
	}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	var p model.CanvasProject
	db.First(&p, projID)
	if p.BudgetCreditsUsed != 40 {
		t.Errorf("project Used = %d, want 40 (60 charged - 20 refund diff)", p.BudgetCreditsUsed)
	}
}

func TestRelease_RefundsFullToProject(t *testing.T) {
	// Reserve 60, Release (operation failed) → project Used
	// returns to 0.
	cap := 100
	db := testutil.NewTestDB(t)
	uid, _ := seedFreePackOf(t, db, 1000)
	projID := seedProjectWithCap(t, db, uid, &cap)
	svc := NewCreditReservationService()

	var rid uint
	if err := db.Transaction(func(tx *gorm.DB) error {
		res, e := svc.Reserve(tx, ReservationRequest{
			UID: uid, Tool: "test", IdempotencyKey: "rel",
			Reserved: 60, ProjectID: projID,
		})
		if e != nil {
			return e
		}
		rid = res.Reservation.Id
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.Transaction(func(tx *gorm.DB) error {
		return svc.Release(tx, rid)
	}); err != nil {
		t.Fatalf("Release: %v", err)
	}

	var p model.CanvasProject
	db.First(&p, projID)
	if p.BudgetCreditsUsed != 0 {
		t.Errorf("project Used = %d, want 0 (full refund on Release)", p.BudgetCreditsUsed)
	}
}

func TestReserve_UncappedProjectStillIncrementsTally(t *testing.T) {
	// nil cap = unlimited. Project still tracks Used so the
	// REST surface can render "X credits spent" even without
	// a hard cap.
	db := testutil.NewTestDB(t)
	uid, _ := seedFreePackOf(t, db, 1000)
	projID := seedProjectWithCap(t, db, uid, nil) // no cap
	svc := NewCreditReservationService()

	err := db.Transaction(func(tx *gorm.DB) error {
		_, e := svc.Reserve(tx, ReservationRequest{
			UID: uid, Tool: "test", IdempotencyKey: "unc",
			Reserved: 500, ProjectID: projID,
		})
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	var p model.CanvasProject
	db.First(&p, projID)
	if p.BudgetCreditsUsed != 500 {
		t.Errorf("uncapped project Used = %d, want 500", p.BudgetCreditsUsed)
	}
}

func TestReserve_NoProjectScopeSkipsBudgetPath(t *testing.T) {
	// ProjectID=0 = no project scope. Reserve must succeed
	// without touching any project row, even when one with the
	// same uid exists. Pin the no-op path so a future regression
	// that always-queries the project repo doesn't blow up
	// non-agent reservations.
	cap := 1
	db := testutil.NewTestDB(t)
	uid, _ := seedFreePackOf(t, db, 1000)
	// Seed a low-cap project but DON'T pass its id to Reserve.
	_ = seedProjectWithCap(t, db, uid, &cap)
	svc := NewCreditReservationService()

	err := db.Transaction(func(tx *gorm.DB) error {
		_, e := svc.Reserve(tx, ReservationRequest{
			UID: uid, Tool: "test", IdempotencyKey: "no-proj",
			Reserved: 100, ProjectID: 0,
		})
		return e
	})
	if err != nil {
		t.Errorf("Reserve without ProjectID should ignore project budgets, got %v", err)
	}
}

func TestCanonicalReservationRequestDigestFreezesOnlyImmutableIdentity(t *testing.T) {
	request := ReservationRequest{
		UID: 17, Tool: "workagent", IdempotencyKey: "canonical-key", QuoteID: "quote-v1",
		Reserved: 12, ProjectID: 23, TTL: 15 * time.Minute, Remark: "first audit note",
	}
	digest, err := CanonicalReservationRequestDigest(request)
	if err != nil || len(digest) != reservationDigestBytes {
		t.Fatalf("CanonicalReservationRequestDigest() = %q, %v", digest, err)
	}

	policyDrift := request
	policyDrift.TTL = 90 * time.Minute
	policyDrift.Remark = "different audit note"
	policyDigest, err := CanonicalReservationRequestDigest(policyDrift)
	if err != nil || policyDigest != digest {
		t.Fatalf("TTL/Remark digest = %q, %v, want %q", policyDigest, err, digest)
	}

	identityMutations := map[string]func(*ReservationRequest){
		"uid":             func(value *ReservationRequest) { value.UID++ },
		"tool":            func(value *ReservationRequest) { value.Tool += "-other" },
		"idempotency key": func(value *ReservationRequest) { value.IdempotencyKey += "-other" },
		"quote":           func(value *ReservationRequest) { value.QuoteID += "-other" },
		"reserved":        func(value *ReservationRequest) { value.Reserved++ },
		"project":         func(value *ReservationRequest) { value.ProjectID++ },
	}
	for name, mutate := range identityMutations {
		t.Run(name, func(t *testing.T) {
			changed := request
			mutate(&changed)
			changedDigest, err := CanonicalReservationRequestDigest(changed)
			if err != nil || changedDigest == digest {
				t.Fatalf("changed digest = %q, %v, original %q", changedDigest, err, digest)
			}
		})
	}

	invalid := map[string]ReservationRequest{
		"uid zero":       func() ReservationRequest { value := request; value.UID = 0; return value }(),
		"reserved minus": func() ReservationRequest { value := request; value.Reserved = -1; return value }(),
		"tool empty":     func() ReservationRequest { value := request; value.Tool = ""; return value }(),
		"key empty":      func() ReservationRequest { value := request; value.IdempotencyKey = ""; return value }(),
		"quote too long": func() ReservationRequest {
			value := request
			value.QuoteID = strings.Repeat("q", maxReservationQuoteIDBytes+1)
			return value
		}(),
		"project int32": func() ReservationRequest {
			value := request
			value.ProjectID = uint(maxReservationSchemaInt + 1)
			return value
		}(),
	}
	if strconv.IntSize > 32 {
		tooLarge := int(maxReservationSchemaInt + 1)
		uid := request
		uid.UID = tooLarge
		invalid["uid int32"] = uid
		reserved := request
		reserved.Reserved = tooLarge
		invalid["reserved int32"] = reserved
	}
	for name, value := range invalid {
		t.Run(name, func(t *testing.T) {
			if digest, err := CanonicalReservationRequestDigest(value); err == nil || digest != "" {
				t.Fatalf("invalid digest = %q, %v", digest, err)
			}
		})
	}
}

func TestReserve_SameKeyDifferentImmutableRequestFailsBeforeDebit(t *testing.T) {
	db := testutil.NewTestDB(t)
	uid, packID := seedFreePackOf(t, db, 100)
	svc := NewCreditReservationService()
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, err := svc.Reserve(tx, ReservationRequest{
			UID: uid, Tool: "test", IdempotencyKey: "immutable", Reserved: 10,
		})
		return err
	}); err != nil {
		t.Fatalf("first Reserve: %v", err)
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		_, err := svc.Reserve(tx, ReservationRequest{
			UID: uid, Tool: "test", IdempotencyKey: "immutable", Reserved: 11,
		})
		return err
	})
	if !errors.Is(err, ErrReservationReplayConflict) {
		t.Fatalf("conflicting replay error = %v, want ErrReservationReplayConflict", err)
	}
	if got := packUsed(t, db, packID); got != 10 {
		t.Fatalf("conflicting replay changed pack used to %d, want 10", got)
	}
}

func TestReserve_SameKeyTimeoutPolicyDriftReplaysOriginalAdmission(t *testing.T) {
	db := testutil.NewTestDB(t)
	uid, packID := seedFreePackOf(t, db, 100)
	svc := NewCreditReservationService()
	var first ReservationResult
	if err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		first, err = svc.Reserve(tx, ReservationRequest{
			UID: uid, Tool: "workagent", IdempotencyKey: "timeout-drift",
			Reserved: 10, TTL: 35 * time.Minute,
		})
		return err
	}); err != nil {
		t.Fatalf("first Reserve: %v", err)
	}

	var replay ReservationResult
	if err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		replay, err = svc.Reserve(tx, ReservationRequest{
			UID: uid, Tool: "workagent", IdempotencyKey: "timeout-drift",
			Reserved: 10, TTL: 65 * time.Minute,
		})
		return err
	}); err != nil {
		t.Fatalf("timeout-policy replay: %v", err)
	}
	if replay.Created || replay.Reservation.Id != first.Reservation.Id {
		t.Fatalf("replay = created:%v id:%d, want existing id:%d", replay.Created, replay.Reservation.Id, first.Reservation.Id)
	}
	if !replay.Reservation.ExpiresAt.Equal(first.Reservation.ExpiresAt) {
		t.Fatalf("replay changed durable expiry from %s to %s", first.Reservation.ExpiresAt, replay.Reservation.ExpiresAt)
	}
	if got := packUsed(t, db, packID); got != 10 {
		t.Fatalf("timeout-policy replay changed Pack used to %d, want 10", got)
	}
}

func TestReserve_ConcurrentSameKeyCreatesOneDebit(t *testing.T) {
	db := testutil.NewTestDB(t)
	uid, packID := seedFreePackOf(t, db, 100)
	svc := NewCreditReservationService()
	const callers = 32
	start := make(chan struct{})
	errs := make(chan error, callers)
	created := make(chan bool, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			err := db.Transaction(func(tx *gorm.DB) error {
				result, err := svc.Reserve(tx, ReservationRequest{
					UID: uid, Tool: "concurrent", IdempotencyKey: "same-key-32", Reserved: 10,
				})
				if err == nil {
					created <- result.Created
				}
				return err
			})
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	close(created)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Reserve: %v", err)
		}
	}
	createdCount := 0
	for value := range created {
		if value {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Fatalf("Created=true count = %d, want 1", createdCount)
	}
	if got := packUsed(t, db, packID); got != 10 {
		t.Fatalf("pack used = %d, want one 10-credit debit", got)
	}
	var count int64
	if err := db.Model(&model.CreditReservation{}).
		Where("uid = ? AND idempotency_key = ?", uid, "same-key-32").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("reservation rows = %d, want 1", count)
	}
}

func TestReserve_NonDuplicateInsertFailureIsNotDisguisedAsReplay(t *testing.T) {
	db := testutil.NewTestDB(t)
	uid, packID := seedFreePackOf(t, db, 100)
	svc := NewCreditReservationService()
	request := ReservationRequest{
		UID: uid, Tool: "triggered", IdempotencyKey: "triggered-key", Reserved: 10,
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, err := svc.Reserve(tx, request)
		return err
	}); err != nil {
		t.Fatalf("seed Reserve: %v", err)
	}
	if err := db.Exec(`CREATE TRIGGER reject_triggered_reservation
		BEFORE INSERT ON w_credit_reservation
		WHEN NEW.idempotency_key = 'triggered-key'
		BEGIN SELECT RAISE(ABORT, 'injected non-duplicate insert failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		_, err := svc.Reserve(tx, request)
		return err
	})
	if err == nil || errors.Is(err, ErrReservationReplayConflict) {
		t.Fatalf("trigger failure = %v, want raw non-duplicate insert failure", err)
	}
	if got := packUsed(t, db, packID); got != 10 {
		t.Fatalf("failed insert changed pack used to %d, want 10", got)
	}
}

func TestReviewHold_ExemptsTTLAndRequiresExactResolution(t *testing.T) {
	db := testutil.NewTestDB(t)
	uid, packID := seedFreePackOf(t, db, 100)
	svc := NewCreditReservationService()
	var rid uint
	if err := db.Transaction(func(tx *gorm.DB) error {
		res, err := svc.Reserve(tx, ReservationRequest{
			UID: uid, Tool: "agent", IdempotencyKey: "review", Reserved: 10,
		})
		if err != nil {
			return err
		}
		rid = res.Reservation.Id
		return nil
	}); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	hold := ReservationReviewHold{
		ReviewID: "review-1", SettlementKey: "settlement-1",
		RequestDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	if err := db.Transaction(func(tx *gorm.DB) error { return svc.HoldForReview(tx, rid, hold) }); err != nil {
		t.Fatalf("HoldForReview: %v", err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error { return svc.HoldForReview(tx, rid, hold) }); err != nil {
		t.Fatalf("HoldForReview exact replay: %v", err)
	}
	conflict := hold
	conflict.ReviewID = "review-2"
	if err := db.Transaction(func(tx *gorm.DB) error { return svc.HoldForReview(tx, rid, conflict) }); !errors.Is(err, ErrReservationReviewConflict) {
		t.Fatalf("different hold error = %v, want ErrReservationReviewConflict", err)
	}
	past := time.Now().Add(-time.Hour)
	if err := db.Model(&model.CreditReservation{}).Where("id = ?", rid).Update("expires_at", past).Error; err != nil {
		t.Fatal(err)
	}
	swept, failed, err := svc.SweepExpiredReservations(db, 100, 0)
	if err != nil || swept != 0 || failed != 0 {
		t.Fatalf("held sweep = %d/%d err=%v, want 0/0/nil", swept, failed, err)
	}
	if got := packUsed(t, db, packID); got != 10 {
		t.Fatalf("held sweep changed pack used to %d, want 10", got)
	}
	active, err := svc.FindActive(db, uid, "review")
	if err != nil || active == nil || active.Status != model.CreditReservationStatusReviewHold {
		t.Fatalf("FindActive held = %+v err=%v", active, err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error { return svc.Finalize(tx, rid, 7) }); !errors.Is(err, ErrReservationReviewPending) {
		t.Fatalf("ordinary Finalize error = %v, want ErrReservationReviewPending", err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error { return svc.FinalizeReview(tx, rid, hold, 7) }); err != nil {
		t.Fatalf("FinalizeReview: %v", err)
	}
	if got := packUsed(t, db, packID); got != 7 {
		t.Fatalf("review finalize pack used = %d, want 7", got)
	}
	if err := db.Transaction(func(tx *gorm.DB) error { return svc.Finalize(tx, rid, 7) }); !errors.Is(err, ErrReservationReviewPending) {
		t.Fatalf("ordinary terminal replay error = %v, want ErrReservationReviewPending", err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error { return svc.FinalizeReview(tx, rid, hold, 7) }); err != nil {
		t.Fatalf("exact review terminal replay: %v", err)
	}
}

func TestSweep_ProjectScopedReservationRefundsProjectAndPack(t *testing.T) {
	cap := 100
	db := testutil.NewTestDB(t)
	uid, packID := seedFreePackOf(t, db, 100)
	projectID := seedProjectWithCap(t, db, uid, &cap)
	svc := NewCreditReservationService()
	var rid uint
	if err := db.Transaction(func(tx *gorm.DB) error {
		res, err := svc.Reserve(tx, ReservationRequest{
			UID: uid, Tool: "agent", IdempotencyKey: "project-expire", Reserved: 20, ProjectID: projectID,
		})
		if err != nil {
			return err
		}
		rid = res.Reservation.Id
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	db.Model(&model.CreditReservation{}).Where("id = ?", rid).Update("expires_at", past)
	swept, failed, err := svc.SweepExpiredReservations(db, 100, 0)
	if err != nil || swept != 1 || failed != 0 {
		t.Fatalf("Sweep = %d/%d err=%v, want 1/0/nil", swept, failed, err)
	}
	if got := packUsed(t, db, packID); got != 0 {
		t.Fatalf("pack used = %d, want 0", got)
	}
	var projectRow model.CanvasProject
	if err := db.First(&projectRow, projectID).Error; err != nil {
		t.Fatal(err)
	}
	if projectRow.BudgetCreditsUsed != 0 {
		t.Fatalf("project budget used = %d, want 0", projectRow.BudgetCreditsUsed)
	}
}

func TestSweep_PackFailureRollsBackEarlierProjectRefund(t *testing.T) {
	cap := 100
	db := testutil.NewTestDB(t)
	uid, packID := seedFreePackOf(t, db, 100)
	projectID := seedProjectWithCap(t, db, uid, &cap)
	svc := NewCreditReservationService()
	var rid uint
	if err := db.Transaction(func(tx *gorm.DB) error {
		res, err := svc.Reserve(tx, ReservationRequest{
			UID: uid, Tool: "agent", IdempotencyKey: "project-refund-fault", Reserved: 20, ProjectID: projectID,
		})
		if err != nil {
			return err
		}
		rid = res.Reservation.Id
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// The allocation remains immutable, but the pack balance is corrupted so
	// the checked refund fails after the Project row has been decremented. The
	// savepoint must restore that Project mutation.
	if err := db.Model(&model.CreditsPack{}).Where("id = ?", packID).Update("credits_used", 0).Error; err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	db.Model(&model.CreditReservation{}).Where("id = ?", rid).Update("expires_at", past)
	swept, failed, err := svc.SweepExpiredReservations(db, 100, 0)
	if err != nil || swept != 0 || failed != 1 {
		t.Fatalf("Sweep = %d/%d err=%v, want 0/1/nil", swept, failed, err)
	}
	var projectRow model.CanvasProject
	if err := db.First(&projectRow, projectID).Error; err != nil {
		t.Fatal(err)
	}
	if projectRow.BudgetCreditsUsed != 20 {
		t.Fatalf("failed pack refund left project budget at %d, want rolled back 20", projectRow.BudgetCreditsUsed)
	}
	var reservation model.CreditReservation
	if err := db.First(&reservation, rid).Error; err != nil {
		t.Fatal(err)
	}
	if reservation.Status != model.CreditReservationStatusRefundPending || reservation.LastRefundErrorCode != "pack_invariant" {
		t.Fatalf("reservation = status:%q code:%q, want refund_pending/pack_invariant", reservation.Status, reservation.LastRefundErrorCode)
	}
}

func TestSweep_SecondPackStatementFailureRollsBackFirstPackUpdate(t *testing.T) {
	db := testutil.NewTestDB(t)
	user := model.User{Member: 0, Nickname: "two-pack", Email: "two-pack@example.com"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	first := model.CreditsPack{
		UID: int(user.Id), SourceType: model.CreditsSourcePurchase, SourceID: "first",
		CreditsTotal: 5,
	}
	second := model.CreditsPack{
		UID: int(user.Id), SourceType: model.CreditsSourcePurchase, SourceID: "second",
		CreditsTotal: 5,
	}
	if err := db.Create(&first).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	svc := NewCreditReservationService()
	var rid uint
	if err := db.Transaction(func(tx *gorm.DB) error {
		res, err := svc.Reserve(tx, ReservationRequest{
			UID: int(user.Id), Tool: "test", IdempotencyKey: "second-pack-fault", Reserved: 10,
		})
		if err != nil {
			return err
		}
		rid = res.Reservation.Id
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	trigger := fmt.Sprintf(`CREATE TRIGGER fail_second_pack_refund
		BEFORE UPDATE OF credits_used ON w_credits_pack
		WHEN OLD.id = %d AND NEW.credits_used < OLD.credits_used
		BEGIN SELECT RAISE(ABORT, 'injected second pack failure'); END`, second.Id)
	if err := db.Exec(trigger).Error; err != nil {
		t.Fatalf("create fault trigger: %v", err)
	}
	past := time.Now().Add(-time.Hour)
	if err := db.Model(&model.CreditReservation{}).Where("id = ?", rid).Update("expires_at", past).Error; err != nil {
		t.Fatal(err)
	}
	swept, failed, err := svc.SweepExpiredReservations(db, 100, 0)
	if err != nil || swept != 0 || failed != 1 {
		t.Fatalf("faulted Sweep = %d/%d err=%v, want 0/1/nil", swept, failed, err)
	}
	if got := packUsed(t, db, first.Id); got != 5 {
		t.Fatalf("first pack update was not rolled back: used=%d want=5", got)
	}
	if got := packUsed(t, db, second.Id); got != 5 {
		t.Fatalf("second pack used=%d, want 5", got)
	}
	if err := db.Exec("DROP TRIGGER fail_second_pack_refund").Error; err != nil {
		t.Fatal(err)
	}
	due := time.Now().Add(-time.Minute)
	if err := db.Model(&model.CreditReservation{}).Where("id = ?", rid).Update("next_refund_at", due).Error; err != nil {
		t.Fatal(err)
	}
	swept, failed, err = svc.SweepExpiredReservations(db, 100, 0)
	if err != nil || swept != 1 || failed != 0 {
		t.Fatalf("recovered Sweep = %d/%d err=%v, want 1/0/nil", swept, failed, err)
	}
	if got := packUsed(t, db, first.Id); got != 0 {
		t.Fatalf("first pack after retry used=%d, want 0", got)
	}
	if got := packUsed(t, db, second.Id); got != 0 {
		t.Fatalf("second pack after retry used=%d, want 0", got)
	}
}
