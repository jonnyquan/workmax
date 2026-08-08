package account

import (
	"errors"
	"strings"
	"testing"
	"time"

	"server/model"
	"server/utils/testutil"

	"gorm.io/gorm"
)

func seedPackLockingUser(t *testing.T, db *gorm.DB) int {
	t.Helper()
	user := model.User{Member: model.MEMBER_SUBSCRIPTION_FREE, Nickname: "pack-locking", Email: "pack-locking@example.com"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return int(user.Id)
}

func seedPackLockingPack(
	t *testing.T,
	db *gorm.DB,
	uid int,
	sourceID string,
	total int,
	used int,
	expiresAt *time.Time,
) model.CreditsPack {
	t.Helper()
	pack := model.CreditsPack{
		UID: uid, SourceType: model.CreditsSourcePurchase, SourceID: sourceID,
		CreditsTotal: total, CreditsUsed: used, ExpiresAt: expiresAt,
	}
	if err := db.Create(&pack).Error; err != nil {
		t.Fatalf("seed pack %s: %v", sourceID, err)
	}
	return pack
}

func reloadPackUsed(t *testing.T, db *gorm.DB, packID uint) int {
	t.Helper()
	var pack model.CreditsPack
	if err := db.First(&pack, packID).Error; err != nil {
		t.Fatalf("reload pack %d: %v", packID, err)
	}
	return pack.CreditsUsed
}

func seedRefundReservation(
	t *testing.T,
	db *gorm.DB,
	uid int,
	reserved int,
	key string,
) model.CreditReservation {
	t.Helper()
	expiresAt := time.Now().Add(time.Hour)
	if err := db.Exec(
		`INSERT INTO w_credit_reservation
			(uid, tool, idempotency_key, request_digest, reserved, status, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		uid, "pack-locking-test", key, strings.Repeat("a", 64), reserved,
		model.CreditReservationStatusReserved, expiresAt,
	).Error; err != nil {
		t.Fatalf("seed reservation: %v", err)
	}
	var reservation model.CreditReservation
	if err := db.Where("uid = ? AND idempotency_key = ?", uid, key).First(&reservation).Error; err != nil {
		t.Fatalf("reload reservation: %v", err)
	}
	return reservation
}

func TestReserveCreditsDetailedTx_LocksByIDButAllocatesByExpiry(t *testing.T) {
	db := testutil.NewTestDB(t)
	uid := seedPackLockingUser(t, db)
	later := time.Now().Add(2 * time.Hour)
	earlier := time.Now().Add(time.Hour)

	// Primary-key order deliberately conflicts with business priority:
	// never-expiring has the smallest ID; earliest-expiring has the largest.
	never := seedPackLockingPack(t, db, uid, "never", 10, 0, nil)
	late := seedPackLockingPack(t, db, uid, "late", 10, 0, &later)
	early := seedPackLockingPack(t, db, uid, "early", 10, 0, &earlier)

	var allocations []creditsPackAllocation
	if err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		allocations, err = NewCreditsPackService().ReserveCreditsDetailedTx(tx, uid, 12)
		return err
	}); err != nil {
		t.Fatalf("reserve: %v", err)
	}

	if len(allocations) != 2 {
		t.Fatalf("allocations = %#v, want two rows", allocations)
	}
	if allocations[0].PackID != early.Id || allocations[0].Credits != 10 ||
		allocations[1].PackID != late.Id || allocations[1].Credits != 2 {
		t.Fatalf("allocation order/amount = %#v, want early:10 then late:2", allocations)
	}
	if got := reloadPackUsed(t, db, never.Id); got != 0 {
		t.Fatalf("never-expiring used = %d, want 0", got)
	}
	if got := reloadPackUsed(t, db, late.Id); got != 2 {
		t.Fatalf("later-expiring used = %d, want 2", got)
	}
	if got := reloadPackUsed(t, db, early.Id); got != 10 {
		t.Fatalf("earlier-expiring used = %d, want 10", got)
	}
}

func TestCreditsPackAndMembershipExpiryUseHalfOpenBoundary(t *testing.T) {
	db := testutil.NewTestDB(t)
	uid := seedPackLockingUser(t, db)
	boundary := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	seedPackLockingPack(t, db, uid, "boundary", 10, 0, &boundary)

	total, used, err := NewCreditsPackService().balanceSumTx(db, uid, boundary)
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 || used != 0 {
		t.Fatalf("Pack at exact expires_at remained active: total=%d used=%d", total, used)
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		_, err := NewCreditsPackService().ReserveCreditsDetailedAtTx(tx, uid, 1, boundary)
		return err
	})
	if !errors.Is(err, ErrInsufficientCredits) {
		t.Fatalf("reserve at exact expires_at error = %v, want insufficient credits", err)
	}
	if isSubscriptionUserActive(model.User{
		Member: model.MEMBER_SUBSCRIPTION_PRO, MemberEndTime: boundary,
	}, boundary) {
		t.Fatal("membership at exact member_end_time remained active")
	}
}

func TestRefundAllocationsCheckedTx_PreservesLIFOAmountsWithCanonicalPackOrder(t *testing.T) {
	db := testutil.NewTestDB(t)
	uid := seedPackLockingUser(t, db)
	first := seedPackLockingPack(t, db, uid, "first", 20, 5, nil)
	second := seedPackLockingPack(t, db, uid, "second", 20, 7, nil)
	reservation := seedRefundReservation(t, db, uid, 12, "lifo")
	allocations := []model.CreditReservationAllocation{
		{ReservationID: reservation.Id, PackID: first.Id, Credits: 5},
		{ReservationID: reservation.Id, PackID: second.Id, Credits: 7},
	}
	if err := db.Create(&allocations).Error; err != nil {
		t.Fatalf("seed allocations: %v", err)
	}

	if err := db.Transaction(func(tx *gorm.DB) error {
		return NewCreditsPackService().RefundAllocationsCheckedTx(tx, reservation.Id, uid, 12, 6)
	}); err != nil {
		t.Fatalf("refund: %v", err)
	}

	// Allocation LIFO refunds the second allocation even though Pack updates
	// themselves are issued in ascending primary-key order.
	if got := reloadPackUsed(t, db, first.Id); got != 5 {
		t.Fatalf("first pack used = %d, want 5", got)
	}
	if got := reloadPackUsed(t, db, second.Id); got != 1 {
		t.Fatalf("second pack used = %d, want 1", got)
	}
}

func TestBuildRefundAllocationPlan_RejectsMalformedImmutableRows(t *testing.T) {
	tests := []struct {
		name        string
		allocations []model.CreditReservationAllocation
	}{
		{
			name: "duplicate pack",
			allocations: []model.CreditReservationAllocation{
				{ReservationID: 7, PackID: 11, Credits: 2},
				{ReservationID: 7, PackID: 11, Credits: 3},
			},
		},
		{
			name: "zero credits",
			allocations: []model.CreditReservationAllocation{
				{ReservationID: 7, PackID: 11, Credits: 0},
			},
		},
		{
			name: "wrong reservation",
			allocations: []model.CreditReservationAllocation{
				{ReservationID: 8, PackID: 11, Credits: 5},
			},
		},
		{
			name: "zero pack",
			allocations: []model.CreditReservationAllocation{
				{ReservationID: 7, PackID: 0, Credits: 5},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := buildRefundAllocationPlan(7, test.allocations, 5, 3, true)
			if !errors.Is(err, ErrRefundAllocationIntegrity) {
				t.Fatalf("plan error = %v, want allocation integrity", err)
			}
			var integrity *RefundIntegrityError
			if !errors.As(err, &integrity) {
				t.Fatalf("plan error type = %T, want *RefundIntegrityError", err)
			}
		})
	}
}

func TestRefundAllocationsCheckedTx_IntegrityFailuresDoNotMutatePacks(t *testing.T) {
	tests := []struct {
		name         string
		packUsed     int
		expected     int
		refund       int
		allocations  func(reservationID, packID uint) []model.CreditReservationAllocation
		wantSentinel error
	}{
		{
			name: "allocation sum mismatch", packUsed: 5, expected: 6, refund: 3,
			allocations: func(reservationID, packID uint) []model.CreditReservationAllocation {
				return []model.CreditReservationAllocation{
					{ReservationID: reservationID, PackID: packID, Credits: 5},
				}
			},
			wantSentinel: ErrRefundAllocationIntegrity,
		},
		{
			name: "pack no longer contains allocation", packUsed: 2, expected: 5, refund: 2,
			allocations: func(reservationID, packID uint) []model.CreditReservationAllocation {
				return []model.CreditReservationAllocation{
					{ReservationID: reservationID, PackID: packID, Credits: 5},
				}
			},
			wantSentinel: ErrRefundPackIntegrity,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			uid := seedPackLockingUser(t, db)
			pack := seedPackLockingPack(t, db, uid, "integrity", 20, test.packUsed, nil)
			reservation := seedRefundReservation(t, db, uid, test.expected, "integrity")
			allocations := test.allocations(reservation.Id, pack.Id)
			if err := db.Create(&allocations).Error; err != nil {
				t.Fatalf("seed allocations: %v", err)
			}
			err := db.Transaction(func(tx *gorm.DB) error {
				return NewCreditsPackService().RefundAllocationsCheckedTx(
					tx, reservation.Id, uid, test.expected, test.refund,
				)
			})
			if !errors.Is(err, test.wantSentinel) {
				t.Fatalf("refund error = %v, want errors.Is(%v)", err, test.wantSentinel)
			}
			var integrity *RefundIntegrityError
			if !errors.As(err, &integrity) {
				t.Fatalf("refund error type = %T, want *RefundIntegrityError", err)
			}
			if integrity.ReservationID != reservation.Id {
				t.Fatalf("error reservation = %d, want %d", integrity.ReservationID, reservation.Id)
			}
			if got := reloadPackUsed(t, db, pack.Id); got != test.packUsed {
				t.Fatalf("pack mutated on integrity error: used=%d want=%d", got, test.packUsed)
			}
		})
	}
}

func TestCreditsPackDeletionIsRestrictedWhileAllocationExists(t *testing.T) {
	db := testutil.NewTestDB(t)
	uid := seedPackLockingUser(t, db)
	pack := seedPackLockingPack(t, db, uid, "allocation-owner", 20, 5, nil)
	reservation := seedRefundReservation(t, db, uid, 5, "allocation-owner")
	allocation := model.CreditReservationAllocation{
		ReservationID: reservation.Id, PackID: pack.Id, Credits: 5,
	}
	if err := db.Create(&allocation).Error; err != nil {
		t.Fatalf("seed allocation: %v", err)
	}

	if err := db.Delete(&model.CreditsPack{}, pack.Id).Error; err == nil {
		t.Fatal("deleting a Pack with an allocation must be RESTRICTed")
	}
	if got := reloadPackUsed(t, db, pack.Id); got != 5 {
		t.Fatalf("restricted delete mutated pack: used=%d want=5", got)
	}
}

func TestRefundAllocationsCheckedTx_RejectsCrossUserPack(t *testing.T) {
	db := testutil.NewTestDB(t)
	reservationUID := seedPackLockingUser(t, db)
	otherUID := seedPackLockingUser(t, db)
	pack := seedPackLockingPack(t, db, otherUID, "other-user", 20, 5, nil)
	reservation := seedRefundReservation(t, db, reservationUID, 5, "cross-user-pack")
	if err := db.Create(&model.CreditReservationAllocation{
		ReservationID: reservation.Id, PackID: pack.Id, Credits: 5,
	}).Error; err != nil {
		t.Fatalf("seed cross-user allocation: %v", err)
	}

	err := db.Transaction(func(tx *gorm.DB) error {
		return NewCreditsPackService().RefundAllocationsCheckedTx(
			tx, reservation.Id, reservationUID, 5, 5,
		)
	})
	if !errors.Is(err, ErrCreditsRefundPackInvariant) {
		t.Fatalf("cross-user refund error = %v, want ErrCreditsRefundPackInvariant", err)
	}
	if got := reloadPackUsed(t, db, pack.Id); got != 5 {
		t.Fatalf("cross-user refund mutated pack used to %d, want 5", got)
	}
}

func TestRefundAllocationsTx_CompatibilityWrapperPrevalidatesTotal(t *testing.T) {
	db := testutil.NewTestDB(t)
	uid := seedPackLockingUser(t, db)
	pack := seedPackLockingPack(t, db, uid, "compat", 20, 5, nil)
	reservation := seedRefundReservation(t, db, uid, 5, "compat")
	allocation := model.CreditReservationAllocation{
		ReservationID: reservation.Id, PackID: pack.Id, Credits: 5,
	}
	if err := db.Create(&allocation).Error; err != nil {
		t.Fatalf("seed allocation: %v", err)
	}

	err := db.Transaction(func(tx *gorm.DB) error {
		return NewCreditsPackService().RefundAllocationsTx(tx, reservation.Id, 6)
	})
	if !errors.Is(err, ErrRefundAllocationIntegrity) {
		t.Fatalf("refund error = %v, want allocation integrity", err)
	}
	if got := reloadPackUsed(t, db, pack.Id); got != 5 {
		t.Fatalf("pack mutated on prevalidation failure: used=%d want=5", got)
	}
}

func TestUpsertSubscriptionPackTx_PreservesReservationDebitLedger(t *testing.T) {
	db := testutil.NewTestDB(t)
	uid := seedPackLockingUser(t, db)
	expires := time.Now().Add(time.Hour)
	pack := model.CreditsPack{
		UID: uid, SourceType: model.CreditsSourceSubscription, SourceID: "subscription",
		CreditsTotal: 100, CreditsUsed: 20, ExpiresAt: &expires,
	}
	if err := db.Create(&pack).Error; err != nil {
		t.Fatalf("seed subscription Pack: %v", err)
	}

	nextExpiry := time.Now().AddDate(0, 1, 0)
	if err := db.Transaction(func(tx *gorm.DB) error {
		return NewCreditsPackService().upsertLegacySubscriptionPackTx(
			tx, uid, "monthly", 100, &nextExpiry, "monthly renewal",
		)
	}); err != nil {
		t.Fatalf("renew subscription Pack: %v", err)
	}

	var got model.CreditsPack
	if err := db.First(&got, pack.Id).Error; err != nil {
		t.Fatalf("reload subscription Pack: %v", err)
	}
	if got.CreditsUsed != 20 {
		t.Fatalf("renewal reset credits_used to %d, want immutable 20", got.CreditsUsed)
	}
	if got.CreditsTotal != 120 || got.CreditsTotal-got.CreditsUsed != 100 {
		t.Fatalf("renewed total/used/available = %d/%d/%d, want 120/20/100",
			got.CreditsTotal, got.CreditsUsed, got.CreditsTotal-got.CreditsUsed)
	}
}

func TestCreateSubscriptionCyclePackTx_IsImmutableAndExactReplayIsIdempotent(t *testing.T) {
	db := testutil.NewTestDB(t)
	uid := seedPackLockingUser(t, db)
	oldExpiry := time.Now().Add(time.Hour)
	legacy := model.CreditsPack{
		UID: uid, SourceType: model.CreditsSourceSubscription, SourceID: "subscription",
		CreditsTotal: 100, CreditsUsed: 20, ExpiresAt: &oldExpiry,
	}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatalf("seed legacy Pack: %v", err)
	}

	nextExpiry := time.Now().AddDate(0, 1, 0)
	grant := func(key string) error {
		return db.Transaction(func(tx *gorm.DB) error {
			return NewCreditsPackService().createSubscriptionCyclePackTx(
				tx, uid, "monthly_pro", key, 100, &nextExpiry, "monthly cycle",
			)
		})
	}
	if err := grant("ORDER-cycle-1"); err != nil {
		t.Fatalf("first cycle grant: %v", err)
	}
	if err := grant("ORDER-cycle-1"); err != nil {
		t.Fatalf("exact cycle replay: %v", err)
	}

	sourceID, err := subscriptionCyclePackSourceID("monthly_pro", "ORDER-cycle-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(sourceID) != 64 {
		t.Fatalf("cycle source ID length = %d, want 64", len(sourceID))
	}
	var count int64
	if err := db.Model(&model.CreditsPack{}).
		Where("uid = ? AND source_type = ? AND source_id = ?", uid, model.CreditsSourceSubscription, sourceID).
		Count(&count).Error; err != nil {
		t.Fatalf("count cycle Pack: %v", err)
	}
	if count != 1 {
		t.Fatalf("exact replay created %d cycle Packs, want 1", count)
	}

	var old model.CreditsPack
	if err := db.First(&old, legacy.Id).Error; err != nil {
		t.Fatal(err)
	}
	if old.CreditsTotal != 100 || old.CreditsUsed != 20 || !old.ExpiresAt.Equal(oldExpiry) {
		t.Fatalf("cycle grant mutated legacy Pack: total=%d used=%d expires=%v",
			old.CreditsTotal, old.CreditsUsed, old.ExpiresAt)
	}
}
