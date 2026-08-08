package agentturn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentv1 "server/contracts/agent/v1"
)

var testTime = time.Date(2026, 8, 1, 10, 0, 0, 0, time.FixedZone("CST", 8*60*60))

func TestStartDurablyAdmitsQueuedTurn(t *testing.T) {
	service, _ := newTestService(t)
	result, err := service.Start(context.Background(), testStartCommand("principal_1", "thread_1", "idem_1", "sha256:command-a"))
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if result.IdempotentReplay {
		t.Fatal("first Start unexpectedly reported an idempotent replay")
	}
	if result.Turn.Status != agentv1.TurnStatusQueued {
		t.Fatalf("status = %q, want queued", result.Turn.Status)
	}
	if result.Turn.CreatedAt.Location() != time.UTC {
		t.Fatalf("CreatedAt location = %v, want UTC", result.Turn.CreatedAt.Location())
	}
	if got, want := result.Admission.StreamURL, "/api/v1/agent/threads/thread_1/turns/turn_1/stream"; got != want {
		t.Fatalf("stream URL = %q, want %q", got, want)
	}
	if err := result.Admission.Validate(); err != nil {
		t.Fatalf("admission result is invalid: %v", err)
	}

	attachment := mustAttach(t, service, "principal_1", result.Turn.ID, agentv1.ReplayCursor{})
	if attachment.Window != (agentv1.ReplayWindow{OldestSequence: 1, LatestSequence: 1}) {
		t.Fatalf("window = %+v, want 1..1", attachment.Window)
	}
	if len(attachment.Events) != 1 {
		t.Fatalf("initial events = %d, want 1", len(attachment.Events))
	}
	event := attachment.Events[0]
	if event.Sequence != 1 || event.EventID != string(result.Turn.ID)+":1" || event.Type != agentv1.EventCoreTurnStatus {
		t.Fatalf("unexpected initial event: %+v", event)
	}
	assertJSONField(t, event.Data, "status", string(agentv1.TurnStatusQueued))
}

func TestStartIsScopedIdempotentAndDetectsCommandConflicts(t *testing.T) {
	service, _ := newTestService(t)
	command := testStartCommand("principal_1", "thread_1", "idem_1", "sha256:command-a")
	first := mustStart(t, service, command)

	retryCommand := command
	retryCommand.Plugin = agentv1.EventPluginRef{
		ID:            "workmax.writer",
		Version:       "9.9.9",
		ReleaseDigest: "sha256:new-deployment",
	}
	retry := mustStart(t, service, retryCommand)
	if !retry.IdempotentReplay {
		t.Fatal("retry did not report idempotent replay")
	}
	if retry.Turn.ID != first.Turn.ID {
		t.Fatalf("retry turn = %q, want %q", retry.Turn.ID, first.Turn.ID)
	}
	if retry.Turn.Plugin != first.Turn.Plugin {
		t.Fatalf("retry replaced admitted Plugin snapshot: got %+v want %+v", retry.Turn.Plugin, first.Turn.Plugin)
	}
	if events := mustAttach(t, service, "principal_1", first.Turn.ID, agentv1.ReplayCursor{}).Events; len(events) != 1 {
		t.Fatalf("idempotent retry appended %d events, want exactly 1 total", len(events))
	}

	conflicting := command
	conflicting.CommandDigest = "sha256:different-command"
	if _, err := service.Start(context.Background(), conflicting); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflicting Start error = %v, want ErrIdempotencyConflict", err)
	}

	otherThread := command
	otherThread.Request.ThreadID = "thread_2"
	if got := mustStart(t, service, otherThread); got.Turn.ID == first.Turn.ID {
		t.Fatal("same idempotency key on another thread collapsed unexpectedly")
	}
	otherPrincipal := command
	otherPrincipal.PrincipalID = "principal_2"
	if got := mustStart(t, service, otherPrincipal); got.Turn.ID == first.Turn.ID {
		t.Fatal("same thread/key for another principal collapsed unexpectedly")
	}
}

func TestConcurrentStartCommitsExactlyOneTurn(t *testing.T) {
	service, _ := newTestService(t)
	command := testStartCommand("principal_1", "thread_1", "idem_concurrent", "sha256:command-a")
	const callers = 64
	start := make(chan struct{})
	results := make(chan StartResult, callers)
	errorsCh := make(chan error, callers)
	var wait sync.WaitGroup
	for i := 0; i < callers; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			result, err := service.Start(context.Background(), command)
			if err != nil {
				errorsCh <- err
				return
			}
			results <- result
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(errorsCh)
	for err := range errorsCh {
		t.Errorf("concurrent Start error = %v", err)
	}
	var turnID agentv1.TurnID
	created := 0
	count := 0
	for result := range results {
		count++
		if turnID == "" {
			turnID = result.Turn.ID
		}
		if result.Turn.ID != turnID {
			t.Errorf("concurrent Start returned turn %q, want %q", result.Turn.ID, turnID)
		}
		if !result.IdempotentReplay {
			created++
		}
	}
	if count != callers {
		t.Fatalf("successful results = %d, want %d", count, callers)
	}
	if created != 1 {
		t.Fatalf("non-replay admissions = %d, want exactly 1", created)
	}
	if events := mustAttach(t, service, "principal_1", turnID, agentv1.ReplayCursor{}).Events; len(events) != 1 {
		t.Fatalf("concurrent admission produced %d initial events, want 1", len(events))
	}
}

func TestTransitionAndAppendMaintainMonotonicEventSequence(t *testing.T) {
	service, _ := newTestService(t)
	turnID := mustStart(t, service, testStartCommand("principal_1", "thread_1", "idem_1", "sha256:a")).Turn.ID

	if _, err := service.Transition(context.Background(), turnID, agentv1.TurnStatusCompleted); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("queued -> completed error = %v, want ErrInvalidTransition", err)
	}
	running, err := service.Transition(context.Background(), turnID, agentv1.TurnStatusRunning)
	if err != nil {
		t.Fatalf("queued -> running error = %v", err)
	}
	if !running.Changed || running.Event == nil || running.Event.Sequence != 2 {
		t.Fatalf("running transition = %+v, want changed event sequence 2", running)
	}
	if running.Turn.StartedAt == nil {
		t.Fatal("running transition did not set StartedAt")
	}
	retry, err := service.Transition(context.Background(), turnID, agentv1.TurnStatusRunning)
	if err != nil {
		t.Fatalf("idempotent running transition error = %v", err)
	}
	if retry.Changed || retry.Event != nil {
		t.Fatalf("same-state transition appended an event: %+v", retry)
	}

	domain, err := service.AppendDomainEvent(context.Background(), turnID, EventDraft{
		Type:         "writer.document.proposed",
		ResourceRefs: []string{"wm:workmax.writer:document:doc_1@7"},
		Data:         json.RawMessage(`{"title":"Draft"}`),
	})
	if err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if domain.Sequence != 3 || domain.Plugin != running.Turn.Plugin {
		t.Fatalf("domain event = %+v, want sequence 3 and admitted Plugin", domain)
	}

	completed, err := service.Transition(context.Background(), turnID, agentv1.TurnStatusCompleted)
	if err != nil {
		t.Fatalf("running -> completed error = %v", err)
	}
	if completed.Event == nil || completed.Event.Sequence != 4 || completed.Turn.FinishedAt == nil {
		t.Fatalf("completed transition = %+v, want terminal event sequence 4", completed)
	}
	if _, err := service.AppendDomainEvent(context.Background(), turnID, EventDraft{Type: "writer.after-terminal", Data: json.RawMessage(`{}`)}); !errors.Is(err, ErrTurnTerminal) {
		t.Fatalf("append after terminal error = %v, want ErrTurnTerminal", err)
	}
	if _, err := service.Transition(context.Background(), turnID, agentv1.TurnStatusFailed); !errors.Is(err, ErrTurnTerminal) {
		t.Fatalf("completed -> failed error = %v, want ErrTurnTerminal", err)
	}

	events := mustAttach(t, service, "principal_1", turnID, agentv1.ReplayCursor{}).Events
	if len(events) != 4 {
		t.Fatalf("events = %d, want 4", len(events))
	}
	if err := agentv1.ValidateEventSequence(events); err != nil {
		t.Fatalf("event sequence is invalid: %v", err)
	}
	for i, event := range events {
		want := agentv1.Sequence(i + 1)
		if event.Sequence != want {
			t.Errorf("event %d sequence = %d, want %d", i, event.Sequence, want)
		}
	}
}

func TestConcurrentDomainEventsReceiveUniqueMonotonicSequences(t *testing.T) {
	service, _ := newTestService(t)
	turnID := mustStart(t, service, testStartCommand("principal_1", "thread_1", "idem_1", "sha256:a")).Turn.ID
	if _, err := service.Transition(context.Background(), turnID, agentv1.TurnStatusRunning); err != nil {
		t.Fatal(err)
	}
	const writers = 96
	start := make(chan struct{})
	sequences := make(chan agentv1.Sequence, writers)
	errorsCh := make(chan error, writers)
	var wait sync.WaitGroup
	for i := 0; i < writers; i++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			event, err := service.AppendDomainEvent(context.Background(), turnID, EventDraft{
				Type: agentv1.EventType(fmt.Sprintf("writer.delta.%d", index)),
				Data: json.RawMessage(fmt.Sprintf(`{"index":%d}`, index)),
			})
			if err != nil {
				errorsCh <- err
				return
			}
			sequences <- event.Sequence
		}(i)
	}
	close(start)
	wait.Wait()
	close(sequences)
	close(errorsCh)
	for err := range errorsCh {
		t.Errorf("AppendDomainEvent error = %v", err)
	}
	seen := make(map[agentv1.Sequence]struct{}, writers)
	for sequence := range sequences {
		if _, duplicate := seen[sequence]; duplicate {
			t.Errorf("duplicate sequence %d", sequence)
		}
		seen[sequence] = struct{}{}
	}
	if len(seen) != writers {
		t.Fatalf("unique event sequences = %d, want %d", len(seen), writers)
	}
	events := mustAttach(t, service, "principal_1", turnID, agentv1.ReplayCursor{}).Events
	if len(events) != writers+2 {
		t.Fatalf("events = %d, want initial+running+%d", len(events), writers)
	}
	if err := agentv1.ValidateEventSequence(events); err != nil {
		t.Fatalf("concurrent replay sequence invalid: %v", err)
	}
}

func TestDomainAppendRejectsReservedCoreNamespace(t *testing.T) {
	service, _ := newTestService(t)
	turnID := mustStart(t, service, testStartCommand("principal_1", "thread_1", "idem_1", "sha256:a")).Turn.ID
	_, err := service.AppendDomainEvent(context.Background(), turnID, EventDraft{
		Type: agentv1.EventCoreToolStarted,
		Data: json.RawMessage(`{}`),
	})
	if !errors.Is(err, ErrReservedEventType) {
		t.Fatalf("reserved event error = %v, want ErrReservedEventType", err)
	}
	if events := mustAttach(t, service, "principal_1", turnID, agentv1.ReplayCursor{}).Events; len(events) != 1 {
		t.Fatalf("reserved event mutated store; event total = %d", len(events))
	}
}

func TestCancelIntentIsSeparateFromStoppedTerminalState(t *testing.T) {
	service, _ := newTestService(t)
	turnID := mustStart(t, service, testStartCommand("principal_1", "thread_1", "idem_1", "sha256:a")).Turn.ID
	if _, err := service.Transition(context.Background(), turnID, agentv1.TurnStatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Transition(context.Background(), turnID, agentv1.TurnStatusStopped); !errors.Is(err, ErrCancellationNotRequested) {
		t.Fatalf("stop without cancellation error = %v, want ErrCancellationNotRequested", err)
	}

	if _, err := service.Cancel(context.Background(), CancelCommand{
		PrincipalID: "principal_2",
		ThreadID:    "thread_1",
		Request:     agentv1.CancelRequest{TurnID: turnID},
	}); !errors.Is(err, ErrTurnNotFound) {
		t.Fatalf("cross-owner cancel error = %v, want ErrTurnNotFound", err)
	}
	if _, err := service.Cancel(context.Background(), CancelCommand{
		PrincipalID: "principal_1",
		ThreadID:    "thread_other",
		Request:     agentv1.CancelRequest{TurnID: turnID},
	}); !errors.Is(err, ErrTurnNotFound) {
		t.Fatalf("cross-thread cancel error = %v, want ErrTurnNotFound", err)
	}
	cancelled, err := service.Cancel(context.Background(), CancelCommand{
		PrincipalID: "principal_1",
		ThreadID:    "thread_1",
		Request:     agentv1.CancelRequest{TurnID: turnID},
	})
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if !cancelled.NewlyRequested || cancelled.Event == nil {
		t.Fatalf("cancel result = %+v, want newly requested event", cancelled)
	}
	if cancelled.Turn.Status != agentv1.TurnStatusRunning || cancelled.Turn.CancelRequestedAt == nil {
		t.Fatalf("Cancel changed terminal state instead of intent only: %+v", cancelled.Turn)
	}
	assertJSONField(t, cancelled.Event.Data, "cancellationRequested", true)

	retry, err := service.Cancel(context.Background(), CancelCommand{
		PrincipalID: "principal_1",
		ThreadID:    "thread_1",
		Request:     agentv1.CancelRequest{TurnID: turnID},
	})
	if err != nil {
		t.Fatalf("repeated Cancel() error = %v", err)
	}
	if retry.NewlyRequested || retry.Event != nil {
		t.Fatalf("repeated Cancel appended another intent: %+v", retry)
	}

	stopped, err := service.Transition(context.Background(), turnID, agentv1.TurnStatusStopped)
	if err != nil {
		t.Fatalf("acknowledge stopped error = %v", err)
	}
	if stopped.Turn.Status != agentv1.TurnStatusStopped || stopped.Event == nil {
		t.Fatalf("stopped result = %+v", stopped)
	}
	terminal := agentv1.TerminalState{TurnID: turnID, Status: stopped.Turn.Status}
	if err := terminal.Validate(); err != nil {
		t.Fatalf("stopped terminal state is invalid: %v", err)
	}
	if agentv1.TurnStatus("cancelled").Valid() {
		t.Fatal("cancelled alias unexpectedly became a durable status")
	}

	afterTerminal, err := service.Cancel(context.Background(), CancelCommand{
		PrincipalID: "principal_1",
		ThreadID:    "thread_1",
		Request:     agentv1.CancelRequest{TurnID: turnID},
	})
	if err != nil {
		t.Fatalf("Cancel after terminal error = %v", err)
	}
	if afterTerminal.NewlyRequested || afterTerminal.Event != nil || afterTerminal.Turn.Status != agentv1.TurnStatusStopped {
		t.Fatalf("Cancel after terminal was not an idempotent reconciliation: %+v", afterTerminal)
	}

	events := mustAttach(t, service, "principal_1", turnID, agentv1.ReplayCursor{}).Events
	if len(events) != 4 {
		t.Fatalf("events = %d, want queued/running/cancel-intent/stopped", len(events))
	}
	if events[2].Sequence != 3 || events[3].Sequence != 4 {
		t.Fatalf("cancel sequence = %+v, want intent 3 then stopped 4", events)
	}
}

func TestAttachResolvesReplayCursorAndEnforcesOwnership(t *testing.T) {
	service, _ := newTestService(t)
	turnID := mustStart(t, service, testStartCommand("principal_1", "thread_1", "idem_1", "sha256:a")).Turn.ID
	if _, err := service.Transition(context.Background(), turnID, agentv1.TurnStatusRunning); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := service.AppendDomainEvent(context.Background(), turnID, EventDraft{
			Type: agentv1.EventType(fmt.Sprintf("writer.delta.%d", i+1)),
			Data: json.RawMessage(fmt.Sprintf(`{"index":%d}`, i+1)),
		}); err != nil {
			t.Fatal(err)
		}
	}
	all := mustAttach(t, service, "principal_1", turnID, agentv1.ReplayCursor{})
	if len(all.Events) != 5 || all.Window != (agentv1.ReplayWindow{OldestSequence: 1, LatestSequence: 5}) {
		t.Fatalf("initial attach = %+v, want 5 events window 1..5", all)
	}

	afterTwo := agentv1.Sequence(2)
	bySequence := mustAttach(t, service, "principal_1", turnID, agentv1.ReplayCursor{AfterSequence: &afterTwo})
	assertSequences(t, bySequence.Events, 3, 4, 5)
	byEventID := mustAttach(t, service, "principal_1", turnID, agentv1.ReplayCursor{LastEventID: all.Events[2].EventID})
	assertSequences(t, byEventID.Events, 4, 5)

	afterFour := agentv1.Sequence(4)
	both := mustAttach(t, service, "principal_1", turnID, agentv1.ReplayCursor{
		LastEventID:   all.Events[1].EventID,
		AfterSequence: &afterFour,
	})
	if both.AfterSequence != 4 {
		t.Fatalf("resolved cursor = %d, want furthest sequence 4", both.AfterSequence)
	}
	assertSequences(t, both.Events, 5)

	afterFuture := agentv1.Sequence(100)
	_, err := service.Attach(context.Background(), AttachCommand{
		PrincipalID: "principal_1",
		ThreadID:    "thread_1",
		Request: agentv1.AttachRequest{
			TurnID: turnID,
			Cursor: agentv1.ReplayCursor{AfterSequence: &afterFuture},
		},
	})
	if !errors.Is(err, ErrReplayCursorAhead) {
		t.Fatalf("future cursor error = %v, want ErrReplayCursorAhead", err)
	}
	var boundary *ReplayBoundaryError
	if !errors.As(err, &boundary) || boundary.Window.LatestSequence != 5 || boundary.Cursor != 100 {
		t.Fatalf("future cursor boundary = %+v, want cursor 100/window latest 5", boundary)
	}

	if _, err := service.Attach(context.Background(), AttachCommand{
		PrincipalID: "principal_2",
		ThreadID:    "thread_1",
		Request:     agentv1.AttachRequest{TurnID: turnID},
	}); !errors.Is(err, ErrTurnNotFound) {
		t.Fatalf("cross-owner Attach error = %v, want ErrTurnNotFound", err)
	}
	if _, err := service.Attach(context.Background(), AttachCommand{
		PrincipalID: "principal_1",
		ThreadID:    "thread_other",
		Request:     agentv1.AttachRequest{TurnID: turnID},
	}); !errors.Is(err, ErrTurnNotFound) {
		t.Fatalf("cross-thread Attach error = %v, want ErrTurnNotFound", err)
	}
	if _, err := service.Attach(context.Background(), AttachCommand{
		PrincipalID: "principal_1",
		ThreadID:    "thread_1",
		Request: agentv1.AttachRequest{
			TurnID: turnID,
			Cursor: agentv1.ReplayCursor{LastEventID: "turn_other:1"},
		},
	}); !errors.Is(err, ErrReplayCursorNotFound) {
		t.Fatalf("foreign event cursor error = %v, want ErrReplayCursorNotFound", err)
	}
}

func TestAttachUsesBoundedReplayPages(t *testing.T) {
	store := NewMemoryStore()
	var ids atomic.Uint64
	service, err := NewService(ServiceConfig{
		Store:           store,
		NewTurnID:       func() (agentv1.TurnID, error) { return agentv1.TurnID(fmt.Sprintf("turn_%d", ids.Add(1))), nil },
		Now:             func() time.Time { return testTime },
		ReplayPageLimit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	turnID := mustStart(t, service, testStartCommand("principal_1", "thread_1", "idem_1", "sha256:a")).Turn.ID
	for i := 0; i < 4; i++ {
		if _, err := service.AppendDomainEvent(context.Background(), turnID, EventDraft{
			Type: agentv1.EventType(fmt.Sprintf("writer.delta.%d", i)),
			Data: json.RawMessage(`{}`),
		}); err != nil {
			t.Fatal(err)
		}
	}
	first := mustAttach(t, service, "principal_1", turnID, agentv1.ReplayCursor{})
	assertSequences(t, first.Events, 1, 2)
	if !first.HasMore {
		t.Fatal("first bounded replay page did not report HasMore")
	}
	after := first.Events[len(first.Events)-1].Sequence
	second := mustAttach(t, service, "principal_1", turnID, agentv1.ReplayCursor{AfterSequence: &after})
	assertSequences(t, second.Events, 3, 4)
	if !second.HasMore {
		t.Fatal("second bounded replay page did not report HasMore")
	}
	after = second.Events[len(second.Events)-1].Sequence
	third := mustAttach(t, service, "principal_1", turnID, agentv1.ReplayCursor{AfterSequence: &after})
	assertSequences(t, third.Events, 5)
	if third.HasMore {
		t.Fatal("last bounded replay page reported HasMore")
	}
}

func TestAttachReturnsDefensiveCopiesAndDoesNotMutateLifecycle(t *testing.T) {
	service, _ := newTestService(t)
	turnID := mustStart(t, service, testStartCommand("principal_1", "thread_1", "idem_1", "sha256:a")).Turn.ID
	if _, err := service.AppendDomainEvent(context.Background(), turnID, EventDraft{
		Type:         "writer.document.proposed",
		ResourceRefs: []string{"wm:document:1"},
		Data:         json.RawMessage(`{"safe":true}`),
	}); err != nil {
		t.Fatal(err)
	}
	first := mustAttach(t, service, "principal_1", turnID, agentv1.ReplayCursor{})
	first.Events[1].Data[2] = 'X'
	first.Events[1].ResourceRefs[0] = "mutated"
	second := mustAttach(t, service, "principal_1", turnID, agentv1.ReplayCursor{})
	if !json.Valid(second.Events[1].Data) || second.Events[1].ResourceRefs[0] != "wm:document:1" {
		t.Fatalf("caller mutation escaped into store: %+v", second.Events[1])
	}
	if second.Turn.Status != agentv1.TurnStatusQueued || second.Turn.CancelRequestedAt != nil {
		t.Fatalf("observer attach mutated lifecycle: %+v", second.Turn)
	}
}

func TestStatusAndCancelFailClosedOnOwnership(t *testing.T) {
	service, _ := newTestService(t)
	turnID := mustStart(t, service, testStartCommand("principal_1", "thread_1", "idem_1", "sha256:a")).Turn.ID
	if _, err := service.Status(context.Background(), OwnedTurnRequest{PrincipalID: "principal_2", ThreadID: "thread_1", TurnID: turnID}); !errors.Is(err, ErrTurnNotFound) {
		t.Fatalf("cross-owner Status error = %v, want ErrTurnNotFound", err)
	}
	if _, err := service.Status(context.Background(), OwnedTurnRequest{PrincipalID: "principal_1", ThreadID: "thread_other", TurnID: turnID}); !errors.Is(err, ErrTurnNotFound) {
		t.Fatalf("cross-thread Status error = %v, want ErrTurnNotFound", err)
	}
	turn, err := service.Status(context.Background(), OwnedTurnRequest{PrincipalID: "principal_1", ThreadID: "thread_1", TurnID: turnID})
	if err != nil || turn.ID != turnID {
		t.Fatalf("owned Status = %+v, %v", turn, err)
	}
}

func TestStartRejectsUnsafePathIdentifiers(t *testing.T) {
	service, _ := newTestService(t)
	for _, threadID := range []agentv1.ThreadID{"thread/other", `thread\\other`, "thread?query", "thread#fragment", "thread%2Fother"} {
		command := testStartCommand("principal_1", threadID, "idem_1", "sha256:a")
		if _, err := service.Start(context.Background(), command); err == nil {
			t.Errorf("Start accepted unsafe threadId %q", threadID)
		}
	}
}

func TestNewServiceRejectsUnsafeReplayLimit(t *testing.T) {
	for _, limit := range []int{-1, 1001} {
		if _, err := NewService(ServiceConfig{Store: NewMemoryStore(), ReplayPageLimit: limit}); err == nil {
			t.Errorf("NewService accepted replay limit %d", limit)
		}
	}
	if _, err := NewService(ServiceConfig{}); err == nil {
		t.Fatal("NewService accepted nil Store")
	}
}

func TestCancelledContextMakesNoMutation(t *testing.T) {
	service, _ := newTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Start(ctx, testStartCommand("principal_1", "thread_1", "idem_1", "sha256:a")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start canceled context error = %v, want context.Canceled", err)
	}
	result := mustStart(t, service, testStartCommand("principal_1", "thread_1", "idem_1", "sha256:a"))
	if _, err := service.Attach(ctx, AttachCommand{
		PrincipalID: "principal_1",
		ThreadID:    "thread_1",
		Request:     agentv1.AttachRequest{TurnID: result.Turn.ID},
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Attach canceled context error = %v, want context.Canceled", err)
	}
}

func TestCanTransitionMatrix(t *testing.T) {
	allowed := map[string]bool{
		"queued->queued":       true,
		"queued->running":      true,
		"queued->stopped":      true,
		"queued->failed":       true,
		"queued->timeout":      true,
		"running->running":     true,
		"running->completed":   true,
		"running->stopped":     true,
		"running->failed":      true,
		"running->timeout":     true,
		"completed->completed": true,
		"stopped->stopped":     true,
		"failed->failed":       true,
		"timeout->timeout":     true,
	}
	statuses := []agentv1.TurnStatus{
		agentv1.TurnStatusQueued,
		agentv1.TurnStatusRunning,
		agentv1.TurnStatusCompleted,
		agentv1.TurnStatusStopped,
		agentv1.TurnStatusFailed,
		agentv1.TurnStatusTimeout,
	}
	for _, from := range statuses {
		for _, to := range statuses {
			key := string(from) + "->" + string(to)
			if got := CanTransition(from, to); got != allowed[key] {
				t.Errorf("CanTransition(%s) = %v, want %v", key, got, allowed[key])
			}
		}
	}
	if CanTransition("cancelled", agentv1.TurnStatusStopped) || CanTransition(agentv1.TurnStatusRunning, "cancelled") {
		t.Fatal("unknown status entered the state machine")
	}
}

func newTestService(t *testing.T) (*Service, *MemoryStore) {
	t.Helper()
	store := NewMemoryStore()
	var ids atomic.Uint64
	service, err := NewService(ServiceConfig{
		Store: store,
		NewTurnID: func() (agentv1.TurnID, error) {
			return agentv1.TurnID(fmt.Sprintf("turn_%d", ids.Add(1))), nil
		},
		Now: func() time.Time { return testTime },
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service, store
}

func testStartCommand(principal PrincipalID, thread agentv1.ThreadID, key agentv1.IdempotencyKey, digest string) StartCommand {
	return StartCommand{
		PrincipalID: principal,
		Request: agentv1.StartRequest{
			ThreadID:       thread,
			IdempotencyKey: key,
		},
		CommandDigest: digest,
		Plugin: agentv1.EventPluginRef{
			ID:            "workmax.writer",
			Version:       "1.2.0",
			ReleaseDigest: "sha256:plugin-a",
		},
	}
}

func mustStart(t *testing.T, service *Service, command StartCommand) StartResult {
	t.Helper()
	result, err := service.Start(context.Background(), command)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	return result
}

func mustAttach(t *testing.T, service *Service, principal PrincipalID, turnID agentv1.TurnID, cursor agentv1.ReplayCursor) Attachment {
	t.Helper()
	attachment, err := service.Attach(context.Background(), AttachCommand{
		PrincipalID: principal,
		ThreadID:    "thread_1",
		Request: agentv1.AttachRequest{
			TurnID: turnID,
			Cursor: cursor,
		},
	})
	if err != nil {
		t.Fatalf("Attach() error = %v", err)
	}
	return attachment
}

func assertSequences(t *testing.T, events []agentv1.EventEnvelope, want ...agentv1.Sequence) {
	t.Helper()
	if len(events) != len(want) {
		t.Fatalf("event count = %d, want %d", len(events), len(want))
	}
	for i, sequence := range want {
		if events[i].Sequence != sequence {
			t.Errorf("event %d sequence = %d, want %d", i, events[i].Sequence, sequence)
		}
	}
}

func assertJSONField(t *testing.T, data json.RawMessage, field string, want any) {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("unmarshal event data: %v", err)
	}
	if got := payload[field]; got != want {
		t.Fatalf("event field %q = %#v, want %#v", field, got, want)
	}
}
