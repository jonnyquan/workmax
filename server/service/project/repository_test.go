package project

import (
	"errors"
	"testing"
	"time"

	"server/model"
	"server/utils/testutil"

	"gorm.io/gorm"
)

// seedProject inserts a canvas_project row with the bare minimum
// fields the repo's predicates exercise (uid, uuid, title,
// visibility, deleted_at). Returns the row so tests can capture
// the assigned id / created_at.
func seedProject(t *testing.T, db *gorm.DB, uid int, title, uuid string) *model.CanvasProject {
	t.Helper()
	p := &model.CanvasProject{
		UID:        uid,
		UUID:       uuid,
		Title:      title,
		Visibility: 0,
	}
	if err := db.Create(p).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	return p
}

func TestLoadByIDForOwner_HappyPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	seeded := seedProject(t, db, 42, "Q1 brand sprint", "uuid-1")
	repo := NewRepository(db)

	got, err := repo.LoadByIDForOwner(seeded.Id, 42)
	if err != nil {
		t.Fatalf("LoadByIDForOwner: %v", err)
	}
	if got == nil || got.Id != seeded.Id {
		t.Fatalf("got %+v, want id=%d", got, seeded.Id)
	}
	if got.Title != "Q1 brand sprint" {
		t.Errorf("title round-trip lost: %q", got.Title)
	}
}

func TestLoadByIDForOwner_CrossTenantReturnsNotFound(t *testing.T) {
	// IDOR guard — user 42's project must NOT be reachable from
	// user 99's repo binding regardless of how the call lands.
	db := testutil.NewTestDB(t)
	seeded := seedProject(t, db, 42, "private", "uuid-2")
	repo := NewRepository(db)

	_, err := repo.LoadByIDForOwner(seeded.Id, 99)
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("expected ErrRecordNotFound, got %v", err)
	}
}

func TestLoadByIDForOwner_ZeroUIDShortCircuits(t *testing.T) {
	// uid=0 means "no authenticated user." Repo must reject
	// without hitting the DB so a system-path caller that lost
	// its uid context can't accidentally surface a project.
	db := testutil.NewTestDB(t)
	seedProject(t, db, 42, "x", "uuid-3")
	repo := NewRepository(db)

	_, err := repo.LoadByIDForOwner(1, 0)
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("expected ErrRecordNotFound for uid=0, got %v", err)
	}
}

func TestLoadByIDForOwner_ZeroIDShortCircuits(t *testing.T) {
	// id=0 is the sentinel for "no project bound" on the asset
	// rows. The repo must not echo a 200 on the first row of
	// the table when called with id=0.
	db := testutil.NewTestDB(t)
	seedProject(t, db, 42, "x", "uuid-3a")
	repo := NewRepository(db)

	_, err := repo.LoadByIDForOwner(0, 42)
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("expected ErrRecordNotFound for id=0, got %v", err)
	}
}

func TestLoadByIDForOwner_SoftDeletedHidden(t *testing.T) {
	db := testutil.NewTestDB(t)
	seeded := seedProject(t, db, 42, "to-delete", "uuid-4")
	if err := db.Delete(seeded).Error; err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	repo := NewRepository(db)

	_, err := repo.LoadByIDForOwner(seeded.Id, 42)
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("soft-deleted project should be invisible, got %v", err)
	}
}

func TestLoadByUUIDForOwner_HappyPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	seedProject(t, db, 42, "share-link", "share-uuid-1")
	repo := NewRepository(db)

	got, err := repo.LoadByUUIDForOwner("share-uuid-1", 42)
	if err != nil {
		t.Fatalf("LoadByUUIDForOwner: %v", err)
	}
	if got == nil || got.UUID != "share-uuid-1" {
		t.Errorf("uuid round-trip lost: %+v", got)
	}
}

func TestLoadByUUIDForOwner_EmptyUUIDReturnsNotFound(t *testing.T) {
	db := testutil.NewTestDB(t)
	seedProject(t, db, 42, "x", "uuid-x")
	repo := NewRepository(db)

	_, err := repo.LoadByUUIDForOwner("", 42)
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("empty uuid should short-circuit, got %v", err)
	}
}

func TestListForOwner_NewestFirst(t *testing.T) {
	db := testutil.NewTestDB(t)
	seedProject(t, db, 42, "older", "u-list-1")
	seedProject(t, db, 42, "middle", "u-list-2")
	seedProject(t, db, 42, "newest", "u-list-3")
	repo := NewRepository(db)

	rows, err := repo.ListForOwner(42, 10, 0)
	if err != nil {
		t.Fatalf("ListForOwner: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	// SQLite stores updated_at to second precision; the inserts
	// can all land in the same second so order isn't guaranteed
	// to be strictly insert-reverse. Assert membership instead.
	titles := map[string]bool{}
	for _, r := range rows {
		titles[r.Title] = true
	}
	for _, want := range []string{"older", "middle", "newest"} {
		if !titles[want] {
			t.Errorf("missing title %q from list", want)
		}
	}
}

func TestListForOwner_ScopedByUID(t *testing.T) {
	db := testutil.NewTestDB(t)
	seedProject(t, db, 42, "mine", "ul-1")
	seedProject(t, db, 99, "foreign", "ul-2")
	repo := NewRepository(db)

	rows, _ := repo.ListForOwner(42, 10, 0)
	if len(rows) != 1 || rows[0].Title != "mine" {
		t.Errorf("expected only own projects; got %+v", rows)
	}
}

func TestListForOwner_ZeroUIDReturnsNil(t *testing.T) {
	db := testutil.NewTestDB(t)
	seedProject(t, db, 42, "x", "ul-z")
	repo := NewRepository(db)

	rows, err := repo.ListForOwner(0, 10, 0)
	if err != nil {
		t.Errorf("uid=0 should not error, got %v", err)
	}
	if rows != nil {
		t.Errorf("uid=0 should return nil, got %v", rows)
	}
}

func TestListForOwner_ClampsLimit(t *testing.T) {
	// Limit 999 is the API-layer "give me everything" sentinel
	// some surfaces send. Repo clamps to its 100 cap so a wide
	// query can't drag the whole table into memory.
	db := testutil.NewTestDB(t)
	for i := 0; i < 5; i++ {
		seedProject(t, db, 42, "p", "u-clamp-")
	}
	repo := NewRepository(db)

	rows, err := repo.ListForOwner(42, 999, -10)
	if err != nil {
		t.Fatalf("ListForOwner: %v", err)
	}
	if len(rows) > 100 {
		t.Errorf("limit not clamped, got %d", len(rows))
	}
}

func TestExistsForOwner_HappyPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	seeded := seedProject(t, db, 42, "x", "u-exists-1")
	repo := NewRepository(db)

	ok, err := repo.ExistsForOwner(seeded.Id, 42)
	if err != nil {
		t.Fatalf("ExistsForOwner: %v", err)
	}
	if !ok {
		t.Error("expected exists=true")
	}
}

func TestExistsForOwner_CrossTenantReturnsFalseNoError(t *testing.T) {
	db := testutil.NewTestDB(t)
	seeded := seedProject(t, db, 42, "x", "u-exists-2")
	repo := NewRepository(db)

	ok, err := repo.ExistsForOwner(seeded.Id, 99)
	if err != nil {
		t.Fatalf("should not error on cross-tenant: %v", err)
	}
	if ok {
		t.Error("cross-tenant exists should be false (IDOR posture)")
	}
}

func TestExistsForOwner_ZeroInputsReturnFalse(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)

	for _, c := range []struct{ id, uid uint }{{0, 42}, {1, 0}, {0, 0}} {
		ok, err := repo.ExistsForOwner(c.id, c.uid)
		if err != nil || ok {
			t.Errorf("(id=%d uid=%d) exists=%v err=%v, want false/nil", c.id, c.uid, ok, err)
		}
	}
}

func TestResolveAccess_OwnerFallbackWithoutMemberRow(t *testing.T) {
	db := testutil.NewTestDB(t)
	p := seedProject(t, db, 42, "owned", "member-owner-fallback")
	repo := NewRepository(db)

	access, err := repo.ResolveAccess(p.Id, 42)
	if err != nil {
		t.Fatalf("ResolveAccess: %v", err)
	}
	if access.Role != model.GlobalProjectRoleOwner || !access.CanManage() || !access.CanEdit() || !access.CanView() {
		t.Fatalf("owner fallback access incorrect: %+v", access)
	}
}

func TestResolveAccess_EditorMemberCanEditNotManage(t *testing.T) {
	db := testutil.NewTestDB(t)
	p := seedProject(t, db, 42, "shared", "member-editor")
	if err := db.Create(&model.GlobalProjectMember{
		ProjectID: p.Id,
		UID:       99,
		Role:      model.GlobalProjectRoleEditor,
		Source:    model.GlobalProjectMemberSourceInvite,
		CreatedBy: 42,
	}).Error; err != nil {
		t.Fatalf("seed member: %v", err)
	}
	repo := NewRepository(db)

	access, err := repo.ResolveAccess(p.Id, 99)
	if err != nil {
		t.Fatalf("ResolveAccess: %v", err)
	}
	if access.Role != model.GlobalProjectRoleEditor {
		t.Fatalf("role = %q", access.Role)
	}
	if !access.CanView() || !access.CanEdit() || access.CanManage() {
		t.Fatalf("editor permissions incorrect: view=%v edit=%v manage=%v", access.CanView(), access.CanEdit(), access.CanManage())
	}
}

func TestResolveAccess_ViewerCannotEdit(t *testing.T) {
	db := testutil.NewTestDB(t)
	p := seedProject(t, db, 42, "shared", "member-viewer")
	if err := db.Create(&model.GlobalProjectMember{
		ProjectID: p.Id,
		UID:       99,
		Role:      model.GlobalProjectRoleViewer,
		Source:    model.GlobalProjectMemberSourceInvite,
		CreatedBy: 42,
	}).Error; err != nil {
		t.Fatalf("seed member: %v", err)
	}
	repo := NewRepository(db)

	canView, err := repo.CanViewProject(p.Id, 99)
	if err != nil {
		t.Fatalf("CanViewProject: %v", err)
	}
	canEdit, err := repo.CanEditProject(p.Id, 99)
	if err != nil {
		t.Fatalf("CanEditProject: %v", err)
	}
	if !canView || canEdit {
		t.Fatalf("viewer canView=%v canEdit=%v, want true/false", canView, canEdit)
	}
}

func TestResolveAccess_NonMemberCannotView(t *testing.T) {
	db := testutil.NewTestDB(t)
	p := seedProject(t, db, 42, "private", "member-none")
	repo := NewRepository(db)

	canView, err := repo.CanViewProject(p.Id, 99)
	if err != nil {
		t.Fatalf("CanViewProject: %v", err)
	}
	if canView {
		t.Fatal("non-member should not view private project")
	}
}

func TestUpsertOwnerMember_Idempotent(t *testing.T) {
	db := testutil.NewTestDB(t)
	p := seedProject(t, db, 42, "owned", "member-upsert")
	repo := NewRepository(db)

	if err := repo.UpsertOwnerMember(p.Id, 42); err != nil {
		t.Fatalf("UpsertOwnerMember: %v", err)
	}
	if err := repo.UpsertOwnerMember(p.Id, 42); err != nil {
		t.Fatalf("second UpsertOwnerMember: %v", err)
	}
	var count int64
	db.Model(&model.GlobalProjectMember{}).Where("project_id = ? AND uid = ?", p.Id, 42).Count(&count)
	if count != 1 {
		t.Fatalf("owner member count = %d, want 1", count)
	}
}

// ---------------------------------------------------------------------
// P1 #6 — budget cap/used tests
// ---------------------------------------------------------------------

func ptrInt(v int) *int { return &v }

func TestGetBudgetStatusForOwner_UncappedDefault(t *testing.T) {
	// Fresh row has nil cap + 0 used → status reports Remaining=-1
	// (the "unlimited" sentinel) and Exceeded=false.
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	p := seedProject(t, db, 42, "uncapped", "uuid-uncapped")

	status, err := repo.GetBudgetStatusForOwner(p.Id, 42)
	if err != nil {
		t.Fatalf("GetBudgetStatusForOwner: %v", err)
	}
	if status.Cap != nil {
		t.Errorf("Cap = %v, want nil", status.Cap)
	}
	if status.Used != 0 {
		t.Errorf("Used = %d, want 0", status.Used)
	}
	if status.Remaining != -1 {
		t.Errorf("Remaining = %d, want -1 (uncapped sentinel)", status.Remaining)
	}
	if status.Exceeded {
		t.Errorf("Exceeded = true, want false")
	}
}

func TestGetBudgetStatusForOwner_CappedComputesRemaining(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	p := seedProject(t, db, 42, "capped", "uuid-capped")
	if err := repo.SetBudgetCapForOwner(p.Id, 42, ptrInt(100)); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddBudgetSpent(p.Id, 42, 30); err != nil {
		t.Fatal(err)
	}

	status, err := repo.GetBudgetStatusForOwner(p.Id, 42)
	if err != nil {
		t.Fatal(err)
	}
	if status.Cap == nil || *status.Cap != 100 {
		t.Errorf("Cap = %v, want 100", status.Cap)
	}
	if status.Used != 30 {
		t.Errorf("Used = %d, want 30", status.Used)
	}
	if status.Remaining != 70 {
		t.Errorf("Remaining = %d, want 70", status.Remaining)
	}
	if status.Exceeded {
		t.Errorf("Exceeded = true at 30/100; want false")
	}
}

func TestSetBudgetCapForOwner_RejectsNegative(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	p := seedProject(t, db, 42, "x", "u-neg")
	err := repo.SetBudgetCapForOwner(p.Id, 42, ptrInt(-1))
	if err == nil {
		t.Fatal("expected error on negative cap")
	}
}

func TestSetBudgetCapForOwner_CrossTenantReturnsNotFound(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	p := seedProject(t, db, 100, "owned-by-100", "u-cross")
	err := repo.SetBudgetCapForOwner(p.Id, 42, ptrInt(50))
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("cross-tenant: errors.Is(err, ErrRecordNotFound) = false; got %v", err)
	}
}

func TestSetBudgetCapForOwner_NilClearsTheCap(t *testing.T) {
	// Set then clear. The row's Cap pointer is nil after the clear
	// — pin so a future "0 means clear" misinterpretation can't
	// silently break the unlimited path.
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	p := seedProject(t, db, 42, "x", "u-clear")
	if err := repo.SetBudgetCapForOwner(p.Id, 42, ptrInt(50)); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetBudgetCapForOwner(p.Id, 42, nil); err != nil {
		t.Fatal(err)
	}
	status, _ := repo.GetBudgetStatusForOwner(p.Id, 42)
	if status.Cap != nil {
		t.Errorf("Cap = %v after clear, want nil", status.Cap)
	}
}

func TestAddBudgetSpent_HappyPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	p := seedProject(t, db, 42, "x", "u-spend")
	if err := repo.SetBudgetCapForOwner(p.Id, 42, ptrInt(100)); err != nil {
		t.Fatal(err)
	}

	if err := repo.AddBudgetSpent(p.Id, 42, 40); err != nil {
		t.Fatalf("AddBudgetSpent: %v", err)
	}
	status, _ := repo.GetBudgetStatusForOwner(p.Id, 42)
	if status.Used != 40 {
		t.Errorf("Used = %d, want 40", status.Used)
	}
}

func TestAddBudgetSpent_UncappedAlwaysAccepts(t *testing.T) {
	// nil cap means unlimited — the conditional UPDATE's
	// `cap IS NULL OR ...` short-circuit accepts any positive
	// charge regardless of how big.
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	p := seedProject(t, db, 42, "x", "u-uncap")
	if err := repo.AddBudgetSpent(p.Id, 42, 1_000_000); err != nil {
		t.Fatalf("AddBudgetSpent: %v", err)
	}
	status, _ := repo.GetBudgetStatusForOwner(p.Id, 42)
	if status.Used != 1_000_000 {
		t.Errorf("Used = %d, want 1_000_000", status.Used)
	}
}

func TestAddBudgetSpent_RejectsOverCap(t *testing.T) {
	// 40 used + 70 charge > 100 cap → ErrBudgetExceeded.
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	p := seedProject(t, db, 42, "x", "u-over")
	if err := repo.SetBudgetCapForOwner(p.Id, 42, ptrInt(100)); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddBudgetSpent(p.Id, 42, 40); err != nil {
		t.Fatal(err)
	}

	err := repo.AddBudgetSpent(p.Id, 42, 70)
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Errorf("errors.Is(err, ErrBudgetExceeded) = false; got %v", err)
	}
	// Used must NOT have been mutated — the conditional UPDATE
	// is the atomicity gate.
	status, _ := repo.GetBudgetStatusForOwner(p.Id, 42)
	if status.Used != 40 {
		t.Errorf("Used = %d, want 40 (cap-reject must not mutate)", status.Used)
	}
}

func TestAddBudgetSpent_BoundaryExactlyAtCapAccepts(t *testing.T) {
	// 0 used + 100 charge = 100 cap → ACCEPT (used <= cap, not <).
	// Pin the inclusive boundary so a "<" vs "<=" regression
	// surfaces here.
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	p := seedProject(t, db, 42, "x", "u-exact")
	if err := repo.SetBudgetCapForOwner(p.Id, 42, ptrInt(100)); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddBudgetSpent(p.Id, 42, 100); err != nil {
		t.Errorf("exact-at-cap should accept, got %v", err)
	}
}

func TestAddBudgetSpent_NegativeDecrementsAsRefund(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	p := seedProject(t, db, 42, "x", "u-refund")
	if err := repo.AddBudgetSpent(p.Id, 42, 50); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddBudgetSpent(p.Id, 42, -20); err != nil {
		t.Fatal(err)
	}
	status, _ := repo.GetBudgetStatusForOwner(p.Id, 42)
	if status.Used != 30 {
		t.Errorf("Used = %d, want 30 (50 - 20)", status.Used)
	}
}

func TestAddBudgetSpent_DoubleRefundDoesNotDriveNegative(t *testing.T) {
	// Idempotency: a double-fire of a refund should not push
	// used below zero. The floor guard is what protects against
	// drift if a slice-2 release hook fires twice on retry.
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	p := seedProject(t, db, 42, "x", "u-double-refund")
	if err := repo.AddBudgetSpent(p.Id, 42, 10); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddBudgetSpent(p.Id, 42, -10); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddBudgetSpent(p.Id, 42, -10); err != nil {
		// Second refund silently no-ops (RowsAffected=0)
		t.Fatalf("second refund should be silent no-op, got %v", err)
	}
	status, _ := repo.GetBudgetStatusForOwner(p.Id, 42)
	if status.Used != 0 {
		t.Errorf("Used = %d, want 0 (clamped at floor)", status.Used)
	}
}

func TestAddBudgetSpent_ZeroIsNoOp(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	p := seedProject(t, db, 42, "x", "u-zero")
	if err := repo.AddBudgetSpent(p.Id, 42, 0); err != nil {
		t.Errorf("0 should be silent no-op, got %v", err)
	}
	status, _ := repo.GetBudgetStatusForOwner(p.Id, 42)
	if status.Used != 0 {
		t.Errorf("Used = %d, want 0", status.Used)
	}
}

func TestRefundBudgetSpentExact_RefundsAndRejectsLedgerDrift(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	p := seedProject(t, db, 42, "x", "u-exact-refund")
	if err := repo.SetBudgetCapForOwner(p.Id, 42, ptrInt(100)); err != nil {
		t.Fatalf("set budget cap: %v", err)
	}
	if err := repo.AddBudgetSpent(p.Id, 42, 40); err != nil {
		t.Fatalf("seed budget charge: %v", err)
	}
	if err := repo.RefundBudgetSpentExact(p.Id, 42, 15); err != nil {
		t.Fatalf("exact refund: %v", err)
	}

	var got model.CanvasProject
	if err := db.First(&got, p.Id).Error; err != nil {
		t.Fatalf("reload project: %v", err)
	}
	if got.BudgetCreditsUsed != 25 {
		t.Fatalf("budget used = %d, want 25", got.BudgetCreditsUsed)
	}

	if err := repo.RefundBudgetSpentExact(p.Id, 42, 30); !errors.Is(err, ErrBudgetRefundInvariant) {
		t.Fatalf("over-refund error = %v, want ErrBudgetRefundInvariant", err)
	}
	if err := db.First(&got, p.Id).Error; err != nil {
		t.Fatalf("reload project after rejected refund: %v", err)
	}
	if got.BudgetCreditsUsed != 25 {
		t.Fatalf("rejected refund changed budget used to %d, want 25", got.BudgetCreditsUsed)
	}
}

func TestRefundBudgetSpentExact_SoftDeletedProjectStillAcceptsLedgerRefund(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	p := seedProject(t, db, 42, "soft-deleted-ledger", "u-soft-deleted-refund")
	if err := repo.AddBudgetSpent(p.Id, 42, 20); err != nil {
		t.Fatalf("seed budget charge: %v", err)
	}
	deletedAt := time.Now()
	if err := db.Model(&model.CanvasProject{}).Where("id = ?", p.Id).
		UpdateColumn("deleted_at", &deletedAt).Error; err != nil {
		t.Fatalf("soft delete project: %v", err)
	}
	if err := repo.RefundBudgetSpentExact(p.Id, 42, 20); err != nil {
		t.Fatalf("refund soft-deleted project: %v", err)
	}

	var got model.CanvasProject
	if err := db.Unscoped().First(&got, p.Id).Error; err != nil {
		t.Fatalf("reload soft-deleted project: %v", err)
	}
	if got.DeletedAt == nil {
		t.Fatal("project unexpectedly restored during refund")
	}
	if got.BudgetCreditsUsed != 0 {
		t.Fatalf("budget used = %d, want 0", got.BudgetCreditsUsed)
	}
}

func TestAddBudgetSpent_CrossTenantReturnsNotFound(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	p := seedProject(t, db, 100, "owned-by-100", "u-cross-spend")
	err := repo.AddBudgetSpent(p.Id, 42, 10)
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("cross-tenant: errors.Is(err, ErrRecordNotFound) = false; got %v", err)
	}
	// Owner's row must NOT have been mutated.
	status, _ := repo.GetBudgetStatusForOwner(p.Id, 100)
	if status.Used != 0 {
		t.Errorf("attacker mutated row across tenants: Used = %d", status.Used)
	}
}
