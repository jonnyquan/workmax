//go:build desktop

package cloud_proxy

import (
	"net/http"
	"slices"
)

// Go Server route paths the sidecar talks to. Centralized here so:
//
//  1. Inline string concats can't drift across the package
//     (the P1.B.6 prereq bug was proxy.go pointing at the wrong
//     /api/workagent/agent/chat instead of /api/work-agent/chat/agent
//     — invisible to tests that stubbed against the same wrong URL).
//
//  2. A future URL-contract test can iterate these constants and
//     assert each one appears in the cloud's gin route registration,
//     catching path drift the same way the wire-shape tests catch
//     payload drift.
//
//  3. Reviewers scanning a PR can see at a glance whether a
//     cloud-side route rename needs sidecar follow-up — one file
//     to scan, not seven.
//
// Convention: name = CloudRoute<Domain><Action>. Keep paths
// literal (no fmt formatting); the test that walks these as a list
// needs them to be raw constants.
const (
	CloudRouteOAuthAuthorize = "/api/desktop/oauth/authorize"
	CloudRouteOAuthToken     = "/api/desktop/oauth/token"
	CloudRouteOAuthRevoke    = "/api/desktop/oauth/revoke"
	CloudRouteOAuthUserInfo  = "/api/desktop/oauth/userinfo"

	CloudRouteSyncThreads  = "/api/desktop/sync/threads"
	CloudRouteSyncMessages = "/api/desktop/sync/messages"

	// CloudRouteModelsList is the official model catalog: metadata only, no
	// keys and no endpoints. Same Desktop OAuth Bearer as /api/desktop/sync/*.
	CloudRouteModelsList = "/api/desktop/models"

	// CloudRouteModelGatewayAnthropic / CloudRouteModelGatewayOpenAI are the
	// official-model inference endpoints the sidecar's own loopback gateway
	// forwards to, so a local tool loop can run on an official model without
	// any cloud credential reaching this machine. Same Desktop OAuth Bearer as
	// /api/desktop/models; the request body's model must be a catalog modelId,
	// and entitlement is re-checked per request (403 when the membership does
	// not cover it).
	CloudRouteModelGatewayAnthropic = "/api/desktop/model-gateway/anthropic/v1/messages"
	CloudRouteModelGatewayOpenAI    = "/api/desktop/model-gateway/openai/v1/chat/completions"

	// CloudRouteModelGatewayAnthropicCountTokens is the second endpoint the
	// packaged claude CLI actually calls. Measured against CLI 2.1.226 with a
	// path-recording stub in the production launch shape: a turn whose tool
	// result is large enough to need sizing issues
	// POST /v1/messages/count_tokens?beta=true before it sends. Anthropic does
	// not bill it; the gateway still admits, limits and meters it like every
	// other call, because it still spends a platform credential's quota.
	CloudRouteModelGatewayAnthropicCountTokens = "/api/desktop/model-gateway/anthropic/v1/messages/count_tokens"

	CloudRouteSkillsList  = "/api/work-agent/skills"
	CloudRouteChatAgent   = "/api/work-agent/chat/agent"
	CloudRouteAgentThread = "/api/desktop/agent/threads/:uuid"

	CloudRouteVersion = "/api/desktop/version"

	CloudRouteLoginTransactionCreate   = "/api/v1/desktop/identity/login-transactions"
	CloudRouteLoginTransactionStatus   = "/api/v1/desktop/identity/login-transactions/:id"
	CloudRouteLoginTransactionPassword = "/api/v1/desktop/identity/login-transactions/:id/password"
	CloudRouteLoginTransactionExchange = "/api/v1/desktop/identity/login-transactions/:id/exchange"
)

// CloudRouteSpec is the sidecar's method-aware inventory of cloud requests.
// Path remains backed by the existing CloudRoute* constants so all callers
// keep their current API while the contract can detect a GET/POST mismatch.
type CloudRouteSpec struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	Path   string `json:"path"`
}

var currentCloudRouteSpecs = []CloudRouteSpec{
	newCloudRouteSpec("desktop.oauth.authorize", http.MethodGet, CloudRouteOAuthAuthorize),
	newCloudRouteSpec("desktop.oauth.token", http.MethodPost, CloudRouteOAuthToken),
	newCloudRouteSpec("desktop.oauth.revoke", http.MethodPost, CloudRouteOAuthRevoke),
	newCloudRouteSpec("desktop.oauth.userinfo", http.MethodGet, CloudRouteOAuthUserInfo),
	newCloudRouteSpec("desktop.sync.threads", http.MethodGet, CloudRouteSyncThreads),
	newCloudRouteSpec("desktop.sync.messages", http.MethodGet, CloudRouteSyncMessages),
	newCloudRouteSpec("desktop.models", http.MethodGet, CloudRouteModelsList),
	newCloudRouteSpec("desktop.model-gateway.anthropic", http.MethodPost, CloudRouteModelGatewayAnthropic),
	newCloudRouteSpec("desktop.model-gateway.anthropic.count-tokens", http.MethodPost, CloudRouteModelGatewayAnthropicCountTokens),
	newCloudRouteSpec("desktop.model-gateway.openai", http.MethodPost, CloudRouteModelGatewayOpenAI),
	newCloudRouteSpec("agent.skills", http.MethodGet, CloudRouteSkillsList),
	newCloudRouteSpec("agent.chat", http.MethodPost, CloudRouteChatAgent),
	newCloudRouteSpec("desktop.agent.thread-put", http.MethodPut, CloudRouteAgentThread),
	newCloudRouteSpec("desktop.version", http.MethodGet, CloudRouteVersion),
	newCloudRouteSpec("desktop.identity.login-transaction.create", http.MethodPost, CloudRouteLoginTransactionCreate),
	newCloudRouteSpec("desktop.identity.login-transaction.status", http.MethodGet, CloudRouteLoginTransactionStatus),
	newCloudRouteSpec("desktop.identity.login-transaction.password", http.MethodPost, CloudRouteLoginTransactionPassword),
	newCloudRouteSpec("desktop.identity.login-transaction.exchange", http.MethodPost, CloudRouteLoginTransactionExchange),
}

func newCloudRouteSpec(id string, method string, path string) CloudRouteSpec {
	return CloudRouteSpec{ID: id, Method: method, Path: path}
}

// CurrentCloudRouteSpecs returns a defensive copy of the current Method+Path
// contract. The private source table cannot be mutated by tests or callers.
func CurrentCloudRouteSpecs() []CloudRouteSpec {
	return slices.Clone(currentCloudRouteSpecs)
}

// AllCloudRoutes preserves the legacy path-only inventory for existing
// callers. New contract code should use CurrentCloudRouteSpecs.
var AllCloudRoutes = cloudRoutePaths(currentCloudRouteSpecs)

func cloudRoutePaths(specs []CloudRouteSpec) []string {
	paths := make([]string, len(specs))
	for i, spec := range specs {
		paths[i] = spec.Path
	}
	return paths
}
