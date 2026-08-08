package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"gorm.io/gorm"

	model "server/model/desktop/oauth"
)

// Client registry errors. Wrapped with %w by callers as needed.
var (
	ErrClientNotFound          = errors.New("client_registry: client_id not registered")
	ErrClientInactive          = errors.New("client_registry: client is_active=false")
	ErrRedirectURIInvalid      = errors.New("client_registry: redirect_uri failed loopback validation")
	ErrRedirectURIMismatch     = errors.New("client_registry: redirect_uri does not match any registered pattern")
	ErrClientPatternsMalformed = errors.New("client_registry: stored redirect_uris JSON is not a string array")
	ErrClientScopesMalformed   = errors.New("client_registry: stored allowed_scopes JSON is not a valid scope array")
	ErrScopeInvalid            = errors.New("client_registry: requested scope is malformed")
	ErrScopeNotAllowed         = errors.New("client_registry: requested scope is not allowed for this client")
)

// ClientRegistry reads oauth_client rows and validates incoming
// redirect_uri values against the patterns the client registered.
//
// Lives on the read path of /authorize + /token; cache-friendly
// (clients change only when an admin re-seeds), but P-1.2 doesn't
// add a cache — a single SELECT per request is fine at desktop scale.
type ClientRegistry struct {
	db *gorm.DB
}

// NewClientRegistry returns a registry backed by the given gorm.DB.
// The DB must be configured to talk to the same connection the
// migration in 20260633 was applied against.
func NewClientRegistry(db *gorm.DB) *ClientRegistry {
	return &ClientRegistry{db: db}
}

// FindActiveClient returns the client row keyed by `clientID`. Returns
// ErrClientNotFound when no row exists and ErrClientInactive when the
// row exists but `is_active=false`. Both states behave the same to
// the OAuth caller (we MUST NOT distinguish them in error messages
// surfaced to the wire — that would let an attacker enumerate valid
// client IDs).
func (r *ClientRegistry) FindActiveClient(ctx context.Context, clientID string) (*model.Client, error) {
	var client model.Client
	err := r.db.WithContext(ctx).Where("client_id = ?", clientID).First(&client).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrClientNotFound
		}
		return nil, fmt.Errorf("client_registry: query: %w", err)
	}
	if !client.IsActive {
		return nil, ErrClientInactive
	}
	return &client, nil
}

// ValidateRedirectURI enforces RFC 8252 §7.3 loopback rules AND the
// per-client pattern allowlist.
//
// Wire-level guarantees (independent of the client's patterns):
//
//   - scheme MUST be http (loopback never uses https — the browser
//     allows http://127.0.0.1 specifically because it can't be
//     intercepted on the wire)
//   - hostname MUST be exactly "127.0.0.1" or "localhost". This
//     rejects "0.0.0.0" (binds all interfaces — would let a remote
//     attacker receive the code), "127.0.0.2" (still loopback per
//     RFC 3330 but not the conventional address), and
//     "localhost.evil.com" (a registered DNS subdomain that
//     superficially looks like "localhost").
//   - no query string and no fragment (the OAuth library appends
//     ?code=...&state=... itself; pre-existing query would corrupt
//     either the redirect or the appended params)
//
// Then the provided URI MUST match at least one pattern in the
// client's `redirect_uris` array. Patterns may use `:*` in the port
// position to allow any numeric port — required because the desktop
// sidecar binds to a fresh OS-assigned port on every launch.
func (r *ClientRegistry) ValidateRedirectURI(client *model.Client, provided string) error {
	u, err := url.Parse(provided)
	if err != nil {
		return fmt.Errorf("%w: parse: %v", ErrRedirectURIInvalid, err)
	}
	if u.Scheme != "http" {
		return fmt.Errorf("%w: scheme must be http (got %q)", ErrRedirectURIInvalid, u.Scheme)
	}
	host := u.Hostname()
	if host != "127.0.0.1" && host != "localhost" {
		return fmt.Errorf("%w: hostname must be 127.0.0.1 or localhost (got %q)", ErrRedirectURIInvalid, host)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%w: query or fragment not allowed", ErrRedirectURIInvalid)
	}

	patterns, err := parseRedirectURIs(client.RedirectURIs)
	if err != nil {
		return err
	}
	for _, p := range patterns {
		if matchLoopbackPattern(provided, p) {
			return nil
		}
	}
	return ErrRedirectURIMismatch
}

// ValidateScopes checks the OAuth space-delimited scope request against the
// registered client's exact allowlist and returns a canonical representation.
// Scope order follows the request while duplicates are removed. Callers must
// apply any product default before invoking this method; an empty request is
// rejected so a missing authorization decision cannot become an implicit
// wildcard grant.
func (r *ClientRegistry) ValidateScopes(client *model.Client, requested string) (string, error) {
	allowed, err := parseAllowedScopes(client.AllowedScopes)
	if err != nil {
		return "", err
	}

	requestedTokens := strings.Fields(requested)
	if len(requestedTokens) == 0 {
		return "", ErrScopeInvalid
	}

	seen := make(map[string]struct{}, len(requestedTokens))
	canonical := make([]string, 0, len(requestedTokens))
	for _, scope := range requestedTokens {
		if !validScopeToken(scope) {
			return "", fmt.Errorf("%w: %q", ErrScopeInvalid, scope)
		}
		if _, ok := allowed[scope]; !ok {
			return "", fmt.Errorf("%w: %q", ErrScopeNotAllowed, scope)
		}
		if _, duplicate := seen[scope]; duplicate {
			continue
		}
		seen[scope] = struct{}{}
		canonical = append(canonical, scope)
	}
	return strings.Join(canonical, " "), nil
}

func parseAllowedScopes(raw string) (map[string]struct{}, error) {
	var scopes []string
	if err := json.Unmarshal([]byte(raw), &scopes); err != nil || scopes == nil {
		return nil, fmt.Errorf("%w", ErrClientScopesMalformed)
	}
	allowed := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		if !validScopeToken(scope) {
			return nil, fmt.Errorf("%w: invalid entry", ErrClientScopesMalformed)
		}
		if _, duplicate := allowed[scope]; duplicate {
			return nil, fmt.Errorf("%w: duplicate entry", ErrClientScopesMalformed)
		}
		allowed[scope] = struct{}{}
	}
	return allowed, nil
}

// validScopeToken follows RFC 6749 section 3.3: scope-token is printable
// ASCII excluding double quote, backslash and space.
func validScopeToken(scope string) bool {
	if scope == "" {
		return false
	}
	for i := 0; i < len(scope); i++ {
		b := scope[i]
		if b < 0x21 || b > 0x7e || b == 0x22 || b == 0x5c {
			return false
		}
	}
	return true
}

// parseRedirectURIs decodes the raw JSON `redirect_uris` column into
// a Go slice. We don't cache; the parse is microseconds and the row
// is loaded once per request anyway.
func parseRedirectURIs(raw string) ([]string, error) {
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrClientPatternsMalformed, err)
	}
	return out, nil
}

// matchLoopbackPattern checks whether `provided` matches `pattern`,
// where `pattern` may contain a single `:*` token in the port
// position to accept any port number.
//
// Implementation: turn the pattern into a regex by escaping every
// character (regexp.QuoteMeta), then swap the escaped `:\*` back
// to `:[0-9]+`. Anchored at both ends so it's an exact match, not a
// substring. Wins over hand-parsing in clarity for this narrow
// shape; perf is irrelevant (one match per /authorize call).
func matchLoopbackPattern(provided, pattern string) bool {
	escaped := regexp.QuoteMeta(pattern)
	re := strings.Replace(escaped, `:\*`, `:[0-9]+`, 1)
	matched, err := regexp.MatchString(`^`+re+`$`, provided)
	return err == nil && matched
}
