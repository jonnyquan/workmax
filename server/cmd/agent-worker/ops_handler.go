package main

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
)

const (
	workerOpsSchema = "workmax.agent-worker.ops/v1"
	workerOpsRole   = "agent-worker"
)

// workerOpsHandler is an operator-only HTTP surface for this process role.
// It is deliberately just an http.Handler: constructing it neither binds a
// port nor registers anything on http.DefaultServeMux or the API server's
// router. A later production composition must mount it on a separately
// protected operator listener.
type workerOpsHandler struct {
	snapshot func() workerHealthSnapshot
}

type workerOpsResponse struct {
	Schema  string   `json:"schema"`
	Role    string   `json:"role"`
	Status  string   `json:"status"`
	Phase   string   `json:"phase"`
	Reasons []string `json:"reasons"`
}

func newWorkerOpsHandler(health *workerRuntimeHealth) http.Handler {
	return workerOpsHandler{snapshot: health.Snapshot}
}

func (handler workerOpsHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	snapshot := handler.safeSnapshot()
	setWorkerOpsHeaders(writer)

	if request == nil || request.URL == nil {
		handler.write(writer, http.StatusBadRequest, "bad_request", snapshot.Phase, []string{"malformed_request"})
		return
	}

	// RawPath is rejected even when it decodes to an allowed path. This keeps
	// the surface to the two literal request paths and avoids proxy-dependent
	// routing differences for encoded aliases.
	if request.URL.RawPath != "" {
		handler.write(writer, http.StatusNotFound, "not_found", snapshot.Phase, []string{"route_not_found"})
		return
	}
	switch request.URL.Path {
	case "/livez", "/readyz":
	default:
		handler.write(writer, http.StatusNotFound, "not_found", snapshot.Phase, []string{"route_not_found"})
		return
	}

	if workerOpsHasOrigin(request) {
		handler.write(writer, http.StatusForbidden, "forbidden", snapshot.Phase, []string{"origin_not_allowed"})
		return
	}
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		handler.write(writer, http.StatusMethodNotAllowed, "method_not_allowed", snapshot.Phase, []string{"method_not_allowed"})
		return
	}
	if request.URL.RawQuery != "" || request.URL.ForceQuery {
		handler.write(writer, http.StatusBadRequest, "bad_request", snapshot.Phase, []string{"query_not_allowed"})
		return
	}
	if len(request.TransferEncoding) != 0 || len(request.Header.Values("Transfer-Encoding")) != 0 {
		handler.write(writer, http.StatusBadRequest, "bad_request", snapshot.Phase, []string{"transfer_encoding_not_allowed"})
		return
	}
	if request.ContentLength != 0 || workerOpsHasBody(request) {
		handler.write(writer, http.StatusBadRequest, "bad_request", snapshot.Phase, []string{"body_not_allowed"})
		return
	}
	if request.URL.Path == "/livez" {
		if snapshot.Live {
			handler.write(writer, http.StatusOK, "live", snapshot.Phase, snapshot.Reasons)
			return
		}
		handler.write(writer, http.StatusServiceUnavailable, "not_live", snapshot.Phase, snapshot.Reasons)
		return
	}
	if snapshot.Ready {
		handler.write(writer, http.StatusOK, "ready", snapshot.Phase, snapshot.Reasons)
		return
	}
	handler.write(writer, http.StatusServiceUnavailable, "not_ready", snapshot.Phase, snapshot.Reasons)
}

func (handler workerOpsHandler) safeSnapshot() workerHealthSnapshot {
	if handler.snapshot == nil {
		return workerHealthSnapshot{
			Phase:   string(workerPhaseFailed),
			Live:    false,
			Ready:   false,
			Reasons: []string{string(reasonRuntimeFailed)},
		}
	}
	return sanitizeWorkerOpsSnapshot(handler.snapshot())
}

func sanitizeWorkerOpsSnapshot(snapshot workerHealthSnapshot) workerHealthSnapshot {
	if !workerOpsPhaseAllowed(snapshot.Phase) {
		return failedWorkerOpsSnapshot()
	}
	wantLive := snapshot.Phase == string(workerPhaseStarting) ||
		snapshot.Phase == string(workerPhaseServing) ||
		snapshot.Phase == string(workerPhaseDraining)
	if snapshot.Live != wantLive ||
		(snapshot.Ready && (snapshot.Phase != string(workerPhaseServing) || !snapshot.Live || len(snapshot.Reasons) != 0)) {
		return failedWorkerOpsSnapshot()
	}

	reasons := make([]string, 0, len(snapshot.Reasons))
	seen := make(map[string]struct{}, len(snapshot.Reasons))
	invalidReason := false
	for _, reason := range snapshot.Reasons {
		if !workerOpsReasonAllowed(reason) {
			invalidReason = true
			continue
		}
		if _, exists := seen[reason]; exists {
			continue
		}
		seen[reason] = struct{}{}
		reasons = append(reasons, reason)
	}
	if invalidReason {
		return failedWorkerOpsSnapshot()
	}
	if !snapshot.Ready && len(reasons) == 0 {
		reasons = append(reasons, string(reasonRuntimeFailed))
	}
	if snapshot.Ready {
		reasons = []string{}
	}
	snapshot.Reasons = reasons
	return snapshot
}

func failedWorkerOpsSnapshot() workerHealthSnapshot {
	return workerHealthSnapshot{
		Phase:   string(workerPhaseFailed),
		Live:    false,
		Ready:   false,
		Reasons: []string{string(reasonRuntimeFailed)},
	}
}

func workerOpsPhaseAllowed(phase string) bool {
	switch phase {
	case string(workerPhaseStarting), string(workerPhaseServing),
		string(workerPhaseDraining), string(workerPhaseStopped),
		string(workerPhaseFailed):
		return true
	default:
		return false
	}
}

func workerOpsReasonAllowed(reason string) bool {
	switch reason {
	case string(reasonCompositionPending), string(reasonStartupProbePending),
		string(reasonStartupProbeFailed), string(reasonLoopsStarting),
		string(reasonDependencyProbeFailed), string(reasonDependencyProbeStale),
		string(reasonLoopPulsePending), string(reasonLoopPulseStale),
		string(reasonLoopExited), string(reasonRuntimeFailed), string(reasonShutdownTimeout),
		string(reasonResourceCloseFailed), string(reasonResourceCloseTimeout),
		string(reasonDraining), string(reasonStopped):
		return true
	default:
		return false
	}
}

func workerOpsHasOrigin(request *http.Request) bool {
	for name := range request.Header {
		if strings.EqualFold(name, "Origin") {
			return true
		}
	}
	return false
}

func workerOpsHasBody(request *http.Request) bool {
	if request.Body == nil {
		return false
	}
	// net/http represents a request with no body using http.NoBody. Comparing
	// interfaces directly could panic for a custom, non-comparable ReadCloser;
	// type comparison is sufficient and keeps malformed test/proxy requests
	// from smuggling a body with Content-Length: 0.
	return reflect.TypeOf(request.Body) != reflect.TypeOf(http.NoBody)
}

func setWorkerOpsHeaders(writer http.ResponseWriter) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
}

func (workerOpsHandler) write(writer http.ResponseWriter, statusCode int, status, phase string, reasons []string) {
	if reasons == nil {
		reasons = []string{}
	}
	writer.WriteHeader(statusCode)
	// Every response field is a closed enum or a reason already reduced to an
	// allowlisted code. Encoding failures cannot introduce configuration,
	// request, identity or raw dependency details into the response.
	_ = json.NewEncoder(writer).Encode(workerOpsResponse{
		Schema:  workerOpsSchema,
		Role:    workerOpsRole,
		Status:  status,
		Phase:   phase,
		Reasons: reasons,
	})
}
