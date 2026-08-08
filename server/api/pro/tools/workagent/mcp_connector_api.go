package workagent

// mcp_connector_api.go — Phase B4a. REST handlers for the
// per-user MCP connector registry.
//
// Endpoints (mounted under /api/work-agent/mcp-connectors):
//
//   GET    /                     list connectors (metadata only)
//   POST   /                     create a connector
//   GET    /:id                  get one (metadata + secret key names only)
//   PUT    /:id                  full update
//   PATCH  /:id/enabled          toggle enabled bit
//   DELETE /:id                  soft delete
//   POST   /:id/test             live-validate persisted remote connector
//
// Wire-shape policy: list + detail scrub env / headers VALUES
// (returns only key names so the UI can render "3 env vars set"
// without re-exposing secrets). Update accepts plaintext replacement
// values; omitted env / headers keep the existing encrypted values.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"server/globals"
	"server/model"
	"server/service/mcp_connector"
	"server/utils"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	mcpConnectorMaxBodyBytes  = 128 * 1024
	mcpConnectorMaxMapEntries = 64
	mcpConnectorMaxMapBytes   = 32 * 1024
	mcpConnectorMaxKeyBytes   = 128
	mcpConnectorMaxNameBytes  = 120
	mcpConnectorMaxURLBytes   = 2048
	mcpConnectorMaxCmdBytes   = 255
)

// ListMCPConnectors — GET /api/work-agent/mcp-connectors
//
// Returns metadata-only summaries (no secret values) so a list
// page can render without re-exposing plaintext. envKeys /
// headerKeys carry the KEY NAMES (not values) so the UI can
// show "DEBUG_LOG_LEVEL, LINEAR_API_KEY set" without revealing
// the API key itself.
func (api *AIChatApiNew) ListMCPConnectors(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	limit := parseQueryIntDefault(c, "limit", 50)
	offset := parseQueryIntDefault(c, "offset", 0)

	rows, err := mcp_connector.Default().ListForOwner(uid, limit, offset)
	if err != nil {
		globals.Error(fmt.Sprintf("[MCPConnector API] list failed uid=%d: %v", uid, err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list connectors"})
		return
	}

	items := make([]mcpConnectorSummary, 0, len(rows))
	for i := range rows {
		items = append(items, summariseMCPConnector(&rows[i]))
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"items": items,
		"pagination": gin.H{
			"limit":   limit,
			"offset":  offset,
			"hasMore": len(items) == limit,
		},
	}})
}

// GetMCPConnector — GET /api/work-agent/mcp-connectors/:id
//
// Returns connector metadata + args. Secret env / header VALUES are
// never echoed; envKeys / headerKeys show what is configured.
// Cross-tenant returns 404 with no existence oracle.
func (api *AIChatApiNew) GetMCPConnector(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	id, err := parsePathUint(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid id"})
		return
	}

	row, err := mcp_connector.Default().LoadByIDForOwner(id, uid)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Connector not found"})
			return
		}
		globals.Error(fmt.Sprintf("[MCPConnector API] get failed uid=%d id=%d: %v", uid, id, err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load connector"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": detailedMCPConnector(row)})
}

// MCPConnectorCreateRequest is the body shape for POST /. Args /
// env / headers come in as plain JSON objects; the model
// converts on persist.
type MCPConnectorCreateRequest struct {
	Name      string                 `json:"name"`
	Transport string                 `json:"transport"`
	Command   string                 `json:"command,omitempty"`
	Args      map[string]interface{} `json:"args,omitempty"`
	Env       map[string]interface{} `json:"env,omitempty"`
	URL       string                 `json:"url,omitempty"`
	Headers   map[string]interface{} `json:"headers,omitempty"`
	Enabled   *bool                  `json:"enabled,omitempty"`
}

// CreateMCPConnector — POST /api/work-agent/mcp-connectors
func (api *AIChatApiNew) CreateMCPConnector(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var req MCPConnectorCreateRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, mcpConnectorMaxBodyBytes)
	if err := c.ShouldBindJSON(&req); err != nil {
		if errors.Is(err, io.EOF) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}
	if err := validateMCPConnectorCreateRequest(req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	row := &model.MCPConnector{
		UID:       int(uid),
		Name:      req.Name,
		Transport: req.Transport,
		Command:   req.Command,
		Args:      model.JSONMap(req.Args),
		Env:       model.EncryptedJSONMap(req.Env),
		URL:       req.URL,
		Headers:   model.EncryptedJSONMap(req.Headers),
		Enabled:   true, // default ON; explicit false below
	}
	if req.Enabled != nil {
		row.Enabled = *req.Enabled
	}

	if err := mcp_connector.Default().Create(row); err != nil {
		// Validation errors (empty name / transport, etc.) are
		// 400; only real DB failures land at 500. The repo
		// returns the validation messages as plain errors with
		// no sentinel; we discriminate by message-prefix here
		// rather than introducing 5 different sentinels.
		if isValidationError(err) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		globals.Error(fmt.Sprintf("[MCPConnector API] create failed uid=%d: %v", uid, err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create connector"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": detailedMCPConnector(row)})
}

// MCPConnectorUpdateRequest mirrors the repo's UpdatePatch but
// in JSON-binding-friendly shape (pointers for "field omitted
// vs explicitly set to zero" disambiguation). Args / Env /
// Headers as map[string]interface{} so the body can clear them
// with `null` (binding sets the pointer to nil) but a present
// empty {} also clears.
type MCPConnectorUpdateRequest struct {
	Name      *string                 `json:"name,omitempty"`
	Transport *string                 `json:"transport,omitempty"`
	Command   *string                 `json:"command,omitempty"`
	Args      *map[string]interface{} `json:"args,omitempty"`
	Env       *map[string]interface{} `json:"env,omitempty"`
	URL       *string                 `json:"url,omitempty"`
	Headers   *map[string]interface{} `json:"headers,omitempty"`
	Enabled   *bool                   `json:"enabled,omitempty"`
}

// UpdateMCPConnector — PUT /api/work-agent/mcp-connectors/:id
func (api *AIChatApiNew) UpdateMCPConnector(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	id, err := parsePathUint(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid id"})
		return
	}
	var req MCPConnectorUpdateRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, mcpConnectorMaxBodyBytes)
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}
	if err := validateMCPConnectorUpdateRequest(req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	patch := mcp_connector.UpdatePatch{
		Name:      req.Name,
		Transport: req.Transport,
		Command:   req.Command,
		URL:       req.URL,
		Enabled:   req.Enabled,
	}
	if req.Args != nil {
		// Round-trip via JSONMap so the type checker accepts
		// the *map[string]any → *JSONMap cast.
		jm := model.JSONMap(*req.Args)
		patch.Args = &jm
	}
	if req.Env != nil {
		em := model.EncryptedJSONMap(*req.Env)
		patch.Env = &em
	}
	if req.Headers != nil {
		em := model.EncryptedJSONMap(*req.Headers)
		patch.Headers = &em
	}

	if err := mcp_connector.Default().Update(id, uid, patch); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Connector not found"})
			return
		}
		if isValidationError(err) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		globals.Error(fmt.Sprintf("[MCPConnector API] update failed uid=%d id=%d: %v", uid, id, err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update connector"})
		return
	}

	// Post-write disambiguator — return the fresh row so the
	// frontend reconciles its local copy without a second
	// fetch (mirrors the asset-library handler shape).
	fresh, err := mcp_connector.Default().LoadByIDForOwner(id, uid)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"data": gin.H{"updated": true}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": detailedMCPConnector(fresh)})
}

// MCPConnectorEnabledRequest — body for the toggle endpoint.
type MCPConnectorEnabledRequest struct {
	Enabled bool `json:"enabled"`
}

// SetMCPConnectorEnabled — PATCH /api/work-agent/mcp-connectors/:id/enabled
//
// One-bit toggle. Cheaper than the full Update path; the UI
// fires this on the per-row switch component without rebuilding
// a full payload.
func (api *AIChatApiNew) SetMCPConnectorEnabled(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	id, err := parsePathUint(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid id"})
		return
	}
	var req MCPConnectorEnabledRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, workAgentSmallJSONMaxBytes)
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}

	if err := mcp_connector.Default().SetEnabled(id, uid, req.Enabled); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Connector not found"})
			return
		}
		globals.Error(fmt.Sprintf("[MCPConnector API] set-enabled failed uid=%d id=%d: %v", uid, id, err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update connector"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"enabled": req.Enabled}})
}

// DeleteMCPConnector — DELETE /api/work-agent/mcp-connectors/:id
func (api *AIChatApiNew) DeleteMCPConnector(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	id, err := parsePathUint(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid id"})
		return
	}

	if err := mcp_connector.Default().SoftDelete(id, uid); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Connector not found"})
			return
		}
		globals.Error(fmt.Sprintf("[MCPConnector API] delete failed uid=%d id=%d: %v", uid, id, err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete connector"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"deleted": true}})
}

// ---- DTO + helpers ----

// mcpConnectorSummary is the list-view shape — no secret values,
// just metadata + key-name hints so the UI can render counts
// without re-exposing plaintext.
type mcpConnectorSummary struct {
	ID         uint     `json:"id"`
	Name       string   `json:"name"`
	Transport  string   `json:"transport"`
	Command    string   `json:"command,omitempty"`
	URL        string   `json:"url,omitempty"`
	Enabled    bool     `json:"enabled"`
	EnvKeys    []string `json:"envKeys,omitempty"`
	HeaderKeys []string `json:"headerKeys,omitempty"`
	UpdatedAt  string   `json:"updatedAt"`
}

// mcpConnectorDetail extends the summary with non-secret args. Env
// and header values are intentionally omitted; summary key lists are
// enough for edit screens to show what exists without re-exposing values.
type mcpConnectorDetail struct {
	mcpConnectorSummary
	Args map[string]interface{} `json:"args,omitempty"`
}

func summariseMCPConnector(c *model.MCPConnector) mcpConnectorSummary {
	return mcpConnectorSummary{
		ID:         c.Id,
		Name:       c.Name,
		Transport:  c.Transport,
		Command:    c.Command,
		URL:        c.URL,
		Enabled:    c.Enabled,
		EnvKeys:    keysOnlySorted(c.Env.AsJSONMap()),
		HeaderKeys: keysOnlySorted(c.Headers.AsJSONMap()),
		UpdatedAt:  c.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

func detailedMCPConnector(c *model.MCPConnector) mcpConnectorDetail {
	return mcpConnectorDetail{
		mcpConnectorSummary: summariseMCPConnector(c),
		Args:                map[string]interface{}(c.Args),
	}
}

func validateMCPConnectorCreateRequest(req MCPConnectorCreateRequest) error {
	if len(req.Name) > mcpConnectorMaxNameBytes {
		return fmt.Errorf("connector name is too long")
	}
	if req.Transport == model.MCPTransportStdio {
		return fmt.Errorf("stdio transport is disabled for user-managed connectors")
	}
	if len(req.Command) > mcpConnectorMaxCmdBytes {
		return fmt.Errorf("connector command is too long")
	}
	if len(req.URL) > mcpConnectorMaxURLBytes {
		return fmt.Errorf("connector url is too long")
	}
	if req.Transport == model.MCPTransportSSE || req.Transport == model.MCPTransportHTTP {
		if err := mcp_connector.ValidateRemoteConnectorURL(req.URL); err != nil {
			return err
		}
	}
	if err := validateConnectorMap("args", req.Args); err != nil {
		return err
	}
	if err := validateConnectorMap("env", req.Env); err != nil {
		return err
	}
	if err := validateConnectorMap("headers", req.Headers); err != nil {
		return err
	}
	return nil
}

func validateMCPConnectorUpdateRequest(req MCPConnectorUpdateRequest) error {
	if req.Name != nil && len(*req.Name) > mcpConnectorMaxNameBytes {
		return fmt.Errorf("connector name is too long")
	}
	if req.Transport != nil && *req.Transport == model.MCPTransportStdio {
		return fmt.Errorf("stdio transport is disabled for user-managed connectors")
	}
	if req.Command != nil && len(*req.Command) > mcpConnectorMaxCmdBytes {
		return fmt.Errorf("connector command is too long")
	}
	if req.URL != nil && len(*req.URL) > mcpConnectorMaxURLBytes {
		return fmt.Errorf("connector url is too long")
	}
	if req.URL != nil {
		if err := mcp_connector.ValidateRemoteConnectorURL(*req.URL); err != nil {
			return err
		}
	}
	if req.Args != nil {
		if err := validateConnectorMap("args", *req.Args); err != nil {
			return err
		}
	}
	if req.Env != nil {
		if err := validateConnectorMap("env", *req.Env); err != nil {
			return err
		}
	}
	if req.Headers != nil {
		if err := validateConnectorMap("headers", *req.Headers); err != nil {
			return err
		}
	}
	return nil
}

func validateConnectorMap(field string, value map[string]interface{}) error {
	if len(value) > mcpConnectorMaxMapEntries {
		return fmt.Errorf("%s has too many entries", field)
	}
	for k := range value {
		if strings.TrimSpace(k) == "" {
			return fmt.Errorf("%s contains an empty key", field)
		}
		if len(k) > mcpConnectorMaxKeyBytes {
			return fmt.Errorf("%s key is too long", field)
		}
	}
	if len(value) == 0 {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("%s must be valid JSON", field)
	}
	if len(data) > mcpConnectorMaxMapBytes {
		return fmt.Errorf("%s is too large", field)
	}
	return nil
}

// keysOnlySorted returns the keys of a JSONMap sorted
// ascending — same posture as the bridge's
// jsonMapToStringSlice fallback. Used to surface "what env
// vars are set?" without leaking values into the list view.
func keysOnlySorted(m model.JSONMap) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Tiny insertion sort to avoid importing sort for one path.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

func parsePathUint(c *gin.Context, key string) (uint, error) {
	raw := c.Param(key)
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s", key)
	}
	return uint(n), nil
}

// parseQueryIntDefault is declared in brand_asset_api.go and
// shared across the workagent package. Don't redeclare here.

// isValidationError discriminates the repo's input-shape errors
// from real DB failures. Matches `errors.Is(err,
// mcp_connector.ErrValidation)` — the repo wraps every input-
// shape rejection in a validationError whose Is method recognises
// the sentinel. The previous shape did string-prefix matching
// against a hand-maintained list of known message prefixes —
// fragile across rephrases and easy to forget when a new
// validation reason landed in the repo (the list silently fell
// out of sync, defaulting new validations to 5xx-shaped errors).
func isValidationError(err error) bool {
	return errors.Is(err, mcp_connector.ErrValidation)
}

// TestMCPConnector — POST /api/work-agent/mcp-connectors/:id/test
//
// Live-validation: loads the connector (uid-scoped), runs a
// transport-appropriate handshake against its URL + headers, and
// returns `{ok, detail}` so the UI can render a green/red badge
// without spending a full agent turn to discover broken config.
//
// Auth posture mirrors GetMCPConnector — uid-in-WHERE; cross-
// tenant 404 — so a probe never leaks "this connector exists for
// someone else" timing data.
//
// Always returns 200 once the auth + lookup boundary clears.
// Probe failures land as `{ok:false, detail:"..."}` in the body
// rather than as HTTP error codes — the UI distinguishes "I
// don't have permission" (HTTP 401/404) from "the upstream
// connector is broken" (HTTP 200 + ok:false).
func (api *AIChatApiNew) TestMCPConnector(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	id, err := parsePathUint(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid id"})
		return
	}
	row, err := mcp_connector.Default().LoadByIDForOwner(id, uid)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Connector not found"})
			return
		}
		globals.Error(fmt.Sprintf("[MCPConnector API] test load failed uid=%d id=%d: %v", uid, id, err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load connector"})
		return
	}
	result := mcp_connector.Probe(c.Request.Context(), row)
	c.JSON(http.StatusOK, gin.H{"data": result})
}
