package agentv1api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	agentv1api "server/api/agent/v1"
	"server/initialize"
)

func TestInitializeRouterLeavesAgentV1CandidateUnmounted(t *testing.T) {
	router := initialize.Routers()
	registered := make(map[string]struct{}, len(router.Routes()))
	for _, route := range router.Routes() {
		registered[route.Method+" "+route.Path] = struct{}{}
	}

	for _, candidate := range agentv1api.CandidateRoutes() {
		if _, mounted := registered[candidate.Method+" "+candidate.Path]; mounted {
			t.Fatalf("candidate route unexpectedly mounted by initialize: %s %s", candidate.Method, candidate.Path)
		}
		requestPath := strings.NewReplacer(
			":threadId", "thread_unmounted",
			":turnId", "turn_unmounted",
		).Replace(candidate.Path)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(candidate.Method, requestPath, nil))
		// initialize's established NoRoute wire contract uses HTTP 200 with
		// a JSON business code of 404. Route-table absence above is what
		// proves this request did not reach a candidate handler.
		var envelope struct {
			Code int `json:"code"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &envelope) != nil || envelope.Code != http.StatusNotFound {
			t.Fatalf("unmounted %s %s response = %d %s, want initialize NoRoute code 404", candidate.Method, requestPath, response.Code, response.Body.String())
		}
	}
}
