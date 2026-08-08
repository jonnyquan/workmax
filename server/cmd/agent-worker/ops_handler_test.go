package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWorkerOpsHandlerHealthMatrix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		path       string
		snapshot   workerHealthSnapshot
		wantCode   int
		wantStatus string
	}{
		{
			name: "starting is live",
			path: "/livez",
			snapshot: workerHealthSnapshot{
				Phase: string(workerPhaseStarting), Live: true, Ready: false,
				Reasons: []string{string(reasonCompositionPending)},
			},
			wantCode: http.StatusOK, wantStatus: "live",
		},
		{
			name: "starting is not ready",
			path: "/readyz",
			snapshot: workerHealthSnapshot{
				Phase: string(workerPhaseStarting), Live: true, Ready: false,
				Reasons: []string{string(reasonStartupProbePending)},
			},
			wantCode: http.StatusServiceUnavailable, wantStatus: "not_ready",
		},
		{
			name: "serving is live",
			path: "/livez",
			snapshot: workerHealthSnapshot{
				Phase: string(workerPhaseServing), Live: true, Ready: true, Reasons: []string{},
			},
			wantCode: http.StatusOK, wantStatus: "live",
		},
		{
			name: "serving is ready",
			path: "/readyz",
			snapshot: workerHealthSnapshot{
				Phase: string(workerPhaseServing), Live: true, Ready: true, Reasons: []string{},
			},
			wantCode: http.StatusOK, wantStatus: "ready",
		},
		{
			name: "draining remains live",
			path: "/livez",
			snapshot: workerHealthSnapshot{
				Phase: string(workerPhaseDraining), Live: true, Ready: false,
				Reasons: []string{string(reasonDraining)},
			},
			wantCode: http.StatusOK, wantStatus: "live",
		},
		{
			name: "draining is not ready",
			path: "/readyz",
			snapshot: workerHealthSnapshot{
				Phase: string(workerPhaseDraining), Live: true, Ready: false,
				Reasons: []string{string(reasonDraining)},
			},
			wantCode: http.StatusServiceUnavailable, wantStatus: "not_ready",
		},
		{
			name: "failed is not live",
			path: "/livez",
			snapshot: workerHealthSnapshot{
				Phase: string(workerPhaseFailed), Live: false, Ready: false,
				Reasons: []string{string(reasonRuntimeFailed)},
			},
			wantCode: http.StatusServiceUnavailable, wantStatus: "not_live",
		},
		{
			name: "stopped is not ready",
			path: "/readyz",
			snapshot: workerHealthSnapshot{
				Phase: string(workerPhaseStopped), Live: false, Ready: false,
				Reasons: []string{string(reasonStopped)},
			},
			wantCode: http.StatusServiceUnavailable, wantStatus: "not_ready",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handler := workerOpsHandler{snapshot: func() workerHealthSnapshot { return test.snapshot }}
			response := serveWorkerOpsRequest(t, handler, httptest.NewRequest(http.MethodGet, test.path, nil))
			if response.Code != test.wantCode {
				t.Fatalf("status code = %d, want %d; body=%s", response.Code, test.wantCode, response.Body.String())
			}
			body := decodeWorkerOpsResponse(t, response)
			if body.Status != test.wantStatus {
				t.Errorf("status = %q, want %q", body.Status, test.wantStatus)
			}
			if body.Phase != test.snapshot.Phase {
				t.Errorf("phase = %q, want %q", body.Phase, test.snapshot.Phase)
			}
			if body.Schema != workerOpsSchema || body.Role != workerOpsRole {
				t.Errorf("contract identity = (%q, %q), want (%q, %q)", body.Schema, body.Role, workerOpsSchema, workerOpsRole)
			}
			assertWorkerOpsHeaders(t, response)
		})
	}
}

func TestWorkerOpsHandlerPreservesStableResourceCloseReasons(t *testing.T) {
	t.Parallel()
	wantReasons := []string{string(reasonRuntimeFailed), string(reasonResourceCloseTimeout)}
	handler := workerOpsHandler{snapshot: func() workerHealthSnapshot {
		return workerHealthSnapshot{
			Phase: string(workerPhaseFailed), Live: false, Ready: false,
			Reasons: wantReasons,
		}
	}}
	response := serveWorkerOpsRequest(t, handler, httptest.NewRequest(http.MethodGet, "/livez", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	body := decodeWorkerOpsResponse(t, response)
	if strings.Join(body.Reasons, ",") != strings.Join(wantReasons, ",") {
		t.Fatalf("reasons = %v, want %v", body.Reasons, wantReasons)
	}
}

func TestWorkerOpsHandlerRejectsRequestsOutsideClosedSurface(t *testing.T) {
	t.Parallel()

	baseSnapshot := workerHealthSnapshot{
		Phase: string(workerPhaseServing), Live: true, Ready: true, Reasons: []string{},
	}
	tests := []struct {
		name       string
		request    func() *http.Request
		wantCode   int
		wantStatus string
		wantReason string
		wantAllow  string
	}{
		{
			name: "browser origin",
			request: func() *http.Request {
				request := httptest.NewRequest(http.MethodGet, "/livez", nil)
				request.Header.Set("Origin", "https://portal.example")
				return request
			},
			wantCode: http.StatusForbidden, wantStatus: "forbidden", wantReason: "origin_not_allowed",
		},
		{
			name: "empty origin header",
			request: func() *http.Request {
				request := httptest.NewRequest(http.MethodGet, "/livez", nil)
				request.Header["origin"] = []string{""}
				return request
			},
			wantCode: http.StatusForbidden, wantStatus: "forbidden", wantReason: "origin_not_allowed",
		},
		{
			name:       "query",
			request:    func() *http.Request { return httptest.NewRequest(http.MethodGet, "/livez?verbose=1", nil) },
			wantCode:   http.StatusBadRequest,
			wantStatus: "bad_request",
			wantReason: "query_not_allowed",
		},
		{
			name: "empty query marker",
			request: func() *http.Request {
				request := httptest.NewRequest(http.MethodGet, "/livez", nil)
				request.URL.ForceQuery = true
				return request
			},
			wantCode: http.StatusBadRequest, wantStatus: "bad_request", wantReason: "query_not_allowed",
		},
		{
			name: "body",
			request: func() *http.Request {
				return httptest.NewRequest(http.MethodGet, "/readyz", strings.NewReader(`{"probe":true}`))
			},
			wantCode:   http.StatusBadRequest,
			wantStatus: "bad_request",
			wantReason: "body_not_allowed",
		},
		{
			name: "transfer encoding",
			request: func() *http.Request {
				request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
				request.TransferEncoding = []string{"chunked"}
				return request
			},
			wantCode: http.StatusBadRequest, wantStatus: "bad_request", wantReason: "transfer_encoding_not_allowed",
		},
		{
			name: "transfer encoding header",
			request: func() *http.Request {
				request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
				request.Header["Transfer-Encoding"] = []string{"trailers"}
				return request
			},
			wantCode: http.StatusBadRequest, wantStatus: "bad_request", wantReason: "transfer_encoding_not_allowed",
		},
		{
			name: "body with zero declared length",
			request: func() *http.Request {
				request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
				request.Body = io.NopCloser(strings.NewReader("hidden"))
				request.ContentLength = 0
				return request
			},
			wantCode: http.StatusBadRequest, wantStatus: "bad_request", wantReason: "body_not_allowed",
		},
		{
			name:       "post",
			request:    func() *http.Request { return httptest.NewRequest(http.MethodPost, "/livez", nil) },
			wantCode:   http.StatusMethodNotAllowed,
			wantStatus: "method_not_allowed",
			wantReason: "method_not_allowed",
			wantAllow:  http.MethodGet,
		},
		{
			name:       "head",
			request:    func() *http.Request { return httptest.NewRequest(http.MethodHead, "/readyz", nil) },
			wantCode:   http.StatusMethodNotAllowed,
			wantStatus: "method_not_allowed",
			wantReason: "method_not_allowed",
			wantAllow:  http.MethodGet,
		},
		{
			name:       "unknown",
			request:    func() *http.Request { return httptest.NewRequest(http.MethodGet, "/metrics", nil) },
			wantCode:   http.StatusNotFound,
			wantStatus: "not_found",
			wantReason: "route_not_found",
		},
		{
			name:       "trailing slash",
			request:    func() *http.Request { return httptest.NewRequest(http.MethodGet, "/livez/", nil) },
			wantCode:   http.StatusNotFound,
			wantStatus: "not_found",
			wantReason: "route_not_found",
		},
		{
			name:       "encoded alias",
			request:    func() *http.Request { return httptest.NewRequest(http.MethodGet, "/live%7a", nil) },
			wantCode:   http.StatusNotFound,
			wantStatus: "not_found",
			wantReason: "route_not_found",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handler := workerOpsHandler{snapshot: func() workerHealthSnapshot { return baseSnapshot }}
			response := serveWorkerOpsRequest(t, handler, test.request())
			if response.Code != test.wantCode {
				t.Fatalf("status code = %d, want %d; body=%s", response.Code, test.wantCode, response.Body.String())
			}
			body := decodeWorkerOpsResponse(t, response)
			if body.Status != test.wantStatus {
				t.Errorf("status = %q, want %q", body.Status, test.wantStatus)
			}
			if len(body.Reasons) != 1 || body.Reasons[0] != test.wantReason {
				t.Errorf("reasons = %#v, want [%q]", body.Reasons, test.wantReason)
			}
			if got := response.Header().Get("Allow"); got != test.wantAllow {
				t.Errorf("Allow = %q, want %q", got, test.wantAllow)
			}
			assertWorkerOpsHeaders(t, response)
		})
	}
}

func TestWorkerOpsHandlerReducesUnexpectedHealthDetailsToStableCodes(t *testing.T) {
	t.Parallel()

	snapshot := workerHealthSnapshot{
		Phase: "serving config=/private/config.yaml password=hunter2",
		Live:  true,
		Ready: true,
		Reasons: []string{
			"mysql://agent:secret@db.internal/workmax",
			"digest=0123456789abcdef",
			"turn_id=turn-sensitive",
		},
	}
	handler := workerOpsHandler{snapshot: func() workerHealthSnapshot { return snapshot }}
	response := serveWorkerOpsRequest(t, handler, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status code = %d, want %d; body=%s", response.Code, http.StatusServiceUnavailable, response.Body.String())
	}
	bodyText := response.Body.String()
	for _, forbidden := range []string{
		"config.yaml", "hunter2", "mysql://", "secret", "db.internal",
		"0123456789abcdef", "turn-sensitive",
	} {
		if strings.Contains(bodyText, forbidden) {
			t.Errorf("response leaked %q: %s", forbidden, bodyText)
		}
	}
	body := decodeWorkerOpsResponseText(t, bodyText)
	if body.Phase != string(workerPhaseFailed) || body.Status != "not_ready" {
		t.Errorf("sanitized state = phase %q status %q, want failed/not_ready", body.Phase, body.Status)
	}
	if len(body.Reasons) != 1 || body.Reasons[0] != string(reasonRuntimeFailed) {
		t.Errorf("sanitized reasons = %#v, want [%q]", body.Reasons, reasonRuntimeFailed)
	}
}

func TestWorkerOpsHandlerFailsClosedOnUnknownReasonInOtherwiseLiveSnapshot(t *testing.T) {
	t.Parallel()

	handler := workerOpsHandler{snapshot: func() workerHealthSnapshot {
		return workerHealthSnapshot{
			Phase: string(workerPhaseServing), Live: true, Ready: false,
			Reasons: []string{"mysql://agent:secret@db.internal/workmax"},
		}
	}}
	response := serveWorkerOpsRequest(t, handler, httptest.NewRequest(http.MethodGet, "/livez", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status code = %d, want %d; body=%s", response.Code, http.StatusServiceUnavailable, response.Body.String())
	}
	bodyText := response.Body.String()
	for _, forbidden := range []string{"mysql://", "secret", "db.internal"} {
		if strings.Contains(bodyText, forbidden) {
			t.Errorf("response leaked %q: %s", forbidden, bodyText)
		}
	}
	body := decodeWorkerOpsResponseText(t, bodyText)
	if body.Phase != string(workerPhaseFailed) || body.Status != "not_live" ||
		len(body.Reasons) != 1 || body.Reasons[0] != string(reasonRuntimeFailed) {
		t.Fatalf("sanitized response = %+v, want failed/not_live/runtime_failed", body)
	}
}

func TestWorkerOpsHandlerFailsClosedOnInconsistentHealthBooleans(t *testing.T) {
	t.Parallel()

	for name, snapshot := range map[string]workerHealthSnapshot{
		"stopped but ready": {
			Phase: string(workerPhaseStopped), Live: false, Ready: true, Reasons: []string{},
		},
		"failed but live": {
			Phase: string(workerPhaseFailed), Live: true, Ready: false,
			Reasons: []string{string(reasonRuntimeFailed)},
		},
		"draining but ready": {
			Phase: string(workerPhaseDraining), Live: true, Ready: true, Reasons: []string{},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			handler := workerOpsHandler{snapshot: func() workerHealthSnapshot { return snapshot }}
			for _, path := range []string{"/livez", "/readyz"} {
				response := serveWorkerOpsRequest(t, handler, httptest.NewRequest(http.MethodGet, path, nil))
				if response.Code != http.StatusServiceUnavailable {
					t.Fatalf("%s status = %d, want 503; body=%s", path, response.Code, response.Body.String())
				}
				body := decodeWorkerOpsResponse(t, response)
				if body.Phase != string(workerPhaseFailed) || len(body.Reasons) != 1 ||
					body.Reasons[0] != string(reasonRuntimeFailed) {
					t.Fatalf("%s response = %+v, want failed/runtime_failed", path, body)
				}
			}
		})
	}
}

func TestWorkerOpsHandlerDoesNotListenAndFailsClosedWithoutHealth(t *testing.T) {
	t.Parallel()

	handler := newWorkerOpsHandler(nil)
	response := serveWorkerOpsRequest(t, handler, httptest.NewRequest(http.MethodGet, "/livez", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status code = %d, want %d; body=%s", response.Code, http.StatusServiceUnavailable, response.Body.String())
	}
	body := decodeWorkerOpsResponse(t, response)
	if body.Status != "not_live" || body.Phase != string(workerPhaseFailed) {
		t.Errorf("nil health state = status %q phase %q, want not_live/failed", body.Status, body.Phase)
	}
	if len(body.Reasons) != 1 || body.Reasons[0] != string(reasonRuntimeFailed) {
		t.Errorf("reasons = %#v, want [%q]", body.Reasons, reasonRuntimeFailed)
	}
}

func serveWorkerOpsRequest(t *testing.T, handler http.Handler, request *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeWorkerOpsResponse(t *testing.T, response *httptest.ResponseRecorder) workerOpsResponse {
	t.Helper()
	return decodeWorkerOpsResponseText(t, response.Body.String())
}

func decodeWorkerOpsResponseText(t *testing.T, bodyText string) workerOpsResponse {
	t.Helper()
	var response workerOpsResponse
	decoder := json.NewDecoder(strings.NewReader(bodyText))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, bodyText)
	}
	if response.Reasons == nil {
		t.Fatalf("reasons must be a JSON array, got null; body=%s", bodyText)
	}
	return response
}

func assertWorkerOpsHeaders(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	wants := map[string]string{
		"Content-Type":           "application/json; charset=utf-8",
		"Cache-Control":          "no-store",
		"X-Content-Type-Options": "nosniff",
	}
	for name, want := range wants {
		if got := response.Header().Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}
