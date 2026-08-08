package agentv1api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	agentcontract "server/contracts/agent/v1"
	"server/service/agentturn"
)

var candidateTestTime = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

type principalResolverFunc func(*gin.Context) (agentturn.PrincipalID, error)

func (f principalResolverFunc) ResolvePrincipal(c *gin.Context) (agentturn.PrincipalID, error) {
	return f(c)
}

type startResolverFunc func(context.Context, StartResolutionInput) (agentturn.StartCommand, error)

func (f startResolverFunc) ResolveStart(ctx context.Context, input StartResolutionInput) (agentturn.StartCommand, error) {
	return f(ctx, input)
}

type eventStreamFunc func(context.Context, agentturn.AttachCommand) (EventSubscription, error)

func (f eventStreamFunc) Subscribe(ctx context.Context, command agentturn.AttachCommand) (EventSubscription, error) {
	return f(ctx, command)
}

type fakeRuntime struct {
	start  func(context.Context, agentturn.StartCommand) (agentturn.StartResult, error)
	status func(context.Context, agentturn.OwnedTurnRequest) (agentturn.Turn, error)
	cancel func(context.Context, agentturn.CancelCommand) (agentturn.CancelResult, error)
}

func (runtime *fakeRuntime) Start(ctx context.Context, command agentturn.StartCommand) (agentturn.StartResult, error) {
	return runtime.start(ctx, command)
}

func (runtime *fakeRuntime) Status(ctx context.Context, request agentturn.OwnedTurnRequest) (agentturn.Turn, error) {
	return runtime.status(ctx, request)
}

func (runtime *fakeRuntime) Cancel(ctx context.Context, command agentturn.CancelCommand) (agentturn.CancelResult, error) {
	return runtime.cancel(ctx, command)
}

type sliceSubscription struct {
	events []agentcontract.EventEnvelope
	index  int
	closed bool
}

func (subscription *sliceSubscription) Next(context.Context) (agentcontract.EventEnvelope, error) {
	if subscription.index >= len(subscription.events) {
		return agentcontract.EventEnvelope{}, io.EOF
	}
	event := subscription.events[subscription.index]
	subscription.index++
	return event, nil
}

func (subscription *sliceSubscription) Close() error {
	subscription.closed = true
	return nil
}

func TestCandidateRouteCatalogIsFixedAndDefensive(t *testing.T) {
	routes := CandidateRoutes()
	want := []CandidateRoute{
		{ID: "agent.turn.start", Method: http.MethodPost, Path: StartTurnsPath},
		{ID: "agent.turn.status", Method: http.MethodGet, Path: TurnStatusPath},
		{ID: "agent.turn.stream", Method: http.MethodGet, Path: TurnStreamPath},
		{ID: "agent.turn.cancel", Method: http.MethodPost, Path: CancelTurnPath},
	}
	if len(routes) != len(want) {
		t.Fatalf("route count = %d, want %d", len(routes), len(want))
	}
	for index := range want {
		if routes[index] != want[index] {
			t.Errorf("route %d = %+v, want %+v", index, routes[index], want[index])
		}
	}
	routes[0].Path = "/mutated"
	if CandidateRoutes()[0].Path != StartTurnsPath {
		t.Fatal("caller mutated the canonical route catalog")
	}
}

func TestStartStrictlyAdmitsResolvedServerCommand(t *testing.T) {
	var resolved StartResolutionInput
	var started agentturn.StartCommand
	runtime := defaultFakeRuntime()
	runtime.start = func(_ context.Context, command agentturn.StartCommand) (agentturn.StartResult, error) {
		started = command
		turn := candidateTurn(command.PrincipalID, command.Request.ThreadID, "turn_1")
		return agentturn.StartResult{
			Admission: agentcontract.StartAdmissionResult{
				TurnID:    turn.ID,
				StreamURL: "/api/v1/agent/threads/thread_1/turns/turn_1/stream",
			},
			Turn: turn,
		}, nil
	}
	handler := mustCandidateHandler(t, runtime,
		startResolverFunc(func(_ context.Context, input StartResolutionInput) (agentturn.StartCommand, error) {
			resolved = input
			return resolvedCommand(input), nil
		}),
		eventStreamFunc(noEventStream),
	)
	router := candidateRouter(handler)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agent/threads/thread_1/turns", strings.NewReader(`{"command":"draft"}`))
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set("Idempotency-Key", "idem-0123456789abcdef")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	if resolved.PrincipalID != "principal_1" || resolved.ThreadID != "thread_1" || resolved.IdempotencyKey != "idem-0123456789abcdef" {
		t.Fatalf("resolver input = %+v", resolved)
	}
	if string(resolved.Body) != `{"command":"draft"}` {
		t.Fatalf("resolver body = %s", resolved.Body)
	}
	if started.CommandDigest != "sha256:server-resolved" || started.Plugin.ID != "workmax.writer" {
		t.Fatalf("runtime command = %+v", started)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("admission response is cacheable")
	}
	var admission agentcontract.StartAdmissionResult
	if err := json.Unmarshal(response.Body.Bytes(), &admission); err != nil || admission.TurnID != "turn_1" {
		t.Fatalf("admission response = %+v, err=%v", admission, err)
	}
}

func TestStartRejectsAmbiguousOrUnsafeWireInputBeforeResolver(t *testing.T) {
	resolverCalls := 0
	handler := mustCandidateHandler(t, defaultFakeRuntime(),
		startResolverFunc(func(_ context.Context, input StartResolutionInput) (agentturn.StartCommand, error) {
			resolverCalls++
			return resolvedCommand(input), nil
		}),
		eventStreamFunc(noEventStream),
	)
	router := candidateRouter(handler)
	tests := []struct {
		name        string
		contentType string
		keyValues   []string
		body        string
	}{
		{name: "wrong content type", contentType: "text/plain", keyValues: []string{"idem-0123456789abcdef"}, body: `{}`},
		{name: "missing key", contentType: "application/json", body: `{}`},
		{name: "short key", contentType: "application/json", keyValues: []string{"short"}, body: `{}`},
		{name: "key whitespace", contentType: "application/json", keyValues: []string{" idem-0123456789abcdef"}, body: `{}`},
		{name: "duplicate key", contentType: "application/json", keyValues: []string{"idem-0123456789abcdef", "idem-abcdef0123456789"}, body: `{}`},
		{name: "array body", contentType: "application/json", keyValues: []string{"idem-0123456789abcdef"}, body: `[]`},
		{name: "two json values", contentType: "application/json", keyValues: []string{"idem-0123456789abcdef"}, body: `{} {}`},
		{name: "malformed json", contentType: "application/json", keyValues: []string{"idem-0123456789abcdef"}, body: `{`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/v1/agent/threads/thread_1/turns", strings.NewReader(test.body))
			if test.contentType != "" {
				request.Header.Set("Content-Type", test.contentType)
			}
			for _, value := range test.keyValues {
				request.Header.Add("Idempotency-Key", value)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			assertErrorCode(t, response, http.StatusBadRequest, ErrorInvalidRequest)
		})
	}
	if resolverCalls != 0 {
		t.Fatalf("unsafe requests reached resolver %d times", resolverCalls)
	}
}

func TestStartMapsIdempotencyConflictWithoutLeakingCommand(t *testing.T) {
	runtime := defaultFakeRuntime()
	runtime.start = func(context.Context, agentturn.StartCommand) (agentturn.StartResult, error) {
		return agentturn.StartResult{}, fmt.Errorf("%w: secret-command-digest", agentturn.ErrIdempotencyConflict)
	}
	handler := mustCandidateHandler(t, runtime, startResolverFunc(func(_ context.Context, input StartResolutionInput) (agentturn.StartCommand, error) {
		return resolvedCommand(input), nil
	}), eventStreamFunc(noEventStream))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agent/threads/thread_1/turns", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "idem-0123456789abcdef")
	response := httptest.NewRecorder()
	candidateRouter(handler).ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusConflict, ErrorIdempotencyConflict)
	if strings.Contains(response.Body.String(), "secret-command-digest") {
		t.Fatal("error response leaked runtime details")
	}
}

func TestCandidateErrorCatalogMapsAdmissionFailuresWithoutLeakingDetails(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		status    int
		code      ErrorCode
		retryable bool
	}{
		{name: "revision conflict", err: ErrRevisionConflict, status: http.StatusConflict, code: ErrorRevisionConflict},
		{name: "too many turns", err: ErrTooManyTurns, status: http.StatusTooManyRequests, code: ErrorTooManyTurns, retryable: true},
		{name: "insufficient credits", err: ErrInsufficientCredits, status: http.StatusPaymentRequired, code: ErrorInsufficientCredits},
		{name: "start disabled", err: ErrStartDisabled, status: http.StatusServiceUnavailable, code: ErrorStartDisabled, retryable: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := defaultFakeRuntime()
			runtime.start = func(context.Context, agentturn.StartCommand) (agentturn.StartResult, error) {
				return agentturn.StartResult{}, fmt.Errorf("%w: private-admission-detail", test.err)
			}
			handler := mustCandidateHandler(t, runtime, startResolverFunc(defaultStartResolver), eventStreamFunc(noEventStream))
			request := httptest.NewRequest(http.MethodPost, "/api/v1/agent/threads/thread_1/turns", strings.NewReader(`{}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", "idem-0123456789abcdef")
			response := httptest.NewRecorder()
			candidateRouter(handler).ServeHTTP(response, request)

			assertErrorCode(t, response, test.status, test.code)
			var envelope errorEnvelope
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decode error envelope: %v", err)
			}
			if envelope.Error.Retryable != test.retryable {
				t.Fatalf("retryable = %v, want %v", envelope.Error.Retryable, test.retryable)
			}
			if strings.Contains(response.Body.String(), "private-admission-detail") {
				t.Fatal("error response leaked admission details")
			}
		})
	}
}

func TestStatusAndCancelBindPrincipalThreadAndTurn(t *testing.T) {
	runtime := defaultFakeRuntime()
	runtime.status = func(_ context.Context, request agentturn.OwnedTurnRequest) (agentturn.Turn, error) {
		if request.PrincipalID != "principal_1" || request.ThreadID != "thread_1" || request.TurnID != "turn_1" {
			return agentturn.Turn{}, agentturn.ErrTurnNotFound
		}
		return candidateTurn(request.PrincipalID, request.ThreadID, request.TurnID), nil
	}
	cancelCalls := 0
	runtime.cancel = func(_ context.Context, command agentturn.CancelCommand) (agentturn.CancelResult, error) {
		cancelCalls++
		if command.PrincipalID != "principal_1" || command.ThreadID != "thread_1" || command.Request.TurnID != "turn_1" {
			return agentturn.CancelResult{}, agentturn.ErrTurnNotFound
		}
		turn := candidateTurn(command.PrincipalID, command.ThreadID, command.Request.TurnID)
		at := candidateTestTime.Add(time.Second)
		turn.CancelRequestedAt = &at
		turn.UpdatedAt = at
		return agentturn.CancelResult{Turn: turn, NewlyRequested: true}, nil
	}
	handler := mustCandidateHandler(t, runtime, startResolverFunc(defaultStartResolver), eventStreamFunc(noEventStream))
	router := candidateRouter(handler)

	statusResponse := httptest.NewRecorder()
	router.ServeHTTP(statusResponse, httptest.NewRequest(http.MethodGet, "/api/v1/agent/threads/thread_1/turns/turn_1", nil))
	if statusResponse.Code != http.StatusOK || !strings.Contains(statusResponse.Body.String(), `"turnId":"turn_1"`) {
		t.Fatalf("status response = %d %s", statusResponse.Code, statusResponse.Body.String())
	}

	cancelResponse := httptest.NewRecorder()
	router.ServeHTTP(cancelResponse, httptest.NewRequest(http.MethodPost, "/api/v1/agent/threads/thread_1/turns/turn_1/cancel", nil))
	if cancelResponse.Code != http.StatusAccepted || !strings.Contains(cancelResponse.Body.String(), `"cancellationRequested":true`) {
		t.Fatalf("cancel response = %d %s", cancelResponse.Code, cancelResponse.Body.String())
	}
	if cancelCalls != 1 {
		t.Fatalf("cancel calls = %d, want 1", cancelCalls)
	}

	wrongThread := httptest.NewRecorder()
	router.ServeHTTP(wrongThread, httptest.NewRequest(http.MethodGet, "/api/v1/agent/threads/thread_2/turns/turn_1", nil))
	assertErrorCode(t, wrongThread, http.StatusNotFound, ErrorTurnNotFound)

	bodyCancel := httptest.NewRecorder()
	router.ServeHTTP(bodyCancel, httptest.NewRequest(http.MethodPost, "/api/v1/agent/threads/thread_1/turns/turn_1/cancel", strings.NewReader(`{}`)))
	assertErrorCode(t, bodyCancel, http.StatusBadRequest, ErrorInvalidRequest)
	if cancelCalls != 1 {
		t.Fatal("cancel with a body reached runtime")
	}
}

func TestStreamPreservesEnvelopeCursorAndObserverDetachSemantics(t *testing.T) {
	events := []agentcontract.EventEnvelope{
		candidateEvent(2, "turn_1:2"),
		candidateEvent(3, "turn_1:3"),
	}
	subscription := &sliceSubscription{events: events}
	var attached agentturn.AttachCommand
	stream := eventStreamFunc(func(_ context.Context, command agentturn.AttachCommand) (EventSubscription, error) {
		attached = command
		return subscription, nil
	})
	cancelCalls := 0
	runtime := defaultFakeRuntime()
	runtime.cancel = func(context.Context, agentturn.CancelCommand) (agentturn.CancelResult, error) {
		cancelCalls++
		return agentturn.CancelResult{}, nil
	}
	handler := mustCandidateHandler(t, runtime, startResolverFunc(defaultStartResolver), stream)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent/threads/thread_1/turns/turn_1/stream?after=1", nil)
	request.Header.Set("Last-Event-ID", "turn_1:1")
	response := httptest.NewRecorder()
	candidateRouter(handler).ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/event-stream; charset=utf-8" {
		t.Fatalf("stream response = %d headers=%v", response.Code, response.Header())
	}
	if attached.PrincipalID != "principal_1" || attached.ThreadID != "thread_1" || attached.Request.TurnID != "turn_1" {
		t.Fatalf("attach command = %+v", attached)
	}
	if attached.Request.Cursor.AfterSequence == nil || *attached.Request.Cursor.AfterSequence != 1 || attached.Request.Cursor.LastEventID != "turn_1:1" {
		t.Fatalf("cursor = %+v", attached.Request.Cursor)
	}
	body := response.Body.String()
	for _, fragment := range []string{
		"id: turn_1:2\n",
		"event: assistant.text.delta\n",
		`"schemaVersion":1`,
		`"sequence":3`,
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("stream body missing %q: %s", fragment, body)
		}
	}
	if !subscription.closed {
		t.Fatal("observer subscription was not detached")
	}
	if cancelCalls != 0 {
		t.Fatal("observer completion invoked Turn cancel")
	}
}

func TestStreamRejectsAmbiguousCursorBeforeSubscribe(t *testing.T) {
	subscribeCalls := 0
	handler := mustCandidateHandler(t, defaultFakeRuntime(), startResolverFunc(defaultStartResolver), eventStreamFunc(func(context.Context, agentturn.AttachCommand) (EventSubscription, error) {
		subscribeCalls++
		return &sliceSubscription{}, nil
	}))
	for _, target := range []string{
		"/api/v1/agent/threads/thread_1/turns/turn_1/stream?after=1&after=2",
		"/api/v1/agent/threads/thread_1/turns/turn_1/stream?after=-1",
		"/api/v1/agent/threads/thread_1/turns/turn_1/stream?after=18446744073709551616",
	} {
		response := httptest.NewRecorder()
		candidateRouter(handler).ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		assertErrorCode(t, response, http.StatusBadRequest, ErrorInvalidRequest)
	}
	if subscribeCalls != 0 {
		t.Fatalf("invalid cursor reached stream %d times", subscribeCalls)
	}
}

func TestStreamLastEventIDUsesDurableContractBound(t *testing.T) {
	subscribeCalls := 0
	handler := mustCandidateHandler(t, defaultFakeRuntime(), startResolverFunc(defaultStartResolver), eventStreamFunc(func(_ context.Context, command agentturn.AttachCommand) (EventSubscription, error) {
		subscribeCalls++
		if len(command.Request.Cursor.LastEventID) > agentturn.MaxEventIDBytes {
			t.Fatalf("oversized Last-Event-ID reached Subscribe: %d bytes", len(command.Request.Cursor.LastEventID))
		}
		return &sliceSubscription{}, nil
	}))
	router := candidateRouter(handler)

	for _, size := range []int{256, 257, agentturn.MaxEventIDBytes} {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/agent/threads/thread_1/turns/turn_1/stream", nil)
		request.Header.Set("Last-Event-ID", strings.Repeat("e", size))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("Last-Event-ID size %d status = %d, body=%s", size, response.Code, response.Body.String())
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent/threads/thread_1/turns/turn_1/stream", nil)
	request.Header.Set("Last-Event-ID", strings.Repeat("e", agentturn.MaxEventIDBytes+1))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusBadRequest, ErrorInvalidRequest)
	if subscribeCalls != 3 {
		t.Fatalf("Subscribe calls = %d, want only the three in-contract cursors", subscribeCalls)
	}
}

func TestNewHandlerFailsClosedWithoutEveryDependency(t *testing.T) {
	valid := HandlerConfig{
		Runtime:    defaultFakeRuntime(),
		Principals: principalResolverFunc(func(*gin.Context) (agentturn.PrincipalID, error) { return "principal_1", nil }),
		Starts:     startResolverFunc(defaultStartResolver),
		Events:     eventStreamFunc(noEventStream),
	}
	if _, err := NewHandler(valid); err != nil {
		t.Fatalf("valid handler config rejected: %v", err)
	}
	for _, mutate := range []func(*HandlerConfig){
		func(config *HandlerConfig) { config.Runtime = nil },
		func(config *HandlerConfig) { config.Principals = nil },
		func(config *HandlerConfig) { config.Starts = nil },
		func(config *HandlerConfig) { config.Events = nil },
	} {
		candidate := valid
		mutate(&candidate)
		if _, err := NewHandler(candidate); err == nil {
			t.Fatal("incomplete handler config was accepted")
		}
	}
}

func mustCandidateHandler(t *testing.T, runtime TurnRuntime, starts StartResolver, events EventStream) *Handler {
	t.Helper()
	handler, err := NewHandler(HandlerConfig{
		Runtime: runtime,
		Principals: principalResolverFunc(func(*gin.Context) (agentturn.PrincipalID, error) {
			return "principal_1", nil
		}),
		Starts: starts,
		Events: events,
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return handler
}

func candidateRouter(handler *Handler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST(StartTurnsPath, handler.Start)
	router.GET(TurnStatusPath, handler.Status)
	router.GET(TurnStreamPath, handler.Stream)
	router.POST(CancelTurnPath, handler.Cancel)
	return router
}

func defaultFakeRuntime() *fakeRuntime {
	return &fakeRuntime{
		start: func(context.Context, agentturn.StartCommand) (agentturn.StartResult, error) {
			return agentturn.StartResult{}, errors.New("unexpected Start")
		},
		status: func(context.Context, agentturn.OwnedTurnRequest) (agentturn.Turn, error) {
			return agentturn.Turn{}, errors.New("unexpected Status")
		},
		cancel: func(context.Context, agentturn.CancelCommand) (agentturn.CancelResult, error) {
			return agentturn.CancelResult{}, errors.New("unexpected Cancel")
		},
	}
}

func defaultStartResolver(_ context.Context, input StartResolutionInput) (agentturn.StartCommand, error) {
	return resolvedCommand(input), nil
}

func resolvedCommand(input StartResolutionInput) agentturn.StartCommand {
	return agentturn.StartCommand{
		PrincipalID: input.PrincipalID,
		Request: agentcontract.StartRequest{
			ThreadID:       input.ThreadID,
			IdempotencyKey: input.IdempotencyKey,
		},
		CommandDigest: "sha256:server-resolved",
		Plugin: agentcontract.EventPluginRef{
			ID:            "workmax.writer",
			Version:       "1.0.0",
			ReleaseDigest: "sha256:plugin",
		},
	}
}

func noEventStream(context.Context, agentturn.AttachCommand) (EventSubscription, error) {
	return nil, errors.New("unexpected Subscribe")
}

func candidateTurn(principal agentturn.PrincipalID, threadID agentcontract.ThreadID, turnID agentcontract.TurnID) agentturn.Turn {
	return agentturn.Turn{
		ID:             turnID,
		PrincipalID:    principal,
		ThreadID:       threadID,
		IdempotencyKey: "idem-0123456789abcdef",
		CommandDigest:  "sha256:server-resolved",
		Plugin: agentcontract.EventPluginRef{
			ID:            "workmax.writer",
			Version:       "1.0.0",
			ReleaseDigest: "sha256:plugin",
		},
		Status:    agentcontract.TurnStatusQueued,
		CreatedAt: candidateTestTime,
		UpdatedAt: candidateTestTime,
	}
}

func candidateEvent(sequence agentcontract.Sequence, eventID string) agentcontract.EventEnvelope {
	return agentcontract.EventEnvelope{
		SchemaVersion: agentcontract.EventEnvelopeSchemaVersion,
		FrameKind:     agentcontract.EventFrameEvent,
		EventID:       eventID,
		TurnID:        "turn_1",
		Sequence:      sequence,
		Plugin: agentcontract.EventPluginRef{
			ID:            "workmax.writer",
			Version:       "1.0.0",
			ReleaseDigest: "sha256:plugin",
		},
		Type:       agentcontract.EventAssistantTextDelta,
		Visibility: agentcontract.EventVisibilityUser,
		Data:       json.RawMessage(`{"text":"hello"}`),
	}
}

func assertErrorCode(t *testing.T, response *httptest.ResponseRecorder, status int, code ErrorCode) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, status, response.Body.String())
	}
	var envelope errorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error response: %v; body=%s", err, response.Body.String())
	}
	if envelope.Error.Code != code {
		t.Fatalf("error code = %q, want %q", envelope.Error.Code, code)
	}
}
