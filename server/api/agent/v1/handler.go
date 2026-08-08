package agentv1api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	agentcontract "server/contracts/agent/v1"
	"server/service/agentturn"
)

const MaxStartBodyBytes int64 = 1 << 20

var (
	ErrUnauthenticated     = errors.New("agent v1 principal is unauthenticated")
	ErrForbidden           = errors.New("agent v1 principal is forbidden")
	ErrInvalidRequest      = errors.New("agent v1 request is invalid")
	ErrRevisionConflict    = errors.New("agent v1 resource revision conflicts")
	ErrTooManyTurns        = errors.New("agent v1 concurrent turn limit reached")
	ErrInsufficientCredits = errors.New("agent v1 principal has insufficient credits")
	ErrStartDisabled       = errors.New("agent v1 new starts are disabled")
)

type ErrorCode string

const (
	ErrorInvalidRequest      ErrorCode = "INVALID_REQUEST"
	ErrorUnauthenticated     ErrorCode = "UNAUTHENTICATED"
	ErrorInsufficientScope   ErrorCode = "INSUFFICIENT_SCOPE"
	ErrorTurnNotFound        ErrorCode = "TURN_NOT_FOUND"
	ErrorIdempotencyConflict ErrorCode = "IDEMPOTENCY_CONFLICT"
	ErrorCursorAhead         ErrorCode = "CURSOR_AHEAD"
	ErrorReplayGap           ErrorCode = "REPLAY_GAP"
	ErrorRevisionConflict    ErrorCode = "REVISION_CONFLICT"
	ErrorTooManyTurns        ErrorCode = "TOO_MANY_TURNS"
	ErrorInsufficientCredits ErrorCode = "INSUFFICIENT_CREDITS"
	ErrorStartDisabled       ErrorCode = "START_DISABLED"
	ErrorInternal            ErrorCode = "INTERNAL"
)

type TurnRuntime interface {
	Start(context.Context, agentturn.StartCommand) (agentturn.StartResult, error)
	Status(context.Context, agentturn.OwnedTurnRequest) (agentturn.Turn, error)
	Cancel(context.Context, agentturn.CancelCommand) (agentturn.CancelResult, error)
}

type PrincipalResolver interface {
	ResolvePrincipal(*gin.Context) (agentturn.PrincipalID, error)
}

// StartResolutionInput contains client-controlled data only. ResolveStart must
// strictly decode Body against the selected domain command schema and compute
// CommandDigest and Plugin from server-owned policy/catalog state.
type StartResolutionInput struct {
	PrincipalID    agentturn.PrincipalID
	ThreadID       agentcontract.ThreadID
	IdempotencyKey agentcontract.IdempotencyKey
	Body           json.RawMessage
}

type StartResolver interface {
	ResolveStart(context.Context, StartResolutionInput) (agentturn.StartCommand, error)
}

// EventSubscription owns an atomic replay-to-live observation boundary.
// Next returns io.EOF only when the authoritative stream is complete. Close
// detaches this observer and must never cancel or release the Turn execution.
type EventSubscription interface {
	Next(context.Context) (agentcontract.EventEnvelope, error)
	Close() error
}

type EventStream interface {
	Subscribe(context.Context, agentturn.AttachCommand) (EventSubscription, error)
}

type HandlerConfig struct {
	Runtime           TurnRuntime
	Principals        PrincipalResolver
	Starts            StartResolver
	Events            EventStream
	MaxStartBodyBytes int64
}

type Handler struct {
	runtime           TurnRuntime
	principals        PrincipalResolver
	starts            StartResolver
	events            EventStream
	maxStartBodyBytes int64
}

func NewHandler(config HandlerConfig) (*Handler, error) {
	if config.Runtime == nil || config.Principals == nil || config.Starts == nil || config.Events == nil {
		return nil, fmt.Errorf("agent v1 candidate handler requires runtime, principal, start and event dependencies")
	}
	if config.MaxStartBodyBytes == 0 {
		config.MaxStartBodyBytes = MaxStartBodyBytes
	}
	if config.MaxStartBodyBytes < 1 || config.MaxStartBodyBytes > MaxStartBodyBytes {
		return nil, fmt.Errorf("agent v1 start body limit must be between 1 and %d", MaxStartBodyBytes)
	}
	return &Handler{
		runtime:           config.Runtime,
		principals:        config.Principals,
		starts:            config.Starts,
		events:            config.Events,
		maxStartBodyBytes: config.MaxStartBodyBytes,
	}, nil
}

// Start handles the target POST .../turns admission contract. It is not
// registered by this package.
func (handler *Handler) Start(c *gin.Context) {
	principal, ok := handler.resolvePrincipal(c)
	if !ok {
		return
	}
	threadID, ok := pathID(c, "threadId")
	if !ok {
		return
	}
	idempotencyKey, err := requestIdempotencyKey(c.Request)
	if err != nil {
		writeError(c, err)
		return
	}
	body, err := readSingleJSONObject(c, handler.maxStartBodyBytes)
	if err != nil {
		writeError(c, fmt.Errorf("%w: start body", ErrInvalidRequest))
		return
	}
	input := StartResolutionInput{
		PrincipalID:    principal,
		ThreadID:       agentcontract.ThreadID(threadID),
		IdempotencyKey: agentcontract.IdempotencyKey(idempotencyKey),
		Body:           body,
	}
	command, err := handler.starts.ResolveStart(c.Request.Context(), input)
	if err != nil {
		writeError(c, err)
		return
	}
	if command.PrincipalID != input.PrincipalID || command.Request.ThreadID != input.ThreadID || command.Request.IdempotencyKey != input.IdempotencyKey {
		writeError(c, errors.New("start resolver changed authenticated request identity"))
		return
	}
	if err := command.Validate(); err != nil {
		writeError(c, errors.New("start resolver returned an invalid command"))
		return
	}
	result, err := handler.runtime.Start(c.Request.Context(), command)
	if err != nil {
		writeError(c, err)
		return
	}
	if err := result.Admission.Validate(); err != nil || result.Turn.ID != result.Admission.TurnID || result.Turn.ThreadID != input.ThreadID || result.Turn.PrincipalID != principal {
		writeError(c, errors.New("runtime returned an invalid admission"))
		return
	}
	writeJSON(c, http.StatusAccepted, result.Admission)
}

func (handler *Handler) Status(c *gin.Context) {
	principal, threadID, turnID, ok := handler.ownedPath(c)
	if !ok {
		return
	}
	turn, err := handler.runtime.Status(c.Request.Context(), agentturn.OwnedTurnRequest{
		PrincipalID: principal,
		ThreadID:    agentcontract.ThreadID(threadID),
		TurnID:      agentcontract.TurnID(turnID),
	})
	if err != nil {
		writeError(c, err)
		return
	}
	if err := validateOwnedTurn(turn, principal, agentcontract.ThreadID(threadID), agentcontract.TurnID(turnID)); err != nil {
		writeError(c, err)
		return
	}
	writeJSON(c, http.StatusOK, turnResponseFrom(turn))
}

func (handler *Handler) Cancel(c *gin.Context) {
	principal, threadID, turnID, ok := handler.ownedPath(c)
	if !ok {
		return
	}
	if !emptyRequestBody(c.Request) {
		writeError(c, fmt.Errorf("%w: cancel body must be empty", ErrInvalidRequest))
		return
	}
	result, err := handler.runtime.Cancel(c.Request.Context(), agentturn.CancelCommand{
		PrincipalID: principal,
		ThreadID:    agentcontract.ThreadID(threadID),
		Request:     agentcontract.CancelRequest{TurnID: agentcontract.TurnID(turnID)},
	})
	if err != nil {
		writeError(c, err)
		return
	}
	if err := validateOwnedTurn(result.Turn, principal, agentcontract.ThreadID(threadID), agentcontract.TurnID(turnID)); err != nil {
		writeError(c, err)
		return
	}
	writeJSON(c, http.StatusAccepted, turnResponseFrom(result.Turn))
}

// Stream serializes one server-provided atomic replay/live subscription as
// SSE. Client disconnect only closes the observer subscription.
func (handler *Handler) Stream(c *gin.Context) {
	principal, threadID, turnID, ok := handler.ownedPath(c)
	if !ok {
		return
	}
	cursor, err := replayCursor(c.Request)
	if err != nil {
		writeError(c, err)
		return
	}
	subscription, err := handler.events.Subscribe(c.Request.Context(), agentturn.AttachCommand{
		PrincipalID: principal,
		ThreadID:    agentcontract.ThreadID(threadID),
		Request: agentcontract.AttachRequest{
			TurnID: agentcontract.TurnID(turnID),
			Cursor: cursor,
		},
	})
	if err != nil {
		writeError(c, err)
		return
	}
	if subscription == nil {
		writeError(c, errors.New("event stream returned a nil subscription"))
		return
	}
	defer subscription.Close()

	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return
	}
	flusher.Flush()

	var previous agentcontract.Sequence
	if cursor.AfterSequence != nil {
		previous = *cursor.AfterSequence
	}
	for {
		event, nextErr := subscription.Next(c.Request.Context())
		if nextErr != nil {
			return
		}
		if err := validateStreamEvent(event, agentcontract.TurnID(turnID), previous); err != nil {
			return
		}
		payload, err := json.Marshal(event)
		if err != nil {
			return
		}
		if _, err := fmt.Fprintf(c.Writer, "id: %s\nevent: %s\ndata: %s\n\n", event.EventID, event.Type, payload); err != nil {
			return
		}
		previous = event.Sequence
		flusher.Flush()
	}
}

func (handler *Handler) resolvePrincipal(c *gin.Context) (agentturn.PrincipalID, bool) {
	principal, err := handler.principals.ResolvePrincipal(c)
	if err != nil {
		writeError(c, err)
		return "", false
	}
	if strings.TrimSpace(string(principal)) == "" || string(principal) != strings.TrimSpace(string(principal)) {
		writeError(c, ErrUnauthenticated)
		return "", false
	}
	return principal, true
}

func (handler *Handler) ownedPath(c *gin.Context) (agentturn.PrincipalID, string, string, bool) {
	principal, ok := handler.resolvePrincipal(c)
	if !ok {
		return "", "", "", false
	}
	threadID, ok := pathID(c, "threadId")
	if !ok {
		return "", "", "", false
	}
	turnID, ok := pathID(c, "turnId")
	if !ok {
		return "", "", "", false
	}
	return principal, threadID, turnID, true
}

func pathID(c *gin.Context, name string) (string, bool) {
	value := c.Param(name)
	if value == "" || value != strings.TrimSpace(value) || len(value) > 256 || strings.ContainsAny(value, "/\\?#%") {
		writeError(c, fmt.Errorf("%w: invalid path identifier", ErrInvalidRequest))
		return "", false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			writeError(c, fmt.Errorf("%w: invalid path identifier", ErrInvalidRequest))
			return "", false
		}
	}
	return value, true
}

func requestIdempotencyKey(request *http.Request) (string, error) {
	values := request.Header.Values("Idempotency-Key")
	if len(values) != 1 {
		return "", fmt.Errorf("%w: exactly one Idempotency-Key is required", ErrInvalidRequest)
	}
	value := values[0]
	if len(value) < 16 || len(value) > 128 || value != strings.TrimSpace(value) {
		return "", fmt.Errorf("%w: invalid Idempotency-Key", ErrInvalidRequest)
	}
	for i := 0; i < len(value); i++ {
		character := value[i]
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("._:-", rune(character)) {
			continue
		}
		return "", fmt.Errorf("%w: invalid Idempotency-Key", ErrInvalidRequest)
	}
	return value, nil
}

func readSingleJSONObject(c *gin.Context, maximum int64) (json.RawMessage, error) {
	mediaType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return nil, ErrInvalidRequest
	}
	reader := http.MaxBytesReader(c.Writer, c.Request.Body, maximum)
	decoder := json.NewDecoder(reader)
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return nil, ErrInvalidRequest
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, ErrInvalidRequest
	}
	return append(json.RawMessage(nil), trimmed...), nil
}

func emptyRequestBody(request *http.Request) bool {
	if request.Body == nil || request.Body == http.NoBody {
		return true
	}
	var one [1]byte
	count, err := request.Body.Read(one[:])
	return count == 0 && errors.Is(err, io.EOF)
}

func replayCursor(request *http.Request) (agentcontract.ReplayCursor, error) {
	queryValues, present := request.URL.Query()["after"]
	if len(queryValues) > 1 {
		return agentcontract.ReplayCursor{}, fmt.Errorf("%w: duplicate after cursor", ErrInvalidRequest)
	}
	var after *agentcontract.Sequence
	if present {
		if len(queryValues) != 1 || queryValues[0] == "" || queryValues[0] != strings.TrimSpace(queryValues[0]) {
			return agentcontract.ReplayCursor{}, fmt.Errorf("%w: invalid after cursor", ErrInvalidRequest)
		}
		parsed, err := strconv.ParseUint(queryValues[0], 10, 64)
		if err != nil {
			return agentcontract.ReplayCursor{}, fmt.Errorf("%w: invalid after cursor", ErrInvalidRequest)
		}
		sequence := agentcontract.Sequence(parsed)
		after = &sequence
	}
	headerValues := request.Header.Values("Last-Event-ID")
	if len(headerValues) > 1 {
		return agentcontract.ReplayCursor{}, fmt.Errorf("%w: duplicate Last-Event-ID", ErrInvalidRequest)
	}
	lastEventID := ""
	if len(headerValues) == 1 {
		lastEventID = headerValues[0]
		if lastEventID == "" || lastEventID != strings.TrimSpace(lastEventID) || len(lastEventID) > agentturn.MaxEventIDBytes || strings.ContainsAny(lastEventID, "\r\n\x00") {
			return agentcontract.ReplayCursor{}, fmt.Errorf("%w: invalid Last-Event-ID", ErrInvalidRequest)
		}
	}
	cursor := agentcontract.ReplayCursor{LastEventID: lastEventID, AfterSequence: after}
	if err := cursor.Validate(); err != nil {
		return agentcontract.ReplayCursor{}, fmt.Errorf("%w: replay cursor", ErrInvalidRequest)
	}
	return cursor, nil
}

func validateStreamEvent(event agentcontract.EventEnvelope, turnID agentcontract.TurnID, previous agentcontract.Sequence) error {
	if err := event.Validate(); err != nil {
		return err
	}
	if event.TurnID != turnID || (previous > 0 && event.Sequence <= previous) {
		return errors.New("event stream identity or sequence mismatch")
	}
	if strings.ContainsAny(event.EventID, "\r\n\x00") || strings.ContainsAny(string(event.Type), "\r\n\x00") {
		return errors.New("event stream contains an unsafe SSE field")
	}
	return nil
}

func validateOwnedTurn(turn agentturn.Turn, principal agentturn.PrincipalID, threadID agentcontract.ThreadID, turnID agentcontract.TurnID) error {
	if err := turn.Validate(); err != nil {
		return errors.New("runtime returned an invalid owned turn")
	}
	if turn.ID != turnID || turn.ThreadID != threadID || turn.PrincipalID != principal {
		return errors.New("runtime returned a mismatched owned turn")
	}
	return nil
}

type turnResponse struct {
	TurnID                agentcontract.TurnID     `json:"turnId"`
	ThreadID              agentcontract.ThreadID   `json:"threadId"`
	Status                agentcontract.TurnStatus `json:"status"`
	CancellationRequested bool                     `json:"cancellationRequested"`
	CreatedAt             time.Time                `json:"createdAt"`
	UpdatedAt             time.Time                `json:"updatedAt"`
	StartedAt             *time.Time               `json:"startedAt,omitempty"`
	FinishedAt            *time.Time               `json:"finishedAt,omitempty"`
}

func turnResponseFrom(turn agentturn.Turn) turnResponse {
	return turnResponse{
		TurnID:                turn.ID,
		ThreadID:              turn.ThreadID,
		Status:                turn.Status,
		CancellationRequested: turn.CancelRequestedAt != nil,
		CreatedAt:             turn.CreatedAt,
		UpdatedAt:             turn.UpdatedAt,
		StartedAt:             turn.StartedAt,
		FinishedAt:            turn.FinishedAt,
	}
}

type errorEnvelope struct {
	Error struct {
		Code      ErrorCode `json:"code"`
		Message   string    `json:"message"`
		Retryable bool      `json:"retryable"`
	} `json:"error"`
}

func writeError(c *gin.Context, err error) {
	status, code, message, retryable := classifyError(err)
	envelope := errorEnvelope{}
	envelope.Error.Code = code
	envelope.Error.Message = message
	envelope.Error.Retryable = retryable
	writeJSON(c, status, envelope)
}

func classifyError(err error) (int, ErrorCode, string, bool) {
	switch {
	case errors.Is(err, ErrUnauthenticated):
		return http.StatusUnauthorized, ErrorUnauthenticated, "Authentication is required.", false
	case errors.Is(err, ErrForbidden):
		return http.StatusForbidden, ErrorInsufficientScope, "The credential cannot access this resource.", false
	case errors.Is(err, ErrInvalidRequest):
		return http.StatusBadRequest, ErrorInvalidRequest, "The request is invalid.", false
	case errors.Is(err, agentturn.ErrTurnNotFound):
		return http.StatusNotFound, ErrorTurnNotFound, "The turn was not found.", false
	case errors.Is(err, agentturn.ErrIdempotencyConflict):
		return http.StatusConflict, ErrorIdempotencyConflict, "The idempotency key conflicts with an existing command.", false
	case errors.Is(err, agentturn.ErrReplayCursorAhead):
		return http.StatusConflict, ErrorCursorAhead, "The replay cursor is ahead of the turn.", false
	case errors.Is(err, agentturn.ErrReplayGap), errors.Is(err, agentturn.ErrReplayCursorNotFound):
		return http.StatusConflict, ErrorReplayGap, "The requested replay history is unavailable.", false
	case errors.Is(err, ErrRevisionConflict):
		return http.StatusConflict, ErrorRevisionConflict, "The resource revision has changed.", false
	case errors.Is(err, ErrTooManyTurns):
		return http.StatusTooManyRequests, ErrorTooManyTurns, "The concurrent turn limit has been reached.", true
	case errors.Is(err, ErrInsufficientCredits):
		return http.StatusPaymentRequired, ErrorInsufficientCredits, "Insufficient credits are available for this turn.", false
	case errors.Is(err, ErrStartDisabled):
		return http.StatusServiceUnavailable, ErrorStartDisabled, "New turn starts are temporarily disabled.", true
	default:
		return http.StatusInternalServerError, ErrorInternal, "The request could not be completed.", false
	}
}

func writeJSON(c *gin.Context, status int, value any) {
	c.Header("Cache-Control", "no-store")
	c.JSON(status, value)
}
