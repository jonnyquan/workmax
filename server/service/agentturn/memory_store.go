package agentturn

import (
	"context"
	"fmt"
	"sync"
	"time"

	agentv1 "server/contracts/agent/v1"
)

type idempotencyScope struct {
	principal PrincipalID
	thread    agentv1.ThreadID
	key       agentv1.IdempotencyKey
}

// MemoryStore is a concurrency-safe reference implementation of Store. It is
// only a test/local contract harness. Process restart loses data, so it must
// never back a production or pilot HTTP endpoint.
type MemoryStore struct {
	mu           sync.RWMutex
	turns        map[agentv1.TurnID]Turn
	byIdem       map[idempotencyScope]agentv1.TurnID
	events       map[agentv1.TurnID][]agentv1.EventEnvelope
	lastSequence map[agentv1.TurnID]agentv1.Sequence
}

var _ Store = (*MemoryStore)(nil)

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		turns:        make(map[agentv1.TurnID]Turn),
		byIdem:       make(map[idempotencyScope]agentv1.TurnID),
		events:       make(map[agentv1.TurnID][]agentv1.EventEnvelope),
		lastSequence: make(map[agentv1.TurnID]agentv1.Sequence),
	}
}

func (s *MemoryStore) Admit(ctx context.Context, candidate Turn, initial EventDraft) (AdmissionRecord, error) {
	if err := contextError(ctx); err != nil {
		return AdmissionRecord{}, err
	}
	if err := candidate.Validate(); err != nil {
		return AdmissionRecord{}, fmt.Errorf("invalid admitted turn: %w", err)
	}
	if candidate.Status != agentv1.TurnStatusQueued || candidate.CancelRequestedAt != nil || candidate.StartedAt != nil || candidate.FinishedAt != nil {
		return AdmissionRecord{}, fmt.Errorf("invalid admitted turn lifecycle")
	}
	if err := initial.Validate(); err != nil {
		return AdmissionRecord{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureMapsLocked()
	if err := contextError(ctx); err != nil {
		return AdmissionRecord{}, err
	}
	scope := idempotencyScope{principal: candidate.PrincipalID, thread: candidate.ThreadID, key: candidate.IdempotencyKey}
	if existingID, ok := s.byIdem[scope]; ok {
		existing := s.turns[existingID]
		if existing.CommandDigest != candidate.CommandDigest {
			return AdmissionRecord{}, fmt.Errorf("%w for principal %q thread %q", ErrIdempotencyConflict, candidate.PrincipalID, candidate.ThreadID)
		}
		return AdmissionRecord{Turn: cloneTurn(existing), Created: false}, nil
	}
	if _, exists := s.turns[candidate.ID]; exists {
		return AdmissionRecord{}, fmt.Errorf("%w: %q", ErrTurnIDConflict, candidate.ID)
	}
	first, err := buildEvent(candidate, 1, initial)
	if err != nil {
		return AdmissionRecord{}, err
	}
	s.turns[candidate.ID] = cloneTurn(candidate)
	s.byIdem[scope] = candidate.ID
	s.events[candidate.ID] = []agentv1.EventEnvelope{cloneEvent(first)}
	s.lastSequence[candidate.ID] = 1
	return AdmissionRecord{Turn: cloneTurn(candidate), Created: true}, nil
}

func (s *MemoryStore) GetOwned(ctx context.Context, principalID PrincipalID, threadID agentv1.ThreadID, turnID agentv1.TurnID) (Turn, error) {
	if err := contextError(ctx); err != nil {
		return Turn{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	turn, ok := s.turns[turnID]
	if !ok || turn.PrincipalID != principalID || turn.ThreadID != threadID {
		return Turn{}, ErrTurnNotFound
	}
	return cloneTurn(turn), nil
}

func (s *MemoryStore) AppendEvent(ctx context.Context, turnID agentv1.TurnID, draft EventDraft) (agentv1.EventEnvelope, error) {
	if err := contextError(ctx); err != nil {
		return agentv1.EventEnvelope{}, err
	}
	if err := draft.Validate(); err != nil {
		return agentv1.EventEnvelope{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := contextError(ctx); err != nil {
		return agentv1.EventEnvelope{}, err
	}
	turn, ok := s.turns[turnID]
	if !ok {
		return agentv1.EventEnvelope{}, ErrTurnNotFound
	}
	if turn.Status.Terminal() {
		return agentv1.EventEnvelope{}, fmt.Errorf("%w: %q", ErrTurnTerminal, turn.Status)
	}
	event, err := s.nextEventLocked(turn, draft)
	if err != nil {
		return agentv1.EventEnvelope{}, err
	}
	s.events[turnID] = append(s.events[turnID], cloneEvent(event))
	s.lastSequence[turnID] = event.Sequence
	return cloneEvent(event), nil
}

func (s *MemoryStore) Transition(ctx context.Context, turnID agentv1.TurnID, to agentv1.TurnStatus, at time.Time, eventDraft EventDraft) (TransitionResult, error) {
	if err := contextError(ctx); err != nil {
		return TransitionResult{}, err
	}
	if at.IsZero() {
		return TransitionResult{}, fmt.Errorf("transition time is required")
	}
	if err := eventDraft.Validate(); err != nil {
		return TransitionResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := contextError(ctx); err != nil {
		return TransitionResult{}, err
	}
	turn, ok := s.turns[turnID]
	if !ok {
		return TransitionResult{}, ErrTurnNotFound
	}
	if turn.Status == to {
		return TransitionResult{Turn: cloneTurn(turn), Changed: false}, nil
	}
	if !CanTransition(turn.Status, to) {
		return TransitionResult{}, transitionError(turn.Status, to)
	}
	if to == agentv1.TurnStatusStopped && turn.CancelRequestedAt == nil {
		return TransitionResult{}, ErrCancellationNotRequested
	}

	at = at.UTC()
	next := cloneTurn(turn)
	next.Status = to
	next.UpdatedAt = at
	if to == agentv1.TurnStatusRunning && next.StartedAt == nil {
		next.StartedAt = timePointer(at)
	}
	if to.Terminal() {
		next.FinishedAt = timePointer(at)
	}
	if err := next.Validate(); err != nil {
		return TransitionResult{}, fmt.Errorf("invalid transitioned turn: %w", err)
	}
	event, err := s.nextEventLocked(next, eventDraft)
	if err != nil {
		return TransitionResult{}, err
	}
	s.turns[turnID] = cloneTurn(next)
	s.events[turnID] = append(s.events[turnID], cloneEvent(event))
	s.lastSequence[turnID] = event.Sequence
	return TransitionResult{Turn: cloneTurn(next), Changed: true, Event: eventPointer(event)}, nil
}

func (s *MemoryStore) RequestCancel(ctx context.Context, principalID PrincipalID, threadID agentv1.ThreadID, turnID agentv1.TurnID, at time.Time, eventDraft EventDraft) (CancelResult, error) {
	if err := contextError(ctx); err != nil {
		return CancelResult{}, err
	}
	if at.IsZero() {
		return CancelResult{}, fmt.Errorf("cancellation time is required")
	}
	if err := eventDraft.Validate(); err != nil {
		return CancelResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := contextError(ctx); err != nil {
		return CancelResult{}, err
	}
	turn, ok := s.turns[turnID]
	if !ok || turn.PrincipalID != principalID || turn.ThreadID != threadID {
		return CancelResult{}, ErrTurnNotFound
	}
	if turn.Status.Terminal() || turn.CancelRequestedAt != nil {
		return CancelResult{Turn: cloneTurn(turn), NewlyRequested: false}, nil
	}

	at = at.UTC()
	next := cloneTurn(turn)
	next.CancelRequestedAt = timePointer(at)
	next.UpdatedAt = at
	if err := next.Validate(); err != nil {
		return CancelResult{}, fmt.Errorf("invalid cancellation intent: %w", err)
	}
	event, err := s.nextEventLocked(next, eventDraft)
	if err != nil {
		return CancelResult{}, err
	}
	s.turns[turnID] = cloneTurn(next)
	s.events[turnID] = append(s.events[turnID], cloneEvent(event))
	s.lastSequence[turnID] = event.Sequence
	return CancelResult{Turn: cloneTurn(next), NewlyRequested: true, Event: eventPointer(event)}, nil
}

func (s *MemoryStore) Replay(ctx context.Context, principalID PrincipalID, threadID agentv1.ThreadID, turnID agentv1.TurnID, query ReplayQuery) (ReplayRecord, error) {
	if err := contextError(ctx); err != nil {
		return ReplayRecord{}, err
	}
	if err := query.Validate(); err != nil {
		return ReplayRecord{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	turn, ok := s.turns[turnID]
	if !ok || turn.PrincipalID != principalID || turn.ThreadID != threadID {
		return ReplayRecord{}, ErrTurnNotFound
	}
	events := s.events[turnID]
	window := agentv1.ReplayWindow{}
	if len(events) > 0 {
		window.OldestSequence = events[0].Sequence
		window.LatestSequence = events[len(events)-1].Sequence
	}
	after := agentv1.Sequence(0)
	if query.Cursor.AfterSequence != nil {
		after = *query.Cursor.AfterSequence
	}
	if query.Cursor.LastEventID != "" {
		found := false
		for _, event := range events {
			if event.EventID == query.Cursor.LastEventID {
				found = true
				if event.Sequence > after {
					after = event.Sequence
				}
				break
			}
		}
		if !found {
			return ReplayRecord{}, fmt.Errorf("%w: %q", ErrReplayCursorNotFound, query.Cursor.LastEventID)
		}
	}
	if (window.Empty() && after > 0) || (!window.Empty() && after > window.LatestSequence) {
		return ReplayRecord{}, &ReplayBoundaryError{Kind: ErrReplayCursorAhead, Cursor: after, Window: window}
	}
	if query.Cursor.AfterSequence != nil && !window.Empty() && after < window.OldestSequence-1 {
		return ReplayRecord{}, &ReplayBoundaryError{Kind: ErrReplayGap, Cursor: after, Window: window}
	}
	replayed := make([]agentv1.EventEnvelope, 0, min(query.Limit, len(events)))
	hasMore := false
	for _, event := range events {
		if event.Sequence > after {
			if len(replayed) == query.Limit {
				hasMore = true
				break
			}
			replayed = append(replayed, cloneEvent(event))
		}
	}
	return ReplayRecord{
		Turn:          cloneTurn(turn),
		AfterSequence: after,
		Window:        window,
		Events:        replayed,
		HasMore:       hasMore,
	}, nil
}

func (s *MemoryStore) nextEventLocked(turn Turn, draft EventDraft) (agentv1.EventEnvelope, error) {
	sequence := agentv1.Sequence(1)
	if latest := s.lastSequence[turn.ID]; latest > 0 {
		if latest >= MaxDurableSequence {
			return agentv1.EventEnvelope{}, ErrSequenceExhausted
		}
		sequence = latest + 1
	}
	return buildEvent(turn, sequence, draft)
}

func buildEvent(turn Turn, sequence agentv1.Sequence, draft EventDraft) (agentv1.EventEnvelope, error) {
	if sequence == 0 || sequence > MaxDurableSequence {
		return agentv1.EventEnvelope{}, ErrSequenceExhausted
	}
	event := agentv1.EventEnvelope{
		SchemaVersion: agentv1.EventEnvelopeSchemaVersion,
		FrameKind:     agentv1.EventFrameEvent,
		EventID:       fmt.Sprintf("%s:%d", turn.ID, sequence),
		TurnID:        turn.ID,
		Sequence:      sequence,
		Plugin:        turn.Plugin,
		Type:          draft.Type,
		Visibility:    agentv1.EventVisibilityUser,
		ResourceRefs:  append([]string(nil), draft.ResourceRefs...),
		Data:          append([]byte(nil), draft.Data...),
	}
	if err := event.Validate(); err != nil {
		return agentv1.EventEnvelope{}, fmt.Errorf("build durable turn event: %w", err)
	}
	return event, nil
}

func (s *MemoryStore) ensureMapsLocked() {
	if s.turns == nil {
		s.turns = make(map[agentv1.TurnID]Turn)
	}
	if s.byIdem == nil {
		s.byIdem = make(map[idempotencyScope]agentv1.TurnID)
	}
	if s.events == nil {
		s.events = make(map[agentv1.TurnID][]agentv1.EventEnvelope)
	}
	if s.lastSequence == nil {
		s.lastSequence = make(map[agentv1.TurnID]agentv1.Sequence)
	}
}

func cloneTurn(turn Turn) Turn {
	turn.CancelRequestedAt = cloneTimePointer(turn.CancelRequestedAt)
	turn.StartedAt = cloneTimePointer(turn.StartedAt)
	turn.FinishedAt = cloneTimePointer(turn.FinishedAt)
	return turn
}

func cloneEvent(event agentv1.EventEnvelope) agentv1.EventEnvelope {
	event.ResourceRefs = append([]string(nil), event.ResourceRefs...)
	event.Data = append([]byte(nil), event.Data...)
	return event
}

func cloneTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func timePointer(value time.Time) *time.Time {
	return &value
}

func eventPointer(event agentv1.EventEnvelope) *agentv1.EventEnvelope {
	cloned := cloneEvent(event)
	return &cloned
}
