package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

func TestSanitizedConfigExamplesParseAndValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		path            string
		wantEnvironment string
		wantStorage     string
		wantPlaceholder bool
	}{
		{
			name:            "development",
			path:            filepath.Join("..", "config.example.yaml"),
			wantEnvironment: "development",
			wantStorage:     "local",
			wantPlaceholder: true,
		},
		{
			name:            "release",
			path:            filepath.Join("..", "config.release.example.yaml"),
			wantEnvironment: "production",
			wantStorage:     "r2",
			wantPlaceholder: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			raw, err := os.ReadFile(tt.path)
			if err != nil {
				t.Fatalf("read %s: %v", tt.path, err)
			}
			if got := strings.Contains(string(raw), "CHANGE_ME"); got != tt.wantPlaceholder {
				t.Fatalf("CHANGE_ME placeholder presence = %v, want %v", got, tt.wantPlaceholder)
			}

			v := viper.New()
			v.SetConfigFile(tt.path)
			v.SetConfigType("yaml")
			if err := v.ReadInConfig(); err != nil {
				t.Fatalf("parse %s: %v", tt.path, err)
			}

			var got Server
			if err := v.Unmarshal(&got); err != nil {
				t.Fatalf("unmarshal %s: %v", tt.path, err)
			}
			if got.System.Env != tt.wantEnvironment {
				t.Errorf("system.env = %q, want %q", got.System.Env, tt.wantEnvironment)
			}
			if got.System.FrontendURL != got.System.BackendURL {
				t.Errorf("Desktop-only example must keep legacy frontend_url on backend_url: frontend=%q backend=%q", got.System.FrontendURL, got.System.BackendURL)
			}
			if !strings.HasSuffix(got.Google.RedirectURL, "/api/auth/google/callback") {
				t.Errorf("google.redirect_url = %q, want Server callback route", got.Google.RedirectURL)
			}
			if got.Stripe.Domain != got.System.BackendURL {
				t.Errorf("stripe.domain = %q, want Go Server origin %q", got.Stripe.Domain, got.System.BackendURL)
			}
			if got.Stripe.ReturnPath != "/api/desktop/billing/return" {
				t.Errorf("stripe.return_path = %q, want Desktop Server landing", got.Stripe.ReturnPath)
			}
			if got.Generator.Storage.Type != tt.wantStorage {
				t.Errorf("generator.storage.type = %q, want %q", got.Generator.Storage.Type, tt.wantStorage)
			}
			if err := got.Generator.Validate(); err != nil {
				t.Errorf("generator config is invalid: %v", err)
			}
			if err := got.Statics.Validate(); err != nil {
				t.Errorf("statics config is invalid: %v", err)
			}
			if got.AgentPlatformRollout == nil {
				t.Fatal("agent_platform_rollout block is missing")
			}
			if err := got.AgentPlatformRollout.Validate(); err != nil {
				t.Errorf("agent platform rollout config is invalid: %v", err)
			}
		})
	}
}
