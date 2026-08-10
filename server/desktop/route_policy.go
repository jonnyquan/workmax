//go:build desktop

package desktop

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"net/http"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"

	"server/desktop/auth"
)

// SidecarCredentialPolicy names the credential accepted by a loopback route.
// It is deliberately separate from cloud OAuth credentials: renderer requests
// authenticate to the sidecar with a per-process local token, while the
// sidecar owns any OAuth access token used for an upstream cloud request.
type SidecarCredentialPolicy string

const (
	SidecarCredentialLocalToken SidecarCredentialPolicy = "local-token"
	// SidecarCredentialGatewayToken is the model-gateway credential: an
	// in-memory, per-process random string the sidecar hands to the local
	// agent subprocesses it starts, and to nobody else.
	//
	// It exists because those callers cannot present the local token. They are
	// somebody else's binary (the claude CLI, pi) speaking a provider's wire
	// protocol, so the only credential slot they have is that provider's own
	// auth header — and the renderer's token has no business travelling inside
	// a subprocess environment. Routes with this credential are therefore
	// NEVER renderer-reachable: the shell's proxy refuses the path, and the
	// boundary manifest records that as policy rather than as an accident of
	// the renderer not knowing the URL.
	SidecarCredentialGatewayToken SidecarCredentialPolicy = "gateway-token"
)

// SidecarCredentialScheme names how a credential travels on the wire. It is
// empty for the local token (always X-Local-Token) and explicit for the
// gateway, where the header is dictated by the protocol the caller speaks.
type SidecarCredentialScheme string

const (
	SidecarCredentialSchemeAnthropicAPIKey SidecarCredentialScheme = "x-api-key"
	SidecarCredentialSchemeBearer          SidecarCredentialScheme = "authorization-bearer"
)

// SidecarRendererAccess records whether the renderer may reach a route. It is
// empty (allowed) for the renderer's own API and "forbidden" for the gateway,
// which is the one thing on this port the page must never be able to name.
type SidecarRendererAccess string

const (
	SidecarRendererForbidden SidecarRendererAccess = "forbidden"
)

// SidecarOriginPolicy controls browser Origin headers on loopback requests.
// The preload bridge performs the HTTP call from its privileged context and
// does not send Origin. Rejecting an Origin-bearing request prevents a normal
// web page from treating the loopback port as a browser API, even if a future
// change accidentally weakens the preload boundary.
type SidecarOriginPolicy string

const (
	SidecarOriginAbsent SidecarOriginPolicy = "absent"
)

// SidecarBodyPolicy describes whether a route accepts a request body.
type SidecarBodyPolicy string

const (
	SidecarBodyForbidden SidecarBodyPolicy = "forbidden"
	SidecarBodyOptional  SidecarBodyPolicy = "optional"
	SidecarBodyRequired  SidecarBodyPolicy = "required"
)

// SidecarRequestPolicy is enforced before a route handler runs. ContentTypes
// contains normalized media types without parameters; charset parameters are
// accepted when the underlying media type matches.
type SidecarRequestPolicy struct {
	Origin            SidecarOriginPolicy `json:"origin"`
	Body              SidecarBodyPolicy   `json:"body"`
	ContentTypes      []string            `json:"contentTypes"`
	MaxBodyBytes      int64               `json:"maxBodyBytes"`
	BodyTooLargeError string              `json:"bodyTooLargeError,omitempty"`
}

// SidecarRoutePolicy is the authoritative current request policy for one
// renderer-to-sidecar route. Method and Path are also used to register the Gin
// route, so the policy cannot silently drift from the runtime route table.
type SidecarRoutePolicy struct {
	ID               string                  `json:"id"`
	Method           string                  `json:"method"`
	Path             string                  `json:"path"`
	Credential       SidecarCredentialPolicy `json:"credential"`
	CredentialScheme SidecarCredentialScheme `json:"credentialScheme,omitempty"`
	RendererAccess   SidecarRendererAccess   `json:"rendererAccess,omitempty"`
	RequestPolicy    SidecarRequestPolicy    `json:"requestPolicy"`
}

const (
	maxOAuthControlBodyBytes   = 4 << 10
	maxAgentThreadPutBodyBytes = 4 << 10
	maxRendererLogBodyBytes    = 64 << 10
	maxManualSyncBodyBytes     = 4 << 10
	// maxModelSettingsBodyBytes also defined for handlers; keep policy in sync.
	maxModelSettingsPolicyBodyBytes = 8 << 10

	maxThreadFileUploadBodyBytes = 50 << 20 // 50 MiB, aligned with cloud workagent upload cap
)

// currentSidecarRoutePolicies is current-observed behavior hardened with an
// explicit perimeter. POST control routes continue to accept the small JSON
// object bodies tolerated by legacy callers even when their handlers do not
// consume the body.
var currentSidecarRoutePolicies = []SidecarRoutePolicy{
	newCurrentSidecarRoutePolicy("health.get", http.MethodGet, "/health", SidecarBodyForbidden, 0),
	newCurrentSidecarRoutePolicy("auth.status", http.MethodGet, "/auth/status", SidecarBodyForbidden, 0),
	newCurrentSidecarRoutePolicy("auth.userinfo", http.MethodGet, "/auth/userinfo", SidecarBodyForbidden, 0),
	newCurrentSidecarRoutePolicy("auth.start", http.MethodPost, "/auth/start", SidecarBodyOptional, maxOAuthControlBodyBytes, "application/x-www-form-urlencoded", "application/json"),
	newCurrentSidecarRoutePolicy("auth.login-transaction.begin", http.MethodPost, "/auth/login-transaction", SidecarBodyForbidden, 0),
	newCurrentSidecarRoutePolicy("auth.login-transaction.status", http.MethodGet, "/auth/login-transaction", SidecarBodyForbidden, 0),
	newCurrentSidecarRoutePolicy("auth.login-transaction.password", http.MethodPost, "/auth/login-transaction/password", SidecarBodyRequired, maxLoginTransactionPasswordBodyBytes, "application/json"),
	newCurrentSidecarRoutePolicy("auth.login-transaction.cancel", http.MethodDelete, "/auth/login-transaction", SidecarBodyForbidden, 0),
	newCurrentSidecarRoutePolicy("auth.logout", http.MethodPost, "/auth/logout", SidecarBodyOptional, maxOAuthControlBodyBytes, "application/json"),
	newCurrentSidecarRoutePolicy("agent.chat", http.MethodPost, "/agent/chat", SidecarBodyRequired, maxAgentChatBodyBytes, "application/json"),
	newCurrentSidecarRoutePolicy("agent.turns-recoverable", http.MethodGet, "/agent/turns/recoverable", SidecarBodyForbidden, 0),
	newCurrentSidecarRoutePolicy("agent.turn-replay", http.MethodPost, "/agent/turns/:uuid/replay", SidecarBodyForbidden, 0),
	newCurrentSidecarRoutePolicy("agent.turn-cancel", http.MethodPost, "/agent/turns/:uuid/cancel", SidecarBodyForbidden, 0),
	newCurrentSidecarRoutePolicy("agent.turn-approve", http.MethodPost, "/agent/turns/:uuid/approve", SidecarBodyRequired, maxAgentApproveBodyBytes, "application/json"),
	newCurrentSidecarRoutePolicy("agent.thread-put", http.MethodPut, "/agent/threads/:uuid", SidecarBodyRequired, maxAgentThreadPutBodyBytes, "application/json"),
	newCurrentSidecarRoutePolicy("agent.thread-delete", http.MethodDelete, "/agent/threads/:uuid", SidecarBodyForbidden, 0),
	newCurrentSidecarRoutePolicy("agent.thread-rename", http.MethodPatch, "/agent/threads/:uuid", SidecarBodyRequired, maxAgentThreadRenameBodyBytes, "application/json"),
	newCurrentSidecarRoutePolicy("agent.skills.catalog", http.MethodGet, "/agent/skills/catalog", SidecarBodyForbidden, 0),
	newCurrentSidecarRoutePolicy("agent.skills.modes", http.MethodGet, "/agent/skills/modes", SidecarBodyForbidden, 0),
	newCurrentSidecarRoutePolicy("local.accounts.list", http.MethodGet, "/local/accounts", SidecarBodyForbidden, 0),
	newCurrentSidecarRoutePolicy("local.accounts.create", http.MethodPost, "/local/accounts", SidecarBodyRequired, maxLocalAccountBodyBytes, "application/json"),
	newCurrentSidecarRoutePolicy("local.accounts.select", http.MethodPost, "/local/accounts/:id/select", SidecarBodyForbidden, 0),
	newCurrentSidecarRoutePolicy("local.accounts.rename", http.MethodPatch, "/local/accounts/:id", SidecarBodyRequired, maxLocalAccountBodyBytes, "application/json"),
	newCurrentSidecarRoutePolicy("local.accounts.delete", http.MethodDelete, "/local/accounts/:id", SidecarBodyForbidden, 0),
	newCurrentSidecarRoutePolicy("agent.threads", http.MethodGet, "/agent/threads", SidecarBodyForbidden, 0),
	newCurrentSidecarRoutePolicy("agent.thread-messages", http.MethodGet, "/agent/threads/:uuid/messages", SidecarBodyForbidden, 0),
	newCurrentSidecarRoutePolicy("system.network-state", http.MethodGet, "/system/network-state", SidecarBodyForbidden, 0),
	newCurrentSidecarRoutePolicy("system.log", http.MethodPost, "/system/log", SidecarBodyOptional, maxRendererLogBodyBytes, "application/json"),
	newCurrentSidecarRoutePolicy("system.diagnostics", http.MethodGet, "/system/diagnostics", SidecarBodyForbidden, 0),
	newCurrentSidecarRoutePolicy("system.server-version", http.MethodGet, "/system/server-version", SidecarBodyForbidden, 0),
	newCurrentSidecarRoutePolicy("system.trigger-sync", http.MethodPost, "/system/trigger-sync", SidecarBodyOptional, maxManualSyncBodyBytes, "application/json"),
	newCurrentSidecarRoutePolicy("settings.model-route.get", http.MethodGet, "/settings/model-route", SidecarBodyForbidden, 0),
	newCurrentSidecarRoutePolicy("settings.model-route.put", http.MethodPut, "/settings/model-route", SidecarBodyRequired, maxModelSettingsPolicyBodyBytes, "application/json"),
	newCurrentSidecarRoutePolicy("agent.thread-file-upload", http.MethodPost, "/agent/threads/:uuid/files", SidecarBodyRequired, maxThreadFileUploadBodyBytes, "multipart/form-data"),
	newCurrentSidecarRoutePolicy("agent.thread-file-list", http.MethodGet, "/agent/threads/:uuid/files", SidecarBodyForbidden, 0),
	newCurrentSidecarRoutePolicy("agent.thread-workspace-list", http.MethodGet, "/agent/threads/:uuid/workspace", SidecarBodyForbidden, 0),
	newCurrentSidecarRoutePolicy("agent.thread-workspace-reveal", http.MethodPost, "/agent/threads/:uuid/workspace/reveal", SidecarBodyForbidden, 0),
	newCurrentSidecarRoutePolicy("agent.thread-export", http.MethodPost, "/agent/threads/:uuid/export", SidecarBodyForbidden, 0),
	newCurrentSidecarRoutePolicy("agent.search", http.MethodGet, "/agent/search", SidecarBodyForbidden, 0),
	newCurrentSidecarRoutePolicy("agent.thread-pin", http.MethodPost, "/agent/threads/:uuid/pin", SidecarBodyForbidden, 0),
	newCurrentSidecarRoutePolicy("agent.thread-unpin", http.MethodDelete, "/agent/threads/:uuid/pin", SidecarBodyForbidden, 0),
	newCurrentSidecarRoutePolicy("agent.thread-cloud-sync", http.MethodPut, "/agent/threads/:uuid/cloud-sync", SidecarBodyRequired, maxThreadCloudSyncBodyBytes, "application/json"),
	newCurrentSidecarRoutePolicy("settings.model-catalog.get", http.MethodGet, "/settings/model-catalog", SidecarBodyForbidden, 0),

	// The model gateway. Two protocols, two path spellings each, because the
	// clients disagree about whether the version segment belongs to the base
	// URL they were configured with: the claude CLI appends /v1/messages to
	// ANTHROPIC_BASE_URL while this repo's own L1 adapter appends /messages,
	// and the OpenAI-shaped clients split the same way. Registering both
	// spellings lets one base string serve every caller — the alternative is a
	// per-engine URL rule that is wrong the first time a client changes its
	// mind, and wrong silently, as a 404 in the middle of a turn.
	newModelGatewayRoutePolicy(
		"model-gateway.anthropic.messages",
		"/model-gateway/anthropic/v1/messages",
		SidecarCredentialSchemeAnthropicAPIKey,
	),
	newModelGatewayRoutePolicy(
		"model-gateway.anthropic.messages-unversioned",
		"/model-gateway/anthropic/messages",
		SidecarCredentialSchemeAnthropicAPIKey,
	),
	newModelGatewayRoutePolicy(
		"model-gateway.openai.chat-completions",
		"/model-gateway/openai/v1/chat/completions",
		SidecarCredentialSchemeBearer,
	),
	newModelGatewayRoutePolicy(
		"model-gateway.openai.chat-completions-unversioned",
		"/model-gateway/openai/chat/completions",
		SidecarCredentialSchemeBearer,
	),
}

func newCurrentSidecarRoutePolicy(
	id string,
	method string,
	path string,
	body SidecarBodyPolicy,
	maxBodyBytes int64,
	contentTypes ...string,
) SidecarRoutePolicy {
	bodyTooLargeError := ""
	if body != SidecarBodyForbidden {
		bodyTooLargeError = "request body too large"
	}
	switch id {
	case "agent.chat":
		bodyTooLargeError = "chat request body too large"
	case "agent.thread-put":
		bodyTooLargeError = "thread request body too large"
	case "system.log":
		bodyTooLargeError = "renderer log body too large"
	case "settings.model-route.put":
		bodyTooLargeError = "model settings body too large"
	case "agent.thread-cloud-sync":
		bodyTooLargeError = "cloud sync request body too large"
	}
	return SidecarRoutePolicy{
		ID:         id,
		Method:     method,
		Path:       path,
		Credential: SidecarCredentialLocalToken,
		RequestPolicy: SidecarRequestPolicy{
			Origin:            SidecarOriginAbsent,
			Body:              body,
			ContentTypes:      slices.Clone(contentTypes),
			MaxBodyBytes:      maxBodyBytes,
			BodyTooLargeError: bodyTooLargeError,
		},
	}
}

// newModelGatewayRoutePolicy declares one gateway route: gateway-token
// credential, renderer forbidden, a JSON body bounded by the tool-loop cap.
func newModelGatewayRoutePolicy(id, path string, scheme SidecarCredentialScheme) SidecarRoutePolicy {
	return SidecarRoutePolicy{
		ID:               id,
		Method:           http.MethodPost,
		Path:             path,
		Credential:       SidecarCredentialGatewayToken,
		CredentialScheme: scheme,
		RendererAccess:   SidecarRendererForbidden,
		RequestPolicy: SidecarRequestPolicy{
			Origin:            SidecarOriginAbsent,
			Body:              SidecarBodyRequired,
			ContentTypes:      []string{"application/json"},
			MaxBodyBytes:      maxModelGatewayBodyBytes,
			BodyTooLargeError: "model gateway request body too large",
		},
	}
}

// modelGatewayCredentialPaths is the exact set of paths the whole-port local
// token perimeter does not apply to.
//
// It is built from the policy table rather than written out, so a gateway
// route that is added without a credential declaration cannot become an
// unauthenticated path by omission. The exemption is by exact path: anything
// else under /model-gateway/ still meets the local token first and is refused
// there, which keeps the perimeter's "unknown paths need a credential"
// property intact everywhere it was ever true.
func modelGatewayCredentialPaths(policies []SidecarRoutePolicy) map[string]struct{} {
	paths := make(map[string]struct{})
	for _, policy := range policies {
		if policy.Credential == SidecarCredentialGatewayToken {
			paths[policy.Path] = struct{}{}
		}
	}
	return paths
}

// CurrentSidecarRoutePolicies returns a defensive copy for diagnostics,
// contract tests, and future generated bridge clients.
func CurrentSidecarRoutePolicies() []SidecarRoutePolicy {
	out := make([]SidecarRoutePolicy, len(currentSidecarRoutePolicies))
	copy(out, currentSidecarRoutePolicies)
	for i := range out {
		out[i].RequestPolicy.ContentTypes = slices.Clone(out[i].RequestPolicy.ContentTypes)
	}
	return out
}

func validateSidecarRoutePolicies(policies []SidecarRoutePolicy) error {
	seenIDs := make(map[string]struct{}, len(policies))
	seenRoutes := make(map[string]struct{}, len(policies))
	for _, policy := range policies {
		if policy.ID == "" || policy.Method == "" || policy.Path == "" {
			return fmt.Errorf("sidecar route policy contains an empty identity")
		}
		if _, exists := seenIDs[policy.ID]; exists {
			return fmt.Errorf("duplicate sidecar route policy id %q", policy.ID)
		}
		seenIDs[policy.ID] = struct{}{}

		routeKey := policy.Method + " " + policy.Path
		if _, exists := seenRoutes[routeKey]; exists {
			return fmt.Errorf("duplicate sidecar route policy %q", routeKey)
		}
		seenRoutes[routeKey] = struct{}{}

		switch policy.Method {
		case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		default:
			return fmt.Errorf("sidecar route %q uses unsupported method %q", policy.ID, policy.Method)
		}
		if !strings.HasPrefix(policy.Path, "/") {
			return fmt.Errorf("sidecar route %q path must be absolute", policy.ID)
		}
		switch policy.Credential {
		case SidecarCredentialLocalToken:
			if policy.CredentialScheme != "" {
				return fmt.Errorf("sidecar route %q must not redeclare the local token scheme", policy.ID)
			}
			if policy.RendererAccess != "" {
				return fmt.Errorf("sidecar route %q is the renderer's own API and must not restrict it", policy.ID)
			}
		case SidecarCredentialGatewayToken:
			switch policy.CredentialScheme {
			case SidecarCredentialSchemeAnthropicAPIKey, SidecarCredentialSchemeBearer:
			default:
				return fmt.Errorf("sidecar route %q has unsupported credential scheme %q", policy.ID, policy.CredentialScheme)
			}
			// A gateway route the renderer could call would be a way for the
			// page to spend the account's membership without any of the
			// checks the agent routes apply. Declaring it here is what lets
			// the boundary manifest and the shell's proxy enforce it.
			if policy.RendererAccess != SidecarRendererForbidden {
				return fmt.Errorf("sidecar route %q must be forbidden to the renderer", policy.ID)
			}
			if !strings.HasPrefix(policy.Path, "/model-gateway/") {
				return fmt.Errorf("sidecar route %q must live under /model-gateway/", policy.ID)
			}
		default:
			return fmt.Errorf("sidecar route %q has unsupported credential policy %q", policy.ID, policy.Credential)
		}
		if policy.RequestPolicy.Origin != SidecarOriginAbsent {
			return fmt.Errorf("sidecar route %q has unsupported origin policy %q", policy.ID, policy.RequestPolicy.Origin)
		}

		switch policy.RequestPolicy.Body {
		case SidecarBodyForbidden:
			if policy.RequestPolicy.MaxBodyBytes != 0 ||
				len(policy.RequestPolicy.ContentTypes) != 0 ||
				policy.RequestPolicy.BodyTooLargeError != "" {
				return fmt.Errorf("sidecar route %q forbids a body but declares body metadata", policy.ID)
			}
		case SidecarBodyOptional, SidecarBodyRequired:
			if policy.RequestPolicy.MaxBodyBytes <= 0 {
				return fmt.Errorf("sidecar route %q must declare a positive body limit", policy.ID)
			}
			if len(policy.RequestPolicy.ContentTypes) == 0 {
				return fmt.Errorf("sidecar route %q must declare a content type", policy.ID)
			}
			if policy.RequestPolicy.BodyTooLargeError == "" {
				return fmt.Errorf("sidecar route %q must declare an oversize error", policy.ID)
			}
		default:
			return fmt.Errorf("sidecar route %q has unsupported body policy %q", policy.ID, policy.RequestPolicy.Body)
		}

		seenContentTypes := make(map[string]struct{}, len(policy.RequestPolicy.ContentTypes))
		for _, contentType := range policy.RequestPolicy.ContentTypes {
			mediaType, params, err := mime.ParseMediaType(contentType)
			if err != nil || mediaType != contentType || len(params) != 0 {
				return fmt.Errorf("sidecar route %q has non-canonical content type %q", policy.ID, contentType)
			}
			if _, exists := seenContentTypes[contentType]; exists {
				return fmt.Errorf("sidecar route %q repeats content type %q", policy.ID, contentType)
			}
			seenContentTypes[contentType] = struct{}{}
		}
	}
	return nil
}

func registerCurrentSidecarRoutes(router *gin.Engine, server *Server, localToken string) error {
	policies := CurrentSidecarRoutePolicies()
	if err := validateSidecarRoutePolicies(policies); err != nil {
		return err
	}
	for _, policy := range policies {
		handler, ok := server.sidecarHandler(policy.ID)
		if !ok {
			return fmt.Errorf("sidecar route %q has no handler", policy.ID)
		}
		handlers, err := sidecarRouteHandlers(policy, localToken, server.cfg.ModelGateway, handler)
		if err != nil {
			return err
		}
		router.Handle(policy.Method, policy.Path, handlers...)
	}
	return nil
}

// registerAuthenticatedNotFound makes the post-perimeter result explicit.
// The whole-port token middleware rejects unauthenticated unknown paths and
// wrong methods first; authenticated callers reach this normal 404.
func registerAuthenticatedNotFound(router *gin.Engine) {
	router.NoRoute(func(c *gin.Context) { c.Status(http.StatusNotFound) })
}

func (s *Server) sidecarHandler(routeID string) (gin.HandlerFunc, bool) {
	switch routeID {
	case "health.get":
		return s.handleHealth, true
	case "auth.status":
		return s.handleAuthStatus, true
	case "auth.userinfo":
		return s.handleAuthUserInfo, true
	case "auth.start":
		return s.handleAuthStart, true
	case "auth.login-transaction.begin":
		return s.handleLoginTransactionBegin, true
	case "auth.login-transaction.status":
		return s.handleLoginTransactionStatus, true
	case "auth.login-transaction.password":
		return s.handleLoginTransactionPassword, true
	case "auth.login-transaction.cancel":
		return s.handleLoginTransactionCancel, true
	case "auth.logout":
		return s.handleAuthLogout, true
	case "agent.chat":
		return s.handleAgentChat, true
	case "agent.turns-recoverable":
		return s.handleListRecoverableAgentTurns, true
	case "agent.turn-replay":
		return s.handleReplayAgentTurn, true
	case "agent.turn-cancel":
		return s.handleCancelAgentTurn, true
	case "agent.turn-approve":
		return s.handleApproveAgentTurn, true
	case "agent.thread-put":
		return s.handlePutAgentThread, true
	case "agent.thread-delete":
		return s.handleDeleteAgentThread, true
	case "agent.thread-rename":
		return s.handleRenameAgentThread, true
	case "agent.thread-file-list":
		return s.handleListThreadFiles, true
	case "agent.thread-workspace-list":
		return s.handleListWorkspaceFiles, true
	case "agent.thread-workspace-reveal":
		return s.handleRevealWorkspace, true
	case "agent.thread-export":
		return s.handleExportThread, true
	case "agent.search":
		return s.handleSearchMessages, true
	case "agent.thread-pin":
		return s.handlePinThread, true
	case "agent.thread-unpin":
		return s.handleUnpinThread, true
	case "agent.thread-cloud-sync":
		return s.handleSetThreadCloudSync, true
	case "settings.model-catalog.get":
		return s.handleModelCatalog, true
	case "model-gateway.anthropic.messages", "model-gateway.anthropic.messages-unversioned":
		return s.handleModelGatewayAnthropicMessages, true
	case "model-gateway.openai.chat-completions", "model-gateway.openai.chat-completions-unversioned":
		return s.handleModelGatewayOpenAIChatCompletions, true
	case "agent.thread-file-upload":
		return s.handleUploadThreadFile, true
	case "agent.skills.catalog":
		return s.handleSkillsCatalog, true
	case "agent.skills.modes":
		return s.handleAgentModes, true
	case "local.accounts.list":
		return s.handleListLocalAccounts, true
	case "local.accounts.create":
		return s.handleCreateLocalAccount, true
	case "local.accounts.select":
		return s.handleSelectLocalAccount, true
	case "local.accounts.rename":
		return s.handleRenameLocalAccount, true
	case "local.accounts.delete":
		return s.handleDeleteLocalAccount, true
	case "agent.threads":
		return s.handleListThreads, true
	case "agent.thread-messages":
		return s.handleListMessages, true
	case "system.network-state":
		return s.handleNetworkStateSSE, true
	case "system.log":
		return s.handleRendererLog, true
	case "system.diagnostics":
		return s.handleDiagnostics, true
	case "system.server-version":
		return s.handleServerVersion, true
	case "system.trigger-sync":
		return s.handleTriggerSync, true
	case "settings.model-route.get":
		return s.handleGetModelRoute, true
	case "settings.model-route.put":
		return s.handlePutModelRoute, true
	default:
		return nil, false
	}
}

func sidecarRouteHandlers(
	policy SidecarRoutePolicy,
	localToken string,
	gateway *ModelGateway,
	handler gin.HandlerFunc,
) ([]gin.HandlerFunc, error) {
	var credentialHandler gin.HandlerFunc
	switch policy.Credential {
	case SidecarCredentialLocalToken:
		credentialHandler = auth.RequireLocalToken(localToken)
	case SidecarCredentialGatewayToken:
		credentialHandler = requireModelGatewayToken(policy, gateway)
	default:
		return nil, fmt.Errorf("sidecar route %q has unsupported credential policy %q", policy.ID, policy.Credential)
	}
	return []gin.HandlerFunc{
		credentialHandler,
		requireSidecarRequestPolicy(policy),
		handler,
	}, nil
}

// requireModelGatewayToken checks the loopback credential in the header slot
// the caller's protocol dictates, and in that slot only.
//
// Accepting either header on either route would be friendlier and wrong: the
// point of naming the scheme in the policy is that "which credential does this
// route take, and where does it live" has one answer a reviewer can read. A
// gateway that has not been wired (or has been shut down) refuses everything —
// the failure to make available must never be a failure to authenticate.
func requireModelGatewayToken(policy SidecarRoutePolicy, gateway *ModelGateway) gin.HandlerFunc {
	scheme := policy.CredentialScheme
	protocol := modelGatewayAnthropic
	if scheme == SidecarCredentialSchemeBearer {
		protocol = modelGatewayOpenAI
	}
	return func(c *gin.Context) {
		if gateway == nil {
			writeModelGatewayError(c, protocol, http.StatusServiceUnavailable,
				modelGatewayErrorAPIError, modelGatewayUnavailableMessage)
			return
		}
		var presented string
		switch scheme {
		case SidecarCredentialSchemeAnthropicAPIKey:
			values := c.Request.Header.Values("X-Api-Key")
			if len(values) == 1 {
				presented = values[0]
			}
		case SidecarCredentialSchemeBearer:
			values := c.Request.Header.Values("Authorization")
			if len(values) == 1 {
				presented = strings.TrimPrefix(values[0], "Bearer ")
				if presented == values[0] {
					presented = ""
				}
			}
		}
		if !gateway.Matches(presented) {
			writeModelGatewayError(c, protocol, http.StatusUnauthorized,
				modelGatewayErrorAuthentication, modelGatewayCredentialMessage)
			return
		}
		c.Next()
	}
}

func requireSidecarRequestPolicy(policy SidecarRoutePolicy) gin.HandlerFunc {
	return func(c *gin.Context) {
		if len(c.Request.Header.Values("Origin")) != 0 {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "browser Origin is not allowed on sidecar routes"})
			return
		}

		requestPolicy := policy.RequestPolicy
		if requestPolicy.Body == SidecarBodyForbidden {
			if requestHasBody(c.Request) {
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "request body is not allowed"})
				return
			}
			c.Next()
			return
		}

		bodyReader := c.Request.Body
		if bodyReader == nil {
			bodyReader = http.NoBody
		}
		body, err := io.ReadAll(io.LimitReader(bodyReader, requestPolicy.MaxBodyBytes+1))
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "could not read request body"})
			return
		}
		// The password-login route is intentionally buffered so its byte limit
		// can be enforced before the handler runs. Clear that backing array on
		// every exit once downstream processing is finished; otherwise a
		// credential body would remain reachable through this middleware's
		// bytes.Reader longer than necessary.
		defer clear(body)
		if int64(len(body)) > requestPolicy.MaxBodyBytes {
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"error": requestPolicy.BodyTooLargeError})
			return
		}
		if requestPolicy.Body == SidecarBodyRequired && len(body) == 0 {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "request body is required"})
			return
		}
		if len(body) != 0 || c.GetHeader("Content-Type") != "" {
			if !sidecarContentTypeAllowed(c.Request.Header.Values("Content-Type"), requestPolicy.ContentTypes) {
				c.AbortWithStatusJSON(http.StatusUnsupportedMediaType, gin.H{"error": "unsupported Content-Type"})
				return
			}
		}

		if c.Request.Body != nil {
			_ = c.Request.Body.Close()
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		c.Request.ContentLength = int64(len(body))
		c.Next()
	}
}

func requestHasBody(request *http.Request) bool {
	if request.ContentLength != 0 {
		return true
	}
	return len(request.TransferEncoding) != 0
}

func sidecarContentTypeAllowed(values []string, allowed []string) bool {
	if len(values) != 1 {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(values[0])
	if err != nil {
		return false
	}
	return slices.Contains(allowed, strings.ToLower(mediaType))
}
