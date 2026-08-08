package agentturn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	agentv1 "server/contracts/agent/v1"
)

var ErrSubscriptionClosed = errors.New("durable turn subscription is closed")

const (
	DefaultStreamPollInterval = 250 * time.Millisecond
	MaxStreamPollInterval     = 10 * time.Second
	DefaultStreamPageLimit    = 64
)

// ReplayReader is the narrow persistence boundary a stream needs. Keeping it
// separate from Store makes explicit that observing a Turn takes no lease,
// touches no execution state and cannot mutate anything.
type ReplayReader interface {
	Replay(ctx context.Context, principalID PrincipalID, threadID agentv1.ThreadID, turnID agentv1.TurnID, query ReplayQuery) (ReplayRecord, error)
}

// TurnEventBroker wakes waiting observers when a Turn gains events.
//
// It is strictly an optimization. Correctness comes from the monotonic
// sequence in the durable log, which the subscription re-reads on every poll,
// so a dropped, duplicated or entirely absent notification changes latency and
// nothing else. That is deliberate: a broadcaster whose delivery is load
// bearing has to solve exactly-once fan-out, and this one does not.
type TurnEventBroker struct {
	mu      sync.Mutex
	waiters map[agentv1.TurnID][]chan struct{}
}

func NewTurnEventBroker() *TurnEventBroker {
	return &TurnEventBroker{waiters: make(map[agentv1.TurnID][]chan struct{})}
}

// Watch must be called before the caller reads the log. Registering after the
// read would drop a notification published in between and stall the observer
// until the next poll tick.
func (broker *TurnEventBroker) Watch(turnID agentv1.TurnID) (<-chan struct{}, func()) {
	signal := make(chan struct{}, 1)
	broker.mu.Lock()
	broker.waiters[turnID] = append(broker.waiters[turnID], signal)
	broker.mu.Unlock()

	return signal, func() {
		broker.mu.Lock()
		defer broker.mu.Unlock()
		remaining := broker.waiters[turnID][:0]
		for _, candidate := range broker.waiters[turnID] {
			if candidate != signal {
				remaining = append(remaining, candidate)
			}
		}
		if len(remaining) == 0 {
			delete(broker.waiters, turnID)
			return
		}
		broker.waiters[turnID] = remaining
	}
}

// NotifyTurnEvent is non-blocking. A waiter whose buffer already holds a
// pending signal needs no second one: it will re-read the log anyway.
func (broker *TurnEventBroker) NotifyTurnEvent(turnID agentv1.TurnID) {
	broker.mu.Lock()
	defer broker.mu.Unlock()
	for _, signal := range broker.waiters[turnID] {
		select {
		case signal <- struct{}{}:
		default:
		}
	}
}

type EventStreamOptions struct {
	// PollInterval bounds how long an observer waits without a notification.
	// Zero selects DefaultStreamPollInterval.
	PollInterval time.Duration
	// PageLimit bounds one replay page. Zero selects DefaultStreamPageLimit.
	PageLimit int
	// Broker is optional. Without it the stream still works, at poll latency.
	Broker *TurnEventBroker
}

func (options *EventStreamOptions) applyDefaults() {
	if options.PollInterval <= 0 {
		options.PollInterval = DefaultStreamPollInterval
	}
	if options.PageLimit <= 0 {
		options.PageLimit = DefaultStreamPageLimit
	}
}

func (options EventStreamOptions) Validate() error {
	if options.PollInterval <= 0 || options.PollInterval > MaxStreamPollInterval {
		return fmt.Errorf("pollInterval must be between 1ns and %s", MaxStreamPollInterval)
	}
	return ReplayQuery{Limit: options.PageLimit}.Validate()
}

// TurnEventStream serves atomic replay-to-live observation.
//
// "Atomic" here means an observer sees every event exactly once in sequence
// order across the handover from stored history to live output — no gap and no
// duplicate at the seam. It is achieved by never having a seam: the
// subscription only ever reads the durable log forward from its cursor, so
// history and live output are the same source. A broadcaster only decides how
// soon the next read happens.
type TurnEventStream struct {
	reader  ReplayReader
	options EventStreamOptions
}

func NewTurnEventStream(reader ReplayReader, options EventStreamOptions) (*TurnEventStream, error) {
	if reader == nil {
		return nil, fmt.Errorf("turn event stream requires a replay reader")
	}
	options.applyDefaults()
	if err := options.Validate(); err != nil {
		return nil, err
	}
	return &TurnEventStream{reader: reader, options: options}, nil
}

// Subscribe validates ownership and the observer cursor before returning.
// Cursor errors surface here rather than on the first Next so a caller can
// answer an attach request without having opened a stream it must then abort.
func (stream *TurnEventStream) Subscribe(ctx context.Context, command AttachCommand) (*TurnSubscription, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if err := command.Validate(); err != nil {
		return nil, err
	}
	turnID := agentv1.TurnID(command.Request.TurnID)
	first, err := stream.reader.Replay(ctx, command.PrincipalID, command.ThreadID, turnID, ReplayQuery{
		Cursor: command.Request.Cursor,
		Limit:  stream.options.PageLimit,
	})
	if err != nil {
		return nil, err
	}
	return &TurnSubscription{
		stream:      stream,
		principalID: command.PrincipalID,
		threadID:    command.ThreadID,
		turnID:      turnID,
		cursor:      first.AfterSequence,
		buffered:    first.Events,
		latest:      first.Window.LatestSequence,
		terminal:    first.Turn.Status.Terminal(),
	}, nil
}

// TurnSubscription is one observer's position in a Turn's event log.
//
// It holds no lease and no execution rights. Closing it detaches the observer
// and must never cancel, fence or settle the Turn: a client that hangs up mid
// generation is a detached observer, not a cancellation.
type TurnSubscription struct {
	stream      *TurnEventStream
	principalID PrincipalID
	threadID    agentv1.ThreadID
	turnID      agentv1.TurnID

	mu       sync.Mutex
	cursor   agentv1.Sequence
	buffered []agentv1.EventEnvelope
	latest   agentv1.Sequence
	terminal bool

	closed atomic.Bool
}

// Next returns the observer's next event, blocking until one exists.
//
// It returns io.EOF only once the Turn is terminal and every event up to the
// authoritative latest sequence has been handed out. A terminal Turn with
// undelivered events keeps streaming: terminal state is not permission to drop
// the tail of the log.
func (subscription *TurnSubscription) Next(ctx context.Context) (agentv1.EventEnvelope, error) {
	for {
		if subscription.closed.Load() {
			return agentv1.EventEnvelope{}, ErrSubscriptionClosed
		}
		if err := contextError(ctx); err != nil {
			return agentv1.EventEnvelope{}, err
		}
		if event, ok := subscription.pop(); ok {
			return event, nil
		}
		if subscription.finished() {
			return agentv1.EventEnvelope{}, io.EOF
		}

		// Register interest before reading. A commit landing between the read
		// and the wait would otherwise be missed until the poll deadline.
		var signal <-chan struct{}
		release := func() {}
		if broker := subscription.stream.options.Broker; broker != nil {
			signal, release = broker.Watch(subscription.turnID)
		}
		advanced, err := subscription.advance(ctx)
		if err != nil {
			release()
			return agentv1.EventEnvelope{}, err
		}
		if advanced {
			release()
			continue
		}

		timer := time.NewTimer(subscription.stream.options.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			release()
			return agentv1.EventEnvelope{}, ctx.Err()
		case <-signal:
		case <-timer.C:
		}
		timer.Stop()
		release()
	}
}

// advance reads one page forward from the cursor. It reports whether anything
// new arrived so the caller can skip the wait.
func (subscription *TurnSubscription) advance(ctx context.Context) (bool, error) {
	subscription.mu.Lock()
	after := subscription.cursor
	subscription.mu.Unlock()

	cursor := agentv1.ReplayCursor{AfterSequence: &after}
	record, err := subscription.stream.reader.Replay(ctx,
		subscription.principalID, subscription.threadID, subscription.turnID,
		ReplayQuery{Cursor: cursor, Limit: subscription.stream.options.PageLimit})
	if err != nil {
		return false, err
	}

	subscription.mu.Lock()
	defer subscription.mu.Unlock()
	subscription.latest = record.Window.LatestSequence
	subscription.terminal = record.Turn.Status.Terminal()
	if len(record.Events) == 0 {
		return false, nil
	}
	subscription.buffered = append(subscription.buffered, record.Events...)
	return true, nil
}

func (subscription *TurnSubscription) pop() (agentv1.EventEnvelope, bool) {
	subscription.mu.Lock()
	defer subscription.mu.Unlock()
	for len(subscription.buffered) > 0 {
		event := subscription.buffered[0]
		subscription.buffered = subscription.buffered[1:]
		// The log is the only ordering authority. A page that somehow repeated
		// an already-delivered sequence is dropped rather than re-emitted.
		if event.Sequence <= subscription.cursor {
			continue
		}
		subscription.cursor = event.Sequence
		return event, true
	}
	return agentv1.EventEnvelope{}, false
}

func (subscription *TurnSubscription) finished() bool {
	subscription.mu.Lock()
	defer subscription.mu.Unlock()
	return subscription.terminal && len(subscription.buffered) == 0 && subscription.cursor >= subscription.latest
}

// Cursor reports the last delivered sequence so a reconnecting observer can
// resume exactly where it stopped.
func (subscription *TurnSubscription) Cursor() agentv1.Sequence {
	subscription.mu.Lock()
	defer subscription.mu.Unlock()
	return subscription.cursor
}

// Close detaches this observer only. It never cancels, fences or settles the
// Turn, and it never signals the executor.
func (subscription *TurnSubscription) Close() error {
	subscription.closed.Store(true)
	return nil
}
