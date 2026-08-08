package agentturn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	agentv1 "server/contracts/agent/v1"
)

func TestMemoryStoreTransitionIsAtomicWithEventValidation(t *testing.T) {
	service, store := newTestService(t)
	turnID := mustStart(t, service, testStartCommand("principal_1", "thread_1", "idem_1", "sha256:a")).Turn.ID
	_, err := store.Transition(context.Background(), turnID, agentv1.TurnStatusRunning, testTime.Add(1), EventDraft{
		Type: agentv1.EventCoreTurnStatus,
		Data: json.RawMessage(`{`),
	})
	if err == nil {
		t.Fatal("Transition accepted invalid event data")
	}
	turn, err := store.GetOwned(context.Background(), "principal_1", "thread_1", turnID)
	if err != nil {
		t.Fatal(err)
	}
	if turn.Status != agentv1.TurnStatusQueued || turn.StartedAt != nil {
		t.Fatalf("failed atomic transition mutated Turn: %+v", turn)
	}
	if events := mustAttach(t, service, "principal_1", turnID, agentv1.ReplayCursor{}).Events; len(events) != 1 {
		t.Fatalf("failed atomic transition appended event; total = %d", len(events))
	}
}

func TestMemoryStoreRejectsTurnIDCollisionAcrossIdempotencyScopes(t *testing.T) {
	store := NewMemoryStore()
	service, err := NewService(ServiceConfig{
		Store:     store,
		NewTurnID: func() (agentv1.TurnID, error) { return "turn_fixed", nil },
		Now:       func() time.Time { return testTime },
	})
	if err != nil {
		t.Fatal(err)
	}
	mustStart(t, service, testStartCommand("principal_1", "thread_1", "idem_1", "sha256:a"))
	_, err = service.Start(context.Background(), testStartCommand("principal_1", "thread_2", "idem_2", "sha256:b"))
	if !errors.Is(err, ErrTurnIDConflict) {
		t.Fatalf("turn ID collision error = %v, want ErrTurnIDConflict", err)
	}
}

func TestZeroValueMemoryStoreSupportsAdmission(t *testing.T) {
	store := &MemoryStore{}
	service, err := NewService(ServiceConfig{
		Store:     store,
		NewTurnID: func() (agentv1.TurnID, error) { return "turn_zero_store", nil },
		Now:       func() time.Time { return testTime },
	})
	if err != nil {
		t.Fatal(err)
	}
	result := mustStart(t, service, testStartCommand("principal_1", "thread_1", "idem_1", "sha256:a"))
	if result.Turn.ID != "turn_zero_store" {
		t.Fatalf("turn ID = %q", result.Turn.ID)
	}
}

func TestMemoryStoreReportsRetentionGapWithWindow(t *testing.T) {
	service, store := newTestService(t)
	turnID := mustStart(t, service, testStartCommand("principal_1", "thread_1", "idem_1", "sha256:a")).Turn.ID
	for i := 0; i < 4; i++ {
		if _, err := service.AppendDomainEvent(context.Background(), turnID, EventDraft{
			Type: agentv1.EventType(fmt.Sprintf("writer.delta.%d", i)),
			Data: json.RawMessage(`{}`),
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Simulate a future retention implementation pruning sequences 1..3.
	store.mu.Lock()
	store.events[turnID] = append([]agentv1.EventEnvelope(nil), store.events[turnID][3:]...)
	store.mu.Unlock()
	after := agentv1.Sequence(1)
	_, err := service.Attach(context.Background(), AttachCommand{
		PrincipalID: "principal_1",
		ThreadID:    "thread_1",
		Request: agentv1.AttachRequest{
			TurnID: turnID,
			Cursor: agentv1.ReplayCursor{AfterSequence: &after},
		},
	})
	if !errors.Is(err, ErrReplayGap) {
		t.Fatalf("retention gap error = %v, want ErrReplayGap", err)
	}
	var boundary *ReplayBoundaryError
	if !errors.As(err, &boundary) || boundary.Window.OldestSequence != 4 || boundary.Window.LatestSequence != 5 {
		t.Fatalf("retention boundary = %+v, want window 4..5", boundary)
	}
}

func TestMemoryStoreSequenceDoesNotRegressAfterFullPrune(t *testing.T) {
	service, store := newTestService(t)
	turnID := mustStart(t, service, testStartCommand("principal_1", "thread_1", "idem_1", "sha256:a")).Turn.ID
	for index := 0; index < 2; index++ {
		if _, err := service.AppendDomainEvent(context.Background(), turnID, EventDraft{
			Type: agentv1.EventType(fmt.Sprintf("writer.before-prune.%d", index)),
			Data: json.RawMessage(`{}`),
		}); err != nil {
			t.Fatal(err)
		}
	}
	store.mu.Lock()
	store.events[turnID] = nil
	store.mu.Unlock()

	event, err := service.AppendDomainEvent(context.Background(), turnID, EventDraft{
		Type: "writer.after-prune",
		Data: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if event.Sequence != 4 {
		t.Fatalf("sequence after pruning all retained events = %d, want 4", event.Sequence)
	}
}
