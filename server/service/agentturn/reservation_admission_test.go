package agentturn

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	agentv1 "server/contracts/agent/v1"
	"server/utils/testutil"
)

const reservationAdmissionTestBindingTable = "test_agent_turn_reservation_binding"

type reservationAdmissionTestAuthority struct {
	digest       string
	reserveErr   error
	reserveCalls atomic.Int64
	verifyCalls  atomic.Int64
}

type reservationAdmissionCapableSpyStore struct {
	*MemoryStore
	commercialCalls atomic.Int64
}

func (store *reservationAdmissionCapableSpyStore) AdmitWithReservationAuthority(
	ctx context.Context,
	candidate Turn,
	initial EventDraft,
	_ TurnReservationAdmissionAuthority,
) (AdmissionRecord, error) {
	store.commercialCalls.Add(1)
	return store.MemoryStore.Admit(ctx, candidate, initial)
}

func (authority *reservationAdmissionTestAuthority) ReserveAndBindTurn(tx *gorm.DB, turn Turn) error {
	authority.reserveCalls.Add(1)
	if tx == nil {
		return errors.New("missing admission transaction")
	}
	var turns, events int64
	if err := tx.Table(SQLTurnTable).Where("turn_id = ?", string(turn.ID)).Count(&turns).Error; err != nil {
		return err
	}
	if err := tx.Table(SQLTurnEventTable).Where("turn_id = ?", string(turn.ID)).Count(&events).Error; err != nil {
		return err
	}
	if turns != 1 || events != 0 {
		return fmt.Errorf("authority observed turns=%d events=%d", turns, events)
	}
	if err := tx.Table(reservationAdmissionTestBindingTable).Create(map[string]any{
		"turn_id":        string(turn.ID),
		"binding_digest": authority.digest,
	}).Error; err != nil {
		return err
	}
	return authority.reserveErr
}

func (authority *reservationAdmissionTestAuthority) VerifyTurnBinding(tx *gorm.DB, turn Turn) error {
	authority.verifyCalls.Add(1)
	if tx == nil {
		return errors.New("missing verification transaction")
	}
	var count int64
	if err := tx.Table(reservationAdmissionTestBindingTable).
		Where("turn_id = ? AND binding_digest = ?", string(turn.ID), authority.digest).
		Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return errors.New("exact reservation binding was not found")
	}
	return nil
}

func newReservationAdmissionTestStore(t *testing.T) (*gorm.DB, *SQLStore) {
	t.Helper()
	db := testutil.NewTestDB(t)
	if err := db.Exec(`CREATE TABLE test_agent_turn_reservation_binding (
		turn_id TEXT PRIMARY KEY,
		binding_digest TEXT NOT NULL
	)`).Error; err != nil {
		t.Fatalf("create reservation admission test table: %v", err)
	}
	return db, mustSQLStore(t, db)
}

func reservationAdmissionTestCount(t *testing.T, db *gorm.DB, table, predicate string, args ...any) int64 {
	t.Helper()
	query := db.Table(table)
	if predicate != "" {
		query = query.Where(predicate, args...)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

func TestAdmitWithReservationAuthorityCommitsTurnBindingAndEventAtomically(t *testing.T) {
	db, store := newReservationAdmissionTestStore(t)
	turn := sqlStoreTestTurn("reservation_atomic")
	authority := &reservationAdmissionTestAuthority{digest: "pricing:a"}

	result, err := store.AdmitWithReservationAuthority(
		context.Background(), turn,
		sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"status":"queued"}`),
		authority,
	)
	if err != nil || !result.Created || result.Turn.ID != turn.ID {
		t.Fatalf("AdmitWithReservationAuthority() = %+v, %v", result, err)
	}
	if authority.reserveCalls.Load() != 1 || authority.verifyCalls.Load() != 0 {
		t.Fatalf("authority calls reserve=%d verify=%d, want 1/0",
			authority.reserveCalls.Load(), authority.verifyCalls.Load())
	}
	for table, want := range map[string]int64{
		SQLTurnTable:                         1,
		SQLTurnEventTable:                    1,
		reservationAdmissionTestBindingTable: 1,
	} {
		if got := reservationAdmissionTestCount(t, db, table, "turn_id = ?", string(turn.ID)); got != want {
			t.Fatalf("%s rows = %d, want %d", table, got, want)
		}
	}
}

func TestAdmitWithReservationAuthorityFailureRollsBackEverything(t *testing.T) {
	db, store := newReservationAdmissionTestStore(t)
	turn := sqlStoreTestTurn("reservation_rollback")
	authority := &reservationAdmissionTestAuthority{
		digest:     "pricing:a",
		reserveErr: errors.New("private-ledger-detail"),
	}

	_, err := store.AdmitWithReservationAuthority(
		context.Background(), turn,
		sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"status":"queued"}`),
		authority,
	)
	if !errors.Is(err, ErrTurnReservationAdmissionFailed) || strings.Contains(err.Error(), "private-ledger-detail") {
		t.Fatalf("admission error = %v, want sanitized ErrTurnReservationAdmissionFailed", err)
	}
	for _, table := range []string{SQLTurnTable, SQLTurnEventTable, reservationAdmissionTestBindingTable} {
		if got := reservationAdmissionTestCount(t, db, table, "turn_id = ?", string(turn.ID)); got != 0 {
			t.Fatalf("failed admission persisted %d rows in %s", got, table)
		}
	}
}

func TestAdmitWithReservationAuthorityUnknownOutcomeRetryVerifiesLockedWinner(t *testing.T) {
	db, store := newReservationAdmissionTestStore(t)
	turn := sqlStoreTestTurn("reservation_replay")
	authority := &reservationAdmissionTestAuthority{digest: "pricing:a"}
	initial := sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"status":"queued"}`)

	first, err := store.AdmitWithReservationAuthority(context.Background(), turn, initial, authority)
	if err != nil || !first.Created {
		t.Fatalf("first admission = %+v, %v", first, err)
	}
	// Model a caller that did not observe the first COMMIT and retries with a
	// newly minted candidate ID but the same admission scope and request facts.
	retry := turn
	retry.ID = "turn_reservation_replay_retry"
	replayed, err := store.AdmitWithReservationAuthority(context.Background(), retry, initial, authority)
	if err != nil || replayed.Created || replayed.Turn.ID != turn.ID {
		t.Fatalf("unknown-outcome retry = %+v, %v", replayed, err)
	}
	if authority.reserveCalls.Load() != 1 || authority.verifyCalls.Load() != 1 {
		t.Fatalf("authority calls reserve=%d verify=%d, want 1/1",
			authority.reserveCalls.Load(), authority.verifyCalls.Load())
	}
	if got := reservationAdmissionTestCount(t, db, SQLTurnTable, ""); got != 1 {
		t.Fatalf("Turn rows after replay = %d, want 1", got)
	}
	if got := reservationAdmissionTestCount(t, db, SQLTurnEventTable, ""); got != 1 {
		t.Fatalf("Event rows after replay = %d, want 1", got)
	}

	conflicting := &reservationAdmissionTestAuthority{digest: "pricing:other"}
	if _, err := store.AdmitWithReservationAuthority(context.Background(), retry, initial, conflicting); !errors.Is(err, ErrTurnReservationBindingInvalid) {
		t.Fatalf("conflicting binding replay error = %v, want ErrTurnReservationBindingInvalid", err)
	}
	if conflicting.reserveCalls.Load() != 0 || conflicting.verifyCalls.Load() != 1 {
		t.Fatalf("conflicting authority calls reserve=%d verify=%d, want 0/1",
			conflicting.reserveCalls.Load(), conflicting.verifyCalls.Load())
	}

	if err := db.Table(reservationAdmissionTestBindingTable).Where("turn_id = ?", string(turn.ID)).Delete(nil).Error; err != nil {
		t.Fatal(err)
	}
	missing := &reservationAdmissionTestAuthority{digest: "pricing:a"}
	if _, err := store.AdmitWithReservationAuthority(context.Background(), retry, initial, missing); !errors.Is(err, ErrTurnReservationBindingInvalid) {
		t.Fatalf("missing binding replay error = %v, want ErrTurnReservationBindingInvalid", err)
	}
	if missing.reserveCalls.Load() != 0 || missing.verifyCalls.Load() != 1 {
		t.Fatalf("missing authority calls reserve=%d verify=%d, want 0/1",
			missing.reserveCalls.Load(), missing.verifyCalls.Load())
	}
}

func TestReservationAdmissionUnknownCommitResolverVerifiesWinner(t *testing.T) {
	_, store := newReservationAdmissionTestStore(t)
	turn := sqlStoreTestTurn("reservation_unknown_resolve")
	authority := &reservationAdmissionTestAuthority{digest: "pricing:a"}
	initial := sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"status":"queued"}`)
	if _, err := store.AdmitWithReservationAuthority(context.Background(), turn, initial, authority); err != nil {
		t.Fatal(err)
	}

	retry := turn
	retry.ID = "turn_reservation_unknown_resolve_retry"
	resolved, found, err := store.resolveReservationAdmissionWinner(context.Background(), retry, authority)
	if err != nil || !found || resolved.Created || resolved.Turn.ID != turn.ID {
		t.Fatalf("resolveReservationAdmissionWinner() = %+v, found=%v, err=%v", resolved, found, err)
	}
	if authority.reserveCalls.Load() != 1 || authority.verifyCalls.Load() != 1 {
		t.Fatalf("unknown-commit resolver calls reserve=%d verify=%d, want 1/1",
			authority.reserveCalls.Load(), authority.verifyCalls.Load())
	}
}

func TestAdmitWithReservationAuthorityConcurrentCallersVerifyOneWinner(t *testing.T) {
	db, store := newReservationAdmissionTestStore(t)
	base := sqlStoreTestTurn("reservation_concurrent")
	authority := &reservationAdmissionTestAuthority{digest: "pricing:a"}
	initial := sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"status":"queued"}`)
	const callers = 32

	start := make(chan struct{})
	results := make(chan AdmissionRecord, callers)
	errs := make(chan error, callers)
	var wait sync.WaitGroup
	for index := 0; index < callers; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			candidate := base
			candidate.ID = agentv1.TurnID(fmt.Sprintf("turn_reservation_concurrent_%02d", index))
			result, err := store.AdmitWithReservationAuthority(
				context.Background(), candidate, initial, authority,
			)
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}(index)
	}
	close(start)
	wait.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Errorf("concurrent admission error: %v", err)
	}

	created := 0
	var winner agentv1.TurnID
	for result := range results {
		if winner == "" {
			winner = result.Turn.ID
		}
		if result.Turn.ID != winner {
			t.Errorf("concurrent winner = %q, want %q", result.Turn.ID, winner)
		}
		if result.Created {
			created++
		}
	}
	if created != 1 || authority.reserveCalls.Load() != 1 || authority.verifyCalls.Load() != callers-1 {
		t.Fatalf("created=%d reserve=%d verify=%d, want 1/1/%d",
			created, authority.reserveCalls.Load(), authority.verifyCalls.Load(), callers-1)
	}
	for _, table := range []string{SQLTurnTable, SQLTurnEventTable, reservationAdmissionTestBindingTable} {
		if got := reservationAdmissionTestCount(t, db, table, ""); got != 1 {
			t.Fatalf("%s rows = %d, want 1", table, got)
		}
	}
}

func TestOrdinaryAdmissionDoesNotInvokeReservationAuthority(t *testing.T) {
	db, store := newReservationAdmissionTestStore(t)
	turn := sqlStoreTestTurn("reservation_ordinary")
	authority := &reservationAdmissionTestAuthority{digest: "pricing:a"}

	result, err := store.Admit(
		context.Background(), turn,
		sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"status":"queued"}`),
	)
	if err != nil || !result.Created {
		t.Fatalf("ordinary Admit() = %+v, %v", result, err)
	}
	if authority.reserveCalls.Load() != 0 || authority.verifyCalls.Load() != 0 {
		t.Fatalf("ordinary Admit invoked authority: reserve=%d verify=%d",
			authority.reserveCalls.Load(), authority.verifyCalls.Load())
	}
	if got := reservationAdmissionTestCount(t, db, reservationAdmissionTestBindingTable, ""); got != 0 {
		t.Fatalf("ordinary Admit persisted %d reservation bindings", got)
	}
}

func TestOrdinaryStartDoesNotDiscoverCommercialStoreCapability(t *testing.T) {
	store := &reservationAdmissionCapableSpyStore{MemoryStore: NewMemoryStore()}
	var ids atomic.Int64
	service, err := NewService(ServiceConfig{
		Store: store,
		NewTurnID: func() (agentv1.TurnID, error) {
			return agentv1.TurnID(fmt.Sprintf("turn_ordinary_capable_%d", ids.Add(1))), nil
		},
		Now: func() time.Time { return testTime },
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Start(
		context.Background(),
		testStartCommand("principal_capable", "thread_capable", "idem_capable", "sha256:capable"),
	)
	if err != nil || result.IdempotentReplay {
		t.Fatalf("ordinary Start() = %+v, %v", result, err)
	}
	if store.commercialCalls.Load() != 0 {
		t.Fatalf("ordinary Start invoked commercial capability %d times", store.commercialCalls.Load())
	}
}

func TestServiceStartWithReservationAuthorityUsesOnlyCapableStore(t *testing.T) {
	_, store := newReservationAdmissionTestStore(t)
	var ids atomic.Int64
	service, err := NewService(ServiceConfig{
		Store: store,
		NewTurnID: func() (agentv1.TurnID, error) {
			return agentv1.TurnID(fmt.Sprintf("turn_service_reservation_%d", ids.Add(1))), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	authority := &reservationAdmissionTestAuthority{digest: "pricing:a"}
	command := testStartCommand("principal_service", "thread_service", "idem_service", "sha256:service")
	started, err := service.StartWithReservationAuthority(context.Background(), command, authority)
	if err != nil || started.IdempotentReplay || authority.reserveCalls.Load() != 1 {
		t.Fatalf("StartWithReservationAuthority() = %+v, reserve=%d, err=%v",
			started, authority.reserveCalls.Load(), err)
	}

	memoryService, _ := newTestService(t)
	if _, err := memoryService.StartWithReservationAuthority(context.Background(), command, authority); !errors.Is(err, ErrTurnReservationAdmissionAuthorityUnavailable) {
		t.Fatalf("unsupported Store error = %v, want authority unavailable", err)
	}
}
