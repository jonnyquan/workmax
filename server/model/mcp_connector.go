package model

import (
	"server/globals"
	"time"
)

// MCPConnector — per-user external MCP server registration.
// Phase B1 of the MCP-connector quarter. Each row is one
// configured external MCP server the agent will load on every
// turn for the owning user (transport spawn / connection happens
// at SDK turn-setup time; this row is just configuration).
//
// Transports — see github.com/jonnyquan/claude-agent-sdk-go/
// pkg/claudesdk McpServerConfig variants:
//
//   "stdio"  → spawn a local subprocess (Command + Args + Env)
//   "sse"    → connect to a Server-Sent-Events endpoint (URL + Headers)
//   "http"   → connect to an HTTP endpoint (URL + Headers)
//
// Stdio servers see the Env map as os.Environ overlay; sse/http
// servers see Headers stamped onto every request. Authentication
// secrets (Bearer tokens, API keys) live INSIDE Env or Headers
// for now; Phase B3 will encrypt-at-rest those JSON blobs so
// `SELECT *` doesn't surface raw credentials in logs / backups.
type MCPConnector struct {
	globals.GraMODEL

	DeletedAt *time.Time `json:"-" gorm:"column:deleted_at;index"`

	UID  int    `json:"uid" gorm:"column:uid;not null;default:0;index:idx_mcp_connector_uid_enabled,priority:1;comment:owning user"`
	Name string `json:"name" gorm:"column:name;type:varchar(120);not null;default:'';comment:user-facing label"`

	// Transport — one of {"stdio", "sse", "http"}. The SDK side
	// translates this into the appropriate McpServerConfig
	// variant at turn setup.
	Transport string `json:"transport" gorm:"column:transport;type:varchar(16);not null;default:'stdio';comment:stdio/sse/http"`

	// Command + Args apply to Transport=="stdio". Args is plain
	// JSON — argv is not a secret. Env is encrypted at rest
	// because subprocess environment variables routinely carry
	// API keys / OAuth tokens (Phase B3 at-rest encryption).
	Command string           `json:"command" gorm:"column:command;type:varchar(255);not null;default:'';comment:stdio binary path"`
	Args    JSONMap          `json:"args,omitempty" gorm:"column:args;type:json;comment:stdio argv (JSON array)"`
	Env     EncryptedJSONMap `json:"env,omitempty" gorm:"column:env;type:text;comment:stdio env overlay (encrypted at rest, secrets.Encrypt envelope)"`

	// URL + Headers apply to Transport in {"sse", "http"}.
	// URL is plain (not a secret in practice; the bearer is in
	// the header). Headers is encrypted because that's where
	// Authorization: Bearer ... lives.
	URL     string           `json:"url" gorm:"column:url;type:varchar(2048);not null;default:'';comment:sse/http endpoint"`
	Headers EncryptedJSONMap `json:"headers,omitempty" gorm:"column:headers;type:text;comment:sse/http auth headers (encrypted at rest)"`

	// Enabled — per-uid kill switch. Disabled rows skip the
	// SDK-options injection in Phase B2's hot path, so a user can
	// triage a misbehaving connector without deleting the row +
	// losing the configuration.
	Enabled bool `json:"enabled" gorm:"column:enabled;type:tinyint(1);default:1;index:idx_mcp_connector_uid_enabled,priority:2;comment:per-uid kill switch"`
}

// MCPConnector table name. The "global" prefix is intentional —
// the table is platform-managed even though each row is per-uid
// scoped, matching the w_global_brand / w_global_character /
// w_global_director_style naming.
func (MCPConnector) TableName() string {
	return "w_global_mcp_connector"
}

const (
	// MCPTransportStdio names the local-subprocess transport.
	MCPTransportStdio = "stdio"
	// MCPTransportSSE names the Server-Sent-Events transport.
	MCPTransportSSE = "sse"
	// MCPTransportHTTP names the plain HTTP transport.
	MCPTransportHTTP = "http"
)
