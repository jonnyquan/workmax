package request

import (
	jwt "github.com/golang-jwt/jwt/v4"
)

// Custom claims structure
type CustomClaims struct {
	BaseClaims
	BufferTime      int64
	OAuthClientID   string `json:"oauth_client_id,omitempty"`   // OAuth authorized party; empty for legacy /api/auth/sign-in tokens
	OAuthScope      string `json:"scope,omitempty"`             // canonical space-delimited OAuth scopes
	CredentialType  string `json:"credential_type,omitempty"`   // separates resource/device credentials from generic login JWTs
	DeviceID        string `json:"device_id,omitempty"`         // stable Desktop installation identity
	DeviceSessionID string `json:"device_session_id,omitempty"` // refresh-token chain that authorized this access token
	jwt.StandardClaims
}

type BaseClaims struct {
	Id       uint
	Email    string
	Nickname string
}
