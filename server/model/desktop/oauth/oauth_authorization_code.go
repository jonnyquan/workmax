package oauth

import "time"

// AuthorizationCode is a row in oauth_authorization_code. Short-lived
// (10 min TTL), single-use. See backend-oauth-prerequisites.md §3.2.
type AuthorizationCode struct {
	ID                  uint       `gorm:"column:id;primaryKey;autoIncrement"`
	Code                string     `gorm:"column:code;type:varchar(64);uniqueIndex;not null"`
	ClientID            string     `gorm:"column:client_id;type:varchar(64);not null"`
	UID                 int        `gorm:"column:uid;not null"`
	DeviceID            *string    `gorm:"column:device_id;type:varchar(64);index"` // nil only for legacy authorize compatibility
	RedirectURI         string     `gorm:"column:redirect_uri;type:varchar(500);not null"`
	CodeChallenge       string     `gorm:"column:code_challenge;type:varchar(128);not null"`
	CodeChallengeMethod string     `gorm:"column:code_challenge_method;type:varchar(10);not null"`
	Scope               string     `gorm:"column:scope;type:varchar(255);not null"`
	Used                bool       `gorm:"column:used;not null;default:false"`
	ExpiresAt           time.Time  `gorm:"column:expires_at;not null;index"`
	CreatedAt           *time.Time `gorm:"column:created_at"`
}

func (AuthorizationCode) TableName() string { return "w_desktop_oauth_authorization_code" }

// CodeChallengeMethodS256 is the only PKCE challenge method we accept.
// Plain is deliberately rejected at the service layer; this constant
// exists so tests + handlers can refer to a single source.
const CodeChallengeMethodS256 = "S256"

// AuthorizationCodeTTL is the absolute lifetime of an issued code.
// Service layer reads this when computing ExpiresAt at insert time.
const AuthorizationCodeTTL = 10 * time.Minute
