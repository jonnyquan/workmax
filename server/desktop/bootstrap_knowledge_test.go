//go:build desktop

package desktop

import (
	"context"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	localinference "server/desktop/local_inference"
	migrationsdesktop "server/desktop/migrations_desktop"
)

// recordingKnowledgeHooks counts pass-through calls so the gate's behavior is
// observable without any cgo knowledge stack.
type recordingKnowledgeHooks struct {
	indexCalls    int
	retrieveCalls int
}

func (r *recordingKnowledgeHooks) IndexTurn(context.Context, string, string, string) error {
	r.indexCalls++
	return nil
}

func (r *recordingKnowledgeHooks) Retrieve(context.Context, uint64, string, int) ([]localinference.RetrievedSource, error) {
	r.retrieveCalls++
	return []localinference.RetrievedSource{{Kind: "file", Label: "a.md", Text: "fact", Score: 0.9}}, nil
}

func openKnowledgeGateTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:knowledge-gate-" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := migrationsdesktop.Apply(db); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	return db
}

// Phase 0.2: the vec0 store is not uid-partitioned yet, so with more than one
// local account retrieval must be skipped entirely (privacy stopgap), while
// index writes keep flowing. Single-account behavior is unchanged.
func TestMultiAccountRetrievalGate(t *testing.T) {
	db := openKnowledgeGateTestDB(t)
	if err := ensureDefaultLocalAccount(db); err != nil {
		t.Fatalf("seed default account: %v", err)
	}
	inner := &recordingKnowledgeHooks{}
	gate := &multiAccountRetrievalGate{inner: inner, db: db}

	// Single account: retrieval passes through untouched.
	got, err := gate.Retrieve(context.Background(), 1, "q", 4)
	if err != nil || len(got) != 1 {
		t.Fatalf("single-account Retrieve = (%v, %v), want one pass-through hit", got, err)
	}
	if inner.retrieveCalls != 1 {
		t.Fatalf("inner retrieve calls = %d, want 1", inner.retrieveCalls)
	}

	// Second account appears: retrieval is disabled, silently and safely.
	if _, err := createLocalAccount(db, "second"); err != nil {
		t.Fatalf("create second account: %v", err)
	}
	got, err = gate.Retrieve(context.Background(), 1, "q", 4)
	if err != nil {
		t.Fatalf("multi-account Retrieve must not error, got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("multi-account Retrieve = %+v, want nothing injected", got)
	}
	if inner.retrieveCalls != 1 {
		t.Errorf("inner retrieve reached %d calls; the gate must not consult the un-partitioned index", inner.retrieveCalls)
	}

	// Index writes keep flowing in both configurations.
	if err := gate.IndexTurn(context.Background(), "turn", "u", "a"); err != nil {
		t.Fatalf("IndexTurn through the gate: %v", err)
	}
	if inner.indexCalls != 1 {
		t.Errorf("inner index calls = %d, want 1 (indexing must not be gated)", inner.indexCalls)
	}
}

// A DB where the account table cannot be counted fails closed: no retrieval,
// no error — guessing single-account wrong would leak across accounts.
func TestMultiAccountRetrievalGateFailsClosed(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:knowledge-gate-closed?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// No migrations: w_desktop_local_account does not exist.
	inner := &recordingKnowledgeHooks{}
	gate := &multiAccountRetrievalGate{inner: inner, db: db}
	got, rerr := gate.Retrieve(context.Background(), 1, "q", 4)
	if rerr != nil || len(got) != 0 || inner.retrieveCalls != 0 {
		t.Fatalf("fail-closed violated: got=%v err=%v innerCalls=%d", got, rerr, inner.retrieveCalls)
	}
}
