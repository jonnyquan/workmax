package agentturn

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	agentv1 "server/contracts/agent/v1"
)

type ServiceConfig struct {
	Store           Store
	NewTurnID       func() (agentv1.TurnID, error)
	Now             func() time.Time
	ReplayPageLimit int
}

type Service struct {
	store           Store
	newTurnID       func() (agentv1.TurnID, error)
	now             func() time.Time
	replayPageLimit int
}

func NewService(config ServiceConfig) (*Service, error) {
	if config.Store == nil {
		return nil, fmt.Errorf("durable turn store is required")
	}
	if config.NewTurnID == nil {
		config.NewTurnID = randomTurnID
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.ReplayPageLimit == 0 {
		config.ReplayPageLimit = 256
	}
	if config.ReplayPageLimit < 1 || config.ReplayPageLimit > 1000 {
		return nil, fmt.Errorf("replay page limit must be between 1 and 1000")
	}
	return &Service{
		store:           config.Store,
		newTurnID:       config.NewTurnID,
		now:             config.Now,
		replayPageLimit: config.ReplayPageLimit,
	}, nil
}

// Start durably admits a queued Turn. Execution is deliberately separate; a
// successful return means only that the Turn and its first event committed.
func (s *Service) Start(ctx context.Context, command StartCommand) (StartResult, error) {
	return s.start(ctx, command, s.store.Admit)
}

// StartWithReservationAuthority is the explicit commercial admission path.
// The supplied authority is request-scoped and is invoked only by a Store that
// implements the stronger transaction-local admission contract. Start remains
// unchanged and never discovers or invokes this authority implicitly.
func (s *Service) StartWithReservationAuthority(
	ctx context.Context,
	command StartCommand,
	authority TurnReservationAdmissionAuthority,
) (StartResult, error) {
	if turnReservationAdmissionAuthorityMissing(authority) {
		return StartResult{}, ErrTurnReservationAdmissionAuthorityUnavailable
	}
	store, ok := s.store.(TurnReservationAdmissionStore)
	if !ok {
		return StartResult{}, ErrTurnReservationAdmissionAuthorityUnavailable
	}
	return s.start(ctx, command, func(ctx context.Context, candidate Turn, initial EventDraft) (AdmissionRecord, error) {
		return store.AdmitWithReservationAuthority(ctx, candidate, initial, authority)
	})
}

func (s *Service) start(
	ctx context.Context,
	command StartCommand,
	admit func(context.Context, Turn, EventDraft) (AdmissionRecord, error),
) (StartResult, error) {
	if err := command.Validate(); err != nil {
		return StartResult{}, err
	}
	if err := contextError(ctx); err != nil {
		return StartResult{}, err
	}
	turnID, err := s.newTurnID()
	if err != nil {
		return StartResult{}, fmt.Errorf("mint durable turn id: %w", err)
	}
	if err := validatePathSegment("turnId", string(turnID), MaxTurnIDBytes); err != nil {
		return StartResult{}, fmt.Errorf("mint durable turn id: %w", err)
	}
	now := s.now().UTC()
	if now.IsZero() {
		return StartResult{}, fmt.Errorf("durable turn clock returned zero time")
	}
	candidate := Turn{
		ID:             turnID,
		PrincipalID:    command.PrincipalID,
		ThreadID:       command.Request.ThreadID,
		IdempotencyKey: command.Request.IdempotencyKey,
		CommandDigest:  command.CommandDigest,
		Plugin:         command.Plugin,
		Status:         agentv1.TurnStatusQueued,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	initial, err := statusEvent(agentv1.TurnStatusQueued, false)
	if err != nil {
		return StartResult{}, err
	}
	record, err := admit(ctx, candidate, initial)
	if err != nil {
		return StartResult{}, err
	}
	admission := agentv1.StartAdmissionResult{
		TurnID:    record.Turn.ID,
		StreamURL: defaultStreamURL(record.Turn.ThreadID, record.Turn.ID),
	}
	if err := admission.Validate(); err != nil {
		return StartResult{}, fmt.Errorf("build start admission result: %w", err)
	}
	return StartResult{
		Admission:        admission,
		Turn:             record.Turn,
		IdempotentReplay: !record.Created,
	}, nil
}

// Status returns the durable row only when Principal ownership matches.
func (s *Service) Status(ctx context.Context, request OwnedTurnRequest) (Turn, error) {
	if err := request.Validate(); err != nil {
		return Turn{}, err
	}
	return s.store.GetOwned(ctx, request.PrincipalID, request.ThreadID, request.TurnID)
}

// Attach returns a replay snapshot strictly after the resolved cursor. It
// does not acquire a worker lease, change Turn state or bind execution to the
// observer context.
func (s *Service) Attach(ctx context.Context, command AttachCommand) (Attachment, error) {
	if err := command.Validate(); err != nil {
		return Attachment{}, err
	}
	record, err := s.store.Replay(ctx, command.PrincipalID, command.ThreadID, command.Request.TurnID, ReplayQuery{
		Cursor: command.Request.Cursor,
		Limit:  s.replayPageLimit,
	})
	if err != nil {
		return Attachment{}, err
	}
	return Attachment{
		Turn:          record.Turn,
		AfterSequence: record.AfterSequence,
		Window:        record.Window,
		Events:        record.Events,
		HasMore:       record.HasMore,
	}, nil
}

// AppendDomainEvent persists a Plugin-domain event for a non-terminal Turn.
// The core.* namespace is reserved for state-machine operations. The Store
// assigns identity and sequence under the same lock/transaction.
//
// This method is not a worker fencing boundary. Production execution must
// wrap it in an attempt/lease token check before it is made reachable.
func (s *Service) AppendDomainEvent(ctx context.Context, turnID agentv1.TurnID, draft EventDraft) (agentv1.EventEnvelope, error) {
	if err := validatePathSegment("turnId", string(turnID), MaxTurnIDBytes); err != nil {
		return agentv1.EventEnvelope{}, err
	}
	if err := draft.Validate(); err != nil {
		return agentv1.EventEnvelope{}, err
	}
	if strings.HasPrefix(string(draft.Type), "core.") {
		return agentv1.EventEnvelope{}, fmt.Errorf("%w: %q", ErrReservedEventType, draft.Type)
	}
	return s.store.AppendEvent(ctx, turnID, draft)
}

// Transition applies the durable state machine and appends one status event
// atomically. Same-state retries are idempotent and do not append duplicates.
func (s *Service) Transition(ctx context.Context, turnID agentv1.TurnID, to agentv1.TurnStatus) (TransitionResult, error) {
	if err := validatePathSegment("turnId", string(turnID), MaxTurnIDBytes); err != nil {
		return TransitionResult{}, err
	}
	if !to.Valid() {
		return TransitionResult{}, fmt.Errorf("unknown turn status %q", to)
	}
	draft, err := statusEvent(to, to == agentv1.TurnStatusStopped)
	if err != nil {
		return TransitionResult{}, err
	}
	return s.store.Transition(ctx, turnID, to, s.now().UTC(), draft)
}

// Cancel records intent and emits an observable event, but leaves the Turn in
// queued/running state. A worker later reconciles by transitioning to stopped.
func (s *Service) Cancel(ctx context.Context, command CancelCommand) (CancelResult, error) {
	if err := command.Validate(); err != nil {
		return CancelResult{}, err
	}
	draft, err := cancelRequestedEvent()
	if err != nil {
		return CancelResult{}, err
	}
	return s.store.RequestCancel(ctx, command.PrincipalID, command.ThreadID, command.Request.TurnID, s.now().UTC(), draft)
}

func statusEvent(status agentv1.TurnStatus, cancellationRequested bool) (EventDraft, error) {
	return statusEventWithSettlementReview(status, cancellationRequested, nil)
}

func statusEventWithSettlementReview(
	status agentv1.TurnStatus,
	cancellationRequested bool,
	review *SettlementReviewRecord,
) (EventDraft, error) {
	payload := struct {
		Status                 agentv1.TurnStatus `json:"status"`
		CancellationRequested  bool               `json:"cancellationRequested,omitempty"`
		SettlementReviewID     string             `json:"settlementReviewId,omitempty"`
		SettlementReviewDigest string             `json:"settlementReviewDigest,omitempty"`
	}{Status: status, CancellationRequested: cancellationRequested}
	if review != nil {
		if !review.Source.executor() || review.TerminalStatus != status {
			return EventDraft{}, ErrStoreIntegrity
		}
		payload.SettlementReviewID = review.ReviewID
		payload.SettlementReviewDigest = review.RequestDigest
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return EventDraft{}, err
	}
	return EventDraft{Type: agentv1.EventCoreTurnStatus, Data: data}, nil
}

func cancelRequestedEvent() (EventDraft, error) {
	data, err := json.Marshal(struct {
		CancellationRequested bool `json:"cancellationRequested"`
	}{CancellationRequested: true})
	if err != nil {
		return EventDraft{}, err
	}
	return EventDraft{Type: agentv1.EventCoreTurnStatus, Data: data}, nil
}

func randomTurnID() (agentv1.TurnID, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return agentv1.TurnID("turn_" + hex.EncodeToString(raw[:])), nil
}

func defaultStreamURL(threadID agentv1.ThreadID, turnID agentv1.TurnID) string {
	return "/api/v1/agent/threads/" + url.PathEscape(string(threadID)) + "/turns/" + url.PathEscape(string(turnID)) + "/stream"
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("context is required")
	}
	return ctx.Err()
}
