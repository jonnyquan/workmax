package workagent

// agent_reservation_test.go — pins the (release, finalize) closure
// pair's contract. The finalize signature now takes the
// caller-supplied used-credits (post-turn token-based settle), so
// these tests cover:
//
//   - actual < reserved → partial finalize refunds the delta
//   - actual = reserved → full charge, no refund
//   - actual > reserved → defensive clamp (no Finalize error)
//   - negative actual → clamps to 0
//   - settled flag: release-after-finalize is a no-op
//   - settled flag: finalize-after-release is a no-op

import (
	"testing"

	"server/model"
	"server/service/account"
	"server/utils/testutil"

	"gorm.io/gorm"
)

func seedTestPackAndReservation(t *testing.T, db *gorm.DB, reservedCredits int) (uint, int, uint) {
	t.Helper()
	user := model.User{Member: 0, Nickname: "rc-tester", Email: "rc@example.com"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	pack := model.CreditsPack{
		UID:          int(user.Id),
		SourceType:   model.CreditsSourcePurchase,
		SourceID:     "rc-test-pack",
		CreditsTotal: 100,
		CreditsUsed:  0,
	}
	if err := db.Create(&pack).Error; err != nil {
		t.Fatalf("seed pack: %v", err)
	}
	svc := account.NewCreditReservationService()
	var rid uint
	if err := db.Transaction(func(tx *gorm.DB) error {
		res, err := svc.Reserve(tx, account.ReservationRequest{
			UID:            int(user.Id),
			Tool:           "test",
			IdempotencyKey: "rc-key-" + t.Name(),
			Reserved:       reservedCredits,
		})
		if err != nil {
			return err
		}
		rid = res.Reservation.Id
		return nil
	}); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	return user.Id, int(user.Id), rid
}

func packUsedCredits(t *testing.T, db *gorm.DB, uid int) int {
	t.Helper()
	var p model.CreditsPack
	if err := db.Where("uid = ?", uid).First(&p).Error; err != nil {
		t.Fatalf("reload pack: %v", err)
	}
	return p.CreditsUsed
}

func TestReservationClosures_PartialActualRefundsDelta(t *testing.T) {
	db := testutil.NewTestDB(t)
	_, uid, rid := seedTestPackAndReservation(t, db, 10)
	svc := account.NewCreditReservationService()

	_, finalize := ReservationClosures(db, svc, rid, 10, uid, "[Test]")
	finalize(3)

	if got := packUsedCredits(t, db, uid); got != 3 {
		t.Errorf("pack credits_used = %d, want 3 (7 refunded)", got)
	}
}

func TestReservationClosures_ActualEqualsReserved_NoRefund(t *testing.T) {
	db := testutil.NewTestDB(t)
	_, uid, rid := seedTestPackAndReservation(t, db, 10)
	svc := account.NewCreditReservationService()

	_, finalize := ReservationClosures(db, svc, rid, 10, uid, "[Test]")
	finalize(10)

	if got := packUsedCredits(t, db, uid); got != 10 {
		t.Errorf("pack credits_used = %d, want 10", got)
	}
}

func TestReservationClosures_ActualOverReserved_ClampsNotCrash(t *testing.T) {
	// Defensive clamp: a caller bug that passes wildly large used
	// credits (e.g. usd→credits math returned an outlier) must NOT
	// trip the reservation service's `used > reserved` rejection.
	// We clamp at the boundary and finalize cleanly.
	db := testutil.NewTestDB(t)
	_, uid, rid := seedTestPackAndReservation(t, db, 10)
	svc := account.NewCreditReservationService()

	_, finalize := ReservationClosures(db, svc, rid, 10, uid, "[Test]")
	finalize(9999) // pathological — should clamp to 10

	if got := packUsedCredits(t, db, uid); got != 10 {
		t.Errorf("pack credits_used = %d, want 10 (clamped from 9999)", got)
	}
}

func TestReservationClosures_NegativeActual_ClampsToZero(t *testing.T) {
	// A NaN in usd-to-credit math feeding the finalize call would
	// produce a negative int. Clamp to 0 so the reservation
	// fully refunds rather than crashing the billing path.
	db := testutil.NewTestDB(t)
	_, uid, rid := seedTestPackAndReservation(t, db, 10)
	svc := account.NewCreditReservationService()

	_, finalize := ReservationClosures(db, svc, rid, 10, uid, "[Test]")
	finalize(-5)

	if got := packUsedCredits(t, db, uid); got != 0 {
		t.Errorf("pack credits_used = %d, want 0 (negative clamped)", got)
	}
}

func TestReservationClosures_FinalizeAfterRelease_IsNoOp(t *testing.T) {
	// Settled-flag contract: the first call wins. Release first,
	// then finalize — finalize must be a no-op (no double-debit).
	db := testutil.NewTestDB(t)
	_, uid, rid := seedTestPackAndReservation(t, db, 10)
	svc := account.NewCreditReservationService()

	release, finalize := ReservationClosures(db, svc, rid, 10, uid, "[Test]")
	release("test-failure")
	finalize(7) // should be ignored

	if got := packUsedCredits(t, db, uid); got != 0 {
		t.Errorf("pack credits_used = %d, want 0 (release won)", got)
	}
}

func TestReservationClosures_ReleaseAfterFinalize_IsNoOp(t *testing.T) {
	// Inverse direction — finalize at actuals, then a stray
	// release should NOT re-touch the pack.
	db := testutil.NewTestDB(t)
	_, uid, rid := seedTestPackAndReservation(t, db, 10)
	svc := account.NewCreditReservationService()

	release, finalize := ReservationClosures(db, svc, rid, 10, uid, "[Test]")
	finalize(7)
	release("oops") // ignored

	if got := packUsedCredits(t, db, uid); got != 7 {
		t.Errorf("pack credits_used = %d, want 7 (finalize won, release ignored)", got)
	}
}
