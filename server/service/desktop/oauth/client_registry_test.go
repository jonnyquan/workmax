package oauth

import (
	"context"
	"errors"
	"testing"

	model "server/model/desktop/oauth"
)

func TestFindActiveClient_Happy(t *testing.T) {
	db := newTestDB(t)
	seedDesktopClient(t, db)
	r := NewClientRegistry(db)

	got, err := r.FindActiveClient(context.Background(), model.DesktopClientID)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if got.ClientID != model.DesktopClientID {
		t.Errorf("ClientID: got %q, want %q", got.ClientID, model.DesktopClientID)
	}
	if got.ClientType != model.ClientTypePublic {
		t.Errorf("ClientType: got %q, want %q", got.ClientType, model.ClientTypePublic)
	}
	if !got.IsActive {
		t.Error("expected IsActive=true")
	}
}

func TestFindActiveClient_NotFound(t *testing.T) {
	db := newTestDB(t)
	r := NewClientRegistry(db)

	_, err := r.FindActiveClient(context.Background(), "does-not-exist")
	if !errors.Is(err, ErrClientNotFound) {
		t.Errorf("expected ErrClientNotFound, got %v", err)
	}
}

func TestFindActiveClient_Inactive(t *testing.T) {
	db := newTestDB(t)
	seedDesktopClient(t, db)
	// Flip to inactive (admin disabled the client).
	if err := db.Model(&model.Client{}).
		Where("client_id = ?", model.DesktopClientID).
		Update("is_active", false).Error; err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	r := NewClientRegistry(db)

	_, err := r.FindActiveClient(context.Background(), model.DesktopClientID)
	if !errors.Is(err, ErrClientInactive) {
		t.Errorf("expected ErrClientInactive, got %v", err)
	}
}

func TestValidateRedirectURI(t *testing.T) {
	db := newTestDB(t)
	client := seedDesktopClient(t, db)
	r := NewClientRegistry(db)

	cases := []struct {
		name      string
		uri       string
		wantErrIs error // nil means must succeed
	}{
		// Happy paths — any loopback port + the registered path.
		{"127.0.0.1 typical port", "http://127.0.0.1:54321/oauth/callback", nil},
		{"127.0.0.1 high port", "http://127.0.0.1:65000/oauth/callback", nil},
		{"127.0.0.1 low port", "http://127.0.0.1:1024/oauth/callback", nil},

		// Loopback rule violations.
		{"0.0.0.0 (binds all interfaces)", "http://0.0.0.0:54321/oauth/callback", ErrRedirectURIInvalid},
		{"public IP", "http://192.168.1.100:54321/oauth/callback", ErrRedirectURIInvalid},
		{"localhost.evil.com subdomain attack", "http://localhost.evil.com:54321/oauth/callback", ErrRedirectURIInvalid},
		{"127.0.0.2 not the conventional address", "http://127.0.0.2:54321/oauth/callback", ErrRedirectURIInvalid},

		// Scheme/query/fragment rules.
		{"https rejected", "https://127.0.0.1:54321/oauth/callback", ErrRedirectURIInvalid},
		{"ws scheme", "ws://127.0.0.1:54321/oauth/callback", ErrRedirectURIInvalid},
		{"query string", "http://127.0.0.1:54321/oauth/callback?extra=1", ErrRedirectURIInvalid},
		{"fragment", "http://127.0.0.1:54321/oauth/callback#frag", ErrRedirectURIInvalid},

		// Pattern path mismatch (everything else loopback-clean).
		{"different path", "http://127.0.0.1:54321/different/path", ErrRedirectURIMismatch},
		{"missing path", "http://127.0.0.1:54321/", ErrRedirectURIMismatch},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := r.ValidateRedirectURI(client, c.uri)
			if c.wantErrIs == nil {
				if err != nil {
					t.Errorf("expected success, got %v", err)
				}
				return
			}
			if !errors.Is(err, c.wantErrIs) {
				t.Errorf("expected errors.Is(%v), got %v", c.wantErrIs, err)
			}
		})
	}
}

// Ensures localhost is accepted just like 127.0.0.1 if the pattern
// explicitly lists it. Most clients use 127.0.0.1 in patterns, but
// `localhost` is valid per RFC 8252 §7.3.
func TestValidateRedirectURI_LocalhostHostnameAllowed(t *testing.T) {
	db := newTestDB(t)
	client := &model.Client{
		ClientID:      "test-client",
		ClientName:    "test",
		ClientType:    model.ClientTypePublic,
		RedirectURIs:  `["http://localhost:*/oauth/callback"]`,
		AllowedScopes: `["workagent"]`,
		IsActive:      true,
	}
	if err := db.Create(client).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	r := NewClientRegistry(db)

	if err := r.ValidateRedirectURI(client, "http://localhost:54321/oauth/callback"); err != nil {
		t.Errorf("expected localhost to be accepted with matching pattern, got %v", err)
	}
	// Same pattern should NOT accept 127.0.0.1 (patterns are literal modulo `:*`).
	if err := r.ValidateRedirectURI(client, "http://127.0.0.1:54321/oauth/callback"); !errors.Is(err, ErrRedirectURIMismatch) {
		t.Errorf("expected mismatch when hostname differs from pattern, got %v", err)
	}
}

func TestValidateRedirectURI_MalformedClientPatterns(t *testing.T) {
	bad := &model.Client{
		ClientID:      "test",
		RedirectURIs:  `not-json-at-all`,
		AllowedScopes: `[]`,
		IsActive:      true,
	}
	r := NewClientRegistry(nil)
	err := r.ValidateRedirectURI(bad, "http://127.0.0.1:54321/oauth/callback")
	if !errors.Is(err, ErrClientPatternsMalformed) {
		t.Errorf("expected ErrClientPatternsMalformed, got %v", err)
	}
}

func TestValidateScopes(t *testing.T) {
	r := NewClientRegistry(nil)
	client := &model.Client{AllowedScopes: `["workagent","history.read","agent.run"]`}

	got, err := r.ValidateScopes(client, "agent.run  history.read agent.run")
	if err != nil {
		t.Fatalf("valid scopes rejected: %v", err)
	}
	if got != "agent.run history.read" {
		t.Fatalf("canonical scopes = %q, want %q", got, "agent.run history.read")
	}

	for _, tc := range []struct {
		name      string
		requested string
		want      error
	}{
		{name: "empty", requested: "", want: ErrScopeInvalid},
		{name: "unknown", requested: "billing.write", want: ErrScopeNotAllowed},
		{name: "invalid token", requested: "workagent\\scope", want: ErrScopeInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.ValidateScopes(client, tc.requested)
			if !errors.Is(err, tc.want) {
				t.Fatalf("ValidateScopes() error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestValidateScopesRejectsMalformedRegistry(t *testing.T) {
	r := NewClientRegistry(nil)
	for _, raw := range []string{
		`not-json`,
		`null`,
		`["workagent",""]`,
		`["workagent","workagent"]`,
	} {
		_, err := r.ValidateScopes(&model.Client{AllowedScopes: raw}, "workagent")
		if !errors.Is(err, ErrClientScopesMalformed) {
			t.Errorf("AllowedScopes %q error = %v, want ErrClientScopesMalformed", raw, err)
		}
	}
}
