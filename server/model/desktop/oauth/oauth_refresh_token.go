package oauth

import "time"

// RefreshToken is a row in oauth_refresh_token. Each issuance of a
// refresh token creates a new row; rotation marks the previous row
// `revoked=true, revoked_reason='rotated'` and the new row's
// `parent_id` points back to it. Replay detection: if a code path
// receives a token that is `revoked=true`, the service layer revokes
// the entire chain_id (full row sweep, all reason='replay_detected').
//
// `device_id` is the Q4 device identity decision: a 32-character
// hex id minted by the desktop sidecar on first launch and persisted
// across logout/login. Distinct from `chain_id` (which advances on
// every fresh login OAuth flow).
//
// See backend-oauth-prerequisites.md §3.3 + cloud-proxy.md §4 for the
// rotation + replay semantics.
type RefreshToken struct {
	ID            uint       `gorm:"column:id;primaryKey;autoIncrement"`
	Token         string     `gorm:"column:token;type:varchar(128);uniqueIndex;not null"`
	ChainID       string     `gorm:"column:chain_id;type:varchar(64);not null;index"`
	DeviceID      string     `gorm:"column:device_id;type:varchar(64);not null"`
	ClientID      string     `gorm:"column:client_id;type:varchar(64);not null"`
	UID           int        `gorm:"column:uid;not null"`
	Scope         string     `gorm:"column:scope;type:varchar(255);not null"`
	ParentID      *uint      `gorm:"column:parent_id"`
	Revoked       bool       `gorm:"column:revoked;not null;default:false"`
	RevokedReason *string    `gorm:"column:revoked_reason;type:varchar(50)"`
	ExpiresAt     time.Time  `gorm:"column:expires_at;not null"`
	LastUsedAt    *time.Time `gorm:"column:last_used_at"`
	DeviceInfo    *string    `gorm:"column:device_info;type:json"` // raw JSON: {os, app_version, hostname?}
	CreatedAt     *time.Time `gorm:"column:created_at"`
}

func (RefreshToken) TableName() string { return "w_desktop_oauth_refresh_token" }

// RefreshTokenTTL is the absolute lifetime of an issued refresh token.
// Access tokens are short-lived (15 min, per backend-oauth §4.2);
// refresh tokens live 90 days so users don't have to re-login on
// every workweek.
const RefreshTokenTTL = 90 * 24 * time.Hour

// Revocation reasons. The service layer sets exactly one of these
// when flipping `Revoked` to true.
const (
	RevokedReasonRotated        = "rotated"
	RevokedReasonReplayDetected = "replay_detected"
	RevokedReasonUserRevoked    = "user_revoked"
	RevokedReasonExpired        = "expired"
)
