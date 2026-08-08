package agentturn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	agentv1 "server/contracts/agent/v1"
)

func newTestStream(t *testing.T, reader ReplayReader, broker *TurnEventBroker) *TurnEventStream {
	t.Helper()
	stream, err := NewTurnEventStream(reader, EventStreamOptions{
		PollInterval: 5 * time.Millisecond,
		PageLimit:    8,
		Broker:       broker,
	})
	if err != nil {
		t.Fatal(err)
	}
	return stream
}

func attachFrom(turn Turn, after agentv1.Sequence) AttachCommand {
	cursor := agentv1.ReplayCursor{}
	if after > 0 {
		value := after
		cursor.AfterSequence = &value
	} else {
		zero := agentv1.Sequence(0)
		cursor.AfterSequence = &zero
	}
	return AttachCommand{
		PrincipalID: turn.PrincipalID,
		ThreadID:    turn.ThreadID,
		Request:     agentv1.AttachRequest{TurnID: turn.ID, Cursor: cursor},
	}
}

// drainStream reads until io.EOF or the guard fires, returning the sequence
// order it observed.
func drainStream(t *testing.T, ctx context.Context, subscription *TurnSubscription) []agentv1.Sequence {
	t.Helper()
	var sequences []agentv1.Sequence
	for {
		event, err := subscription.Next(ctx)
		if errors.Is(err, io.EOF) {
			return sequences
		}
		if err != nil {
			t.Fatalf("Next(): %v", err)
		}
		sequences = append(sequences, event.Sequence)
	}
}

func assertContiguous(t *testing.T, sequences []agentv1.Sequence, from agentv1.Sequence) {
	t.Helper()
	want := from + 1
	for _, got := range sequences {
		if got != want {
			t.Fatalf("observed sequence %d, want %d (full order %v)", got, want, sequences)
		}
		want++
	}
}

func TestTurnEventStreamHandsOverFromHistoryToLiveWithoutGapOrDuplicate(t *testing.T) {
	_, store, _, turns := newSQLClaimNextFixture(t, "stream_seam")
	turn := turns[0]
	broker := NewTurnEventBroker()
	stream := newTestStream(t, store, broker)

	claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_stream_seam"))
	if err != nil {
		t.Fatal(err)
	}
	// Admission + running are already durable, so the observer starts mid-Turn
	// and must cross the history/live seam.
	subscription, err := stream.Subscribe(context.Background(), attachFrom(turn, 0))
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()

	const liveEvents = 12
	writerDone := make(chan error, 1)
	go func() {
		for index := 0; index < liveEvents; index++ {
			if _, err := store.CommitAttempt(context.Background(), CommitAttemptCommand{
				Fence:       claimed.Attempt.Fence(),
				OperationID: fmt.Sprintf("operation_seam_%02d", index),
				Event: &EventDraft{
					Type: "writer.document.delta",
					Data: json.RawMessage(fmt.Sprintf(`{"n":%d}`, index)),
				},
			}); err != nil {
				writerDone <- err
				return
			}
			broker.NotifyTurnEvent(turn.ID)
		}
		_, err := store.CommitAttempt(context.Background(), CommitAttemptCommand{
			Fence: claimed.Attempt.Fence(), OperationID: "operation_seam_terminal",
			TerminalStatus: agentv1.TurnStatusCompleted,
		})
		broker.NotifyTurnEvent(turn.ID)
		writerDone <- err
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sequences := drainStream(t, ctx, subscription)
	if err := <-writerDone; err != nil {
		t.Fatalf("writer: %v", err)
	}

	// admission(1) + running(2) + liveEvents + terminal
	want := 2 + liveEvents + 1
	if len(sequences) != want {
		t.Fatalf("observed %d events, want %d: %v", len(sequences), want, sequences)
	}
	assertContiguous(t, sequences, 0)
	if subscription.Cursor() != agentv1.Sequence(want) {
		t.Fatalf("final cursor = %d, want %d", subscription.Cursor(), want)
	}
}

func TestTurnEventStreamStreamsTheTailOfAnAlreadyTerminalTurn(t *testing.T) {
	_, store, _, turns := newSQLClaimNextFixture(t, "stream_terminal")
	turn := turns[0]
	claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_stream_terminal"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitAttempt(context.Background(), CommitAttemptCommand{
		Fence: claimed.Attempt.Fence(), OperationID: "operation_terminal",
		TerminalStatus: agentv1.TurnStatusCompleted,
	}); err != nil {
		t.Fatal(err)
	}

	stream := newTestStream(t, store, nil)
	// Terminal state is not permission to drop the tail: an observer starting
	// from zero must still receive every event before EOF.
	subscription, err := stream.Subscribe(context.Background(), attachFrom(turn, 0))
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sequences := drainStream(t, ctx, subscription)
	if len(sequences) != 3 {
		t.Fatalf("observed %v, want admission, running and terminal", sequences)
	}
	assertContiguous(t, sequences, 0)

	// A caller resuming past the end sees EOF immediately, not a hang.
	resumed, err := stream.Subscribe(context.Background(), attachFrom(turn, 3))
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	if _, err := resumed.Next(ctx); !errors.Is(err, io.EOF) {
		t.Fatalf("exhausted Next() error = %v, want io.EOF", err)
	}
}

func TestTurnEventStreamResumesExactlyWhereAnObserverStopped(t *testing.T) {
	_, store, _, turns := newSQLClaimNextFixture(t, "stream_resume")
	turn := turns[0]
	claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_stream_resume"))
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 4; index++ {
		if _, err := store.CommitAttempt(context.Background(), CommitAttemptCommand{
			Fence:       claimed.Attempt.Fence(),
			OperationID: fmt.Sprintf("operation_resume_%d", index),
			Event:       &EventDraft{Type: "writer.document.delta", Data: json.RawMessage(`{}`)},
		}); err != nil {
			t.Fatal(err)
		}
	}
	stream := newTestStream(t, store, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	first, err := stream.Subscribe(context.Background(), attachFrom(turn, 0))
	if err != nil {
		t.Fatal(err)
	}
	var seen []agentv1.Sequence
	for index := 0; index < 3; index++ {
		event, err := first.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		seen = append(seen, event.Sequence)
	}
	cursor := first.Cursor()
	// Detaching must not disturb the Turn.
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Next(ctx); !errors.Is(err, ErrSubscriptionClosed) {
		t.Fatalf("closed Next() error = %v, want ErrSubscriptionClosed", err)
	}

	second, err := stream.Subscribe(context.Background(), attachFrom(turn, cursor))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	for index := 0; index < 3; index++ {
		event, err := second.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		seen = append(seen, event.Sequence)
	}
	assertContiguous(t, seen, 0)

	// The Turn is still live: an observer detaching is not a cancellation.
	if _, err := store.CommitAttempt(context.Background(), CommitAttemptCommand{
		Fence: claimed.Attempt.Fence(), OperationID: "operation_resume_after_detach",
		TerminalStatus: agentv1.TurnStatusCompleted,
	}); err != nil {
		t.Fatalf("detaching an observer disturbed execution: %v", err)
	}
}

func TestTurnEventStreamRejectsBadCursorsAndForeignOwners(t *testing.T) {
	_, store, _, turns := newSQLClaimNextFixture(t, "stream_reject")
	turn := turns[0]
	stream := newTestStream(t, store, nil)

	// Cursor errors surface at Subscribe so an attach can be answered without
	// opening a stream the caller would immediately have to abort.
	if _, err := stream.Subscribe(context.Background(), attachFrom(turn, 999)); !errors.Is(err, ErrReplayCursorAhead) {
		t.Fatalf("ahead cursor Subscribe() error = %v, want ErrReplayCursorAhead", err)
	}
	foreign := attachFrom(turn, 0)
	foreign.PrincipalID = "principal_someone_else"
	if _, err := stream.Subscribe(context.Background(), foreign); !errors.Is(err, ErrTurnNotFound) {
		t.Fatalf("cross-owner Subscribe() error = %v, want ErrTurnNotFound", err)
	}
	crossThread := attachFrom(turn, 0)
	crossThread.ThreadID = "thread_someone_else"
	if _, err := stream.Subscribe(context.Background(), crossThread); !errors.Is(err, ErrTurnNotFound) {
		t.Fatalf("cross-thread Subscribe() error = %v, want ErrTurnNotFound", err)
	}
}

func TestTurnEventStreamIsCorrectWithoutTheBrokerAndConcurrentObserversAgree(t *testing.T) {
	_, store, _, turns := newSQLClaimNextFixture(t, "stream_fanout")
	turn := turns[0]
	claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_stream_fanout"))
	if err != nil {
		t.Fatal(err)
	}

	// No broker at all: notifications are an optimization, never correctness.
	stream := newTestStream(t, store, nil)
	const observers = 4
	const liveEvents = 6
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var wait sync.WaitGroup
	results := make([][]agentv1.Sequence, observers)
	for index := 0; index < observers; index++ {
		subscription, err := stream.Subscribe(context.Background(), attachFrom(turn, 0))
		if err != nil {
			t.Fatal(err)
		}
		wait.Add(1)
		go func(index int, subscription *TurnSubscription) {
			defer wait.Done()
			defer subscription.Close()
			results[index] = drainStream(t, ctx, subscription)
		}(index, subscription)
	}

	for index := 0; index < liveEvents; index++ {
		if _, err := store.CommitAttempt(context.Background(), CommitAttemptCommand{
			Fence:       claimed.Attempt.Fence(),
			OperationID: fmt.Sprintf("operation_fanout_%d", index),
			Event:       &EventDraft{Type: "writer.document.delta", Data: json.RawMessage(`{}`)},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.CommitAttempt(context.Background(), CommitAttemptCommand{
		Fence: claimed.Attempt.Fence(), OperationID: "operation_fanout_terminal",
		TerminalStatus: agentv1.TurnStatusCompleted,
	}); err != nil {
		t.Fatal(err)
	}
	wait.Wait()

	want := 2 + liveEvents + 1
	for index, sequences := range results {
		if len(sequences) != want {
			t.Fatalf("observer %d saw %d events, want %d: %v", index, len(sequences), want, sequences)
		}
		assertContiguous(t, sequences, 0)
	}
}

func TestTurnEventBrokerWakesWaitersWithoutBlocking(t *testing.T) {
	broker := NewTurnEventBroker()
	signal, release := broker.Watch("turn_broker")
	other, releaseOther := broker.Watch("turn_other")

	// Notifying with nobody waiting, and notifying twice, must not block.
	broker.NotifyTurnEvent("turn_absent")
	broker.NotifyTurnEvent("turn_broker")
	broker.NotifyTurnEvent("turn_broker")
	select {
	case <-signal:
	default:
		t.Fatal("waiter was not woken")
	}
	select {
	case <-other:
		t.Fatal("a waiter on a different Turn was woken")
	default:
	}

	release()
	releaseOther()
	broker.mu.Lock()
	remaining := len(broker.waiters)
	broker.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("broker retained %d waiter buckets after release", remaining)
	}
	// Notifying a released waiter must be a no-op, not a panic on a closed
	// channel or a leak.
	broker.NotifyTurnEvent("turn_broker")
}

func TestNewTurnEventStreamRejectsUnsafeConfiguration(t *testing.T) {
	_, store, _, _ := newSQLClaimNextFixture(t, "stream_config")
	if _, err := NewTurnEventStream(nil, EventStreamOptions{}); err == nil {
		t.Fatal("nil reader accepted")
	}
	if _, err := NewTurnEventStream(store, EventStreamOptions{PollInterval: MaxStreamPollInterval + time.Second}); err == nil {
		t.Fatal("oversized poll interval accepted")
	}
	stream, err := NewTurnEventStream(store, EventStreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if stream.options.PollInterval != DefaultStreamPollInterval || stream.options.PageLimit != DefaultStreamPageLimit {
		t.Fatalf("defaults not applied: %+v", stream.options)
	}
}
