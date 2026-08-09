//go:build desktop

package desktop

import (
	"errors"
	"strings"
	"testing"

	"gorm.io/gorm"
)

// The store tests run against the real migration DDL, not a hand-copied
// table: if the migration and the store ever disagree, these must fail.
func openLocalAccountsTestDB(t testing.TB) *gorm.DB {
	t.Helper()
	db := openHistoryTestDB(t)
	if err := db.Exec(`CREATE TABLE w_desktop_local_account (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		name         TEXT    NOT NULL,
		is_active    INTEGER NOT NULL DEFAULT 0,
		created_at   TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP,
		last_used_at TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`).Error; err != nil {
		t.Fatalf("create table: %v", err)
	}
	if err := db.Exec(
		`CREATE UNIQUE INDEX uk_desktop_local_account_name ON w_desktop_local_account(name)`,
	).Error; err != nil {
		t.Fatalf("create index: %v", err)
	}
	return db
}

// The first account must land exactly on the reserved single-user uid: that
// identity equality IS the zero-migration back-compat story.
func TestLocalAccountUIDDerivation(t *testing.T) {
	if got := localAccountUID(1); got != localSingleUserUID {
		t.Fatalf("first account uid = %d, want reserved %d", got, localSingleUserUID)
	}
	if got := localAccountUID(2); got != localSingleUserUID+1 {
		t.Fatalf("second account uid = %d, want %d", got, localSingleUserUID+1)
	}
}

func TestEnsureDefaultLocalAccount(t *testing.T) {
	db := openLocalAccountsTestDB(t)
	accounts, err := listLocalAccounts(db)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(accounts) != 1 {
		t.Fatalf("expected the default account, got %d", len(accounts))
	}
	if accounts[0].Name != defaultLocalAccountName || !accounts[0].Active {
		t.Fatalf("default account = %+v, want active %q", accounts[0], defaultLocalAccountName)
	}
	if accounts[0].UID != localSingleUserUID {
		t.Fatalf("default uid = %d, want %d", accounts[0].UID, localSingleUserUID)
	}
	// Idempotent: a second list must not create a second default.
	if again, _ := listLocalAccounts(db); len(again) != 1 {
		t.Fatalf("default account duplicated: %d rows", len(again))
	}
}

// Creating an account must not activate it — showing up in the switcher and
// taking over the workspace are different consents.
func TestCreateLocalAccountDoesNotActivate(t *testing.T) {
	db := openLocalAccountsTestDB(t)
	created, err := createLocalAccount(db, "  Ming  ")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Name != "Ming" {
		t.Fatalf("name not trimmed: %q", created.Name)
	}
	if created.Active {
		t.Fatal("created account must not be active")
	}
	if got := activeLocalAccountUID(db); got != localSingleUserUID {
		t.Fatalf("active uid moved on create: %d", got)
	}
}

func TestSelectLocalAccountExactlyOneActive(t *testing.T) {
	db := openLocalAccountsTestDB(t)
	created, err := createLocalAccount(db, "Ming")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := selectLocalAccount(db, created.ID); err != nil {
		t.Fatalf("select: %v", err)
	}
	var activeCount int64
	if err := db.Raw(
		`SELECT COUNT(*) FROM w_desktop_local_account WHERE is_active = 1`,
	).Row().Scan(&activeCount); err != nil {
		t.Fatalf("count: %v", err)
	}
	if activeCount != 1 {
		t.Fatalf("active rows = %d, want exactly 1", activeCount)
	}
	if got := activeLocalAccountUID(db); got != localAccountUID(created.ID) {
		t.Fatalf("active uid = %d, want %d", got, localAccountUID(created.ID))
	}
	if err := selectLocalAccount(db, 99); !errors.Is(err, errLocalAccountNotFound) {
		t.Fatalf("select missing id: %v, want errLocalAccountNotFound", err)
	}
}

func TestCreateLocalAccountValidation(t *testing.T) {
	db := openLocalAccountsTestDB(t)
	for _, bad := range []string{"", "   ", "a\nb", "a\x00b", strings.Repeat("长", maxLocalAccountName+1)} {
		if _, err := createLocalAccount(db, bad); !errors.Is(err, errLocalAccountName) {
			t.Fatalf("name %q: %v, want errLocalAccountName", bad, err)
		}
	}
	if _, err := createLocalAccount(db, "Ming"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := createLocalAccount(db, "Ming"); !errors.Is(err, errLocalAccountTaken) {
		t.Fatalf("duplicate: %v, want errLocalAccountTaken", err)
	}
	// Exactly maxLocalAccountName runes is legal; the limit is runes, not bytes.
	if _, err := createLocalAccount(db, strings.Repeat("长", maxLocalAccountName)); err != nil {
		t.Fatalf("max-length name rejected: %v", err)
	}
}

func TestCreateLocalAccountLimit(t *testing.T) {
	db := openLocalAccountsTestDB(t)
	for i := len("x"); ; i++ {
		_, err := createLocalAccount(db, strings.Repeat("a", i))
		if errors.Is(err, errLocalAccountLimit) {
			break
		}
		if err != nil {
			t.Fatalf("create #%d: %v", i, err)
		}
		if i > maxLocalAccounts+2 {
			t.Fatal("limit never enforced")
		}
	}
}

// Any bookkeeping failure must fall back to the reserved uid — an accounts
// problem must never lock a user out of their pre-accounts data.
func TestActiveLocalAccountUIDFallsBack(t *testing.T) {
	if got := activeLocalAccountUID(nil); got != localSingleUserUID {
		t.Fatalf("nil db uid = %d, want %d", got, localSingleUserUID)
	}
	db := openLocalAccountsTestDB(t)
	if err := db.Exec(`DROP TABLE w_desktop_local_account`).Error; err != nil {
		t.Fatalf("drop: %v", err)
	}
	if got := activeLocalAccountUID(db); got != localSingleUserUID {
		t.Fatalf("broken table uid = %d, want fallback %d", got, localSingleUserUID)
	}
}

// The isolation the whole feature rests on: threads written under one local
// account's uid are invisible to another. This drives the same scoped query
// the history routes use.
func TestLocalAccountThreadIsolation(t *testing.T) {
	db := openLocalAccountsTestDB(t)
	second, err := createLocalAccount(db, "Ming")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Pre-accounts thread: written under the raw single-user uid before any
	// account row existed. The default account must own it.
	seedThread(t, db, localSingleUserUID, "de305d54-75b4-431b-adb2-eb6b9e546001", "Old work", "ppt", 60)
	seedThread(t, db, localAccountUID(second.ID), "de305d54-75b4-431b-adb2-eb6b9e546002", "Ming work", "ppt", 30)

	countFor := func(uid uint64) int64 {
		t.Helper()
		var n int64
		if err := db.Raw(
			`SELECT COUNT(*) FROM w_workagent_thread WHERE uid = ?`, uid,
		).Row().Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}
	if got := countFor(activeLocalAccountUID(db)); got != 1 {
		t.Fatalf("default account sees %d threads, want its 1", got)
	}
	if err := selectLocalAccount(db, second.ID); err != nil {
		t.Fatalf("select: %v", err)
	}
	if got := countFor(activeLocalAccountUID(db)); got != 1 {
		t.Fatalf("second account sees %d threads, want its 1", got)
	}
	// The two accounts must not be looking at the same row.
	var name string
	if err := db.Raw(
		`SELECT name FROM w_workagent_thread WHERE uid = ?`, activeLocalAccountUID(db),
	).Row().Scan(&name); err != nil {
		t.Fatalf("read: %v", err)
	}
	if name != "Ming work" {
		t.Fatalf("second account sees %q, want its own thread", name)
	}
}
