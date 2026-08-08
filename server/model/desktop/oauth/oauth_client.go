// Package oauth holds the GORM models for the OAuth Authorization
// Server tables under server/migrations/20260633_create_desktop_oauth_tables.sql.
//
// Platform design: ProjectDocs/writer-work-agent-plugin-platform-design-2026-08.md
//
// These models intentionally store JSON columns as raw JSON-encoded
// strings rather than as `datatypes.JSON` — keeps the dependency
// surface narrow and lets the service layer (server/service/desktop/oauth/)
// own the JSON shape contract via typed Go helpers.
package oauth

import (
	"time"
)

// Client is a row in oauth_client. One row per registered client. The
// only client in P0 is "workmax-desktop" (seeded by the migration). Adding
// third-party clients later is just inserting more rows.
type Client struct {
	ID            uint       `gorm:"column:id;primaryKey;autoIncrement"`
	ClientID      string     `gorm:"column:client_id;type:varchar(64);uniqueIndex;not null"`
	ClientName    string     `gorm:"column:client_name;type:varchar(255);not null"`
	ClientType    string     `gorm:"column:client_type;type:varchar(20);not null"` // "public" | "confidential"
	ClientSecret  *string    `gorm:"column:client_secret;type:varchar(255)"`       // NULL for public clients
	RedirectURIs  string     `gorm:"column:redirect_uris;type:json;not null"`      // raw JSON: e.g. `["http://127.0.0.1:*/oauth/callback"]`
	AllowedScopes string     `gorm:"column:allowed_scopes;type:json;not null"`     // raw JSON: e.g. `["workagent"]`
	IsActive      bool       `gorm:"column:is_active;not null;default:true"`
	CreatedAt     *time.Time `gorm:"column:created_at"`
	UpdatedAt     *time.Time `gorm:"column:updated_at"`
}

// TableName pins the table name; required because GORM's default naming
// strategy would pluralize Client → clients, but the migration declares
// the table as w_desktop_oauth_client (workmax's `w_<feature>_*` convention).
func (Client) TableName() string { return "w_desktop_oauth_client" }

// Standard client_type constants. Keep in lockstep with the CHECK-ish
// values the service layer validates against.
const (
	ClientTypePublic       = "public"
	ClientTypeConfidential = "confidential"
)

// DesktopClientID is the well-known client_id seeded by the migration.
// Service layer and tests reference this constant rather than hardcoding
// the string in multiple places.
const DesktopClientID = "workmax-desktop"

// Desktop access tokens are resource credentials, not generic login JWTs.
// These values are part of the signed token contract. Compatibility routes
// currently shadow-evaluate the strengthened envelope; strict enforcement is
// mounted only after token rollover and minimum-scope gates pass.
const (
	DesktopResourceAudience        = "workmax.desktop"
	DesktopOAuthScopeWorkAgent     = "workagent"
	DesktopCredentialDeviceSession = "device-session"
)
