//go:build desktop

package cloud_proxy

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// ExtractUIDFromAccessToken decodes the access token's JWT payload
// and returns the uid claim. Does NOT verify the signature — we
// got the token from workmax.app via HTTPS so we already trust it.
// Local code just needs to know the uid for SQL filtering.
//
// Returns (0, error) for malformed tokens (not a JWT, bad base64,
// bad JSON). Returns (0, nil) for valid JWTs that don't carry an
// "Id" claim — the caller decides whether that's a hard error
// (e.g. sync worker: skip this tick; chat handler: 401).
//
// Why we don't import the cloud's request.CustomClaims: that
// package transitively pulls model.* + GORM, which would balloon
// the desktop binary. The cloud's claims include 4-5 fields; we
// only need Id. Parsing inline keeps the desktop build leaf.
func ExtractUIDFromAccessToken(accessToken string) (uint, error) {
	parts := strings.Split(accessToken, ".")
	if len(parts) != 3 {
		return 0, fmt.Errorf("jwt: expected 3 segments separated by '.', got %d", len(parts))
	}
	// Segment 1 is the claims payload. JWT uses base64url WITHOUT
	// padding; RawURLEncoding handles that. (StdEncoding would
	// require manual '=' padding fix-up.)
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		// Some JWT producers emit padded variants. Try the padded
		// form as a fallback before giving up.
		if rawPad, errPad := base64.URLEncoding.DecodeString(parts[1]); errPad == nil {
			raw = rawPad
		} else {
			return 0, fmt.Errorf("jwt: decode payload: %w", err)
		}
	}
	// Tiny claims shape — only need Id. Other fields ignored.
	// Matches server/model/system/request.BaseClaims.Id (uint),
	// which is the only thing we need to route SQL.
	var claims struct {
		ID uint `json:"Id"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		return 0, fmt.Errorf("jwt: unmarshal payload: %w", err)
	}
	return claims.ID, nil
}
