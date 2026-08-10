package config

import "testing"

// The gateway spends platform money, so its guards must exist whether or not
// an operator wrote a config block. Every accessor below is tested against
// the "field absent" case first, because that is the shape a fresh deploy has.

func TestModelGateway_NilConfigStillCarriesEveryGuard(t *testing.T) {
	var cfg *ModelGateway
	defaults := ModelGatewayDefaults()

	if !cfg.IsEnabled() {
		t.Error("an absent model_gateway block must read as enabled — a silently-off gateway is indistinguishable from a broken one")
	}
	if got := cfg.EffectiveMaxRequestBytes(); got != defaults.MaxRequestBytes {
		t.Errorf("max request bytes = %d, want %d", got, defaults.MaxRequestBytes)
	}
	if got := cfg.EffectiveRequestTimeoutSeconds(); got != defaults.RequestTimeoutSeconds {
		t.Errorf("request timeout = %d, want %d", got, defaults.RequestTimeoutSeconds)
	}
	if got := cfg.EffectiveResponseHeaderTimeoutSeconds(); got != defaults.ResponseHeaderTimeoutSeconds {
		t.Errorf("response header timeout = %d, want %d", got, defaults.ResponseHeaderTimeoutSeconds)
	}
	bucket := cfg.EffectiveRateLimit()
	if bucket.PerMinute != defaults.RateLimit.PerMinute ||
		bucket.PerUserConcurrent != defaults.RateLimit.PerUserConcurrent ||
		bucket.GlobalConcurrent != defaults.RateLimit.GlobalConcurrent {
		t.Errorf("rate limit = %+v, want %+v", bucket, defaults.RateLimit)
	}
}

// Defaults fill in PER FIELD. A YAML block that sets only one knob must not
// silently zero (i.e. disable) the others.
func TestModelGateway_PartialConfigInheritsTheRest(t *testing.T) {
	cfg := &ModelGateway{RateLimit: ModelGatewayRateLimit{PerMinute: 5}}
	defaults := ModelGatewayDefaults()

	bucket := cfg.EffectiveRateLimit()
	if bucket.PerMinute != 5 {
		t.Errorf("per minute = %d, want the configured 5", bucket.PerMinute)
	}
	if bucket.PerUserConcurrent != defaults.RateLimit.PerUserConcurrent {
		t.Errorf("per-user concurrency = %d, want the default %d — an unset knob is not an open tap",
			bucket.PerUserConcurrent, defaults.RateLimit.PerUserConcurrent)
	}
	if bucket.GlobalConcurrent != defaults.RateLimit.GlobalConcurrent {
		t.Errorf("global concurrency = %d, want the default %d",
			bucket.GlobalConcurrent, defaults.RateLimit.GlobalConcurrent)
	}
	if cfg.EffectiveMaxRequestBytes() != defaults.MaxRequestBytes {
		t.Error("an unset body cap must inherit the default, not become unlimited")
	}
}

func TestModelGateway_ExplicitDisableIsHonoured(t *testing.T) {
	disabled := false
	enabled := true

	if (&ModelGateway{Enabled: &disabled}).IsEnabled() {
		t.Error("enabled: false must turn the gateway off")
	}
	if !(&ModelGateway{Enabled: &enabled}).IsEnabled() {
		t.Error("enabled: true must turn the gateway on")
	}
}

func TestModelGateway_ExplicitValuesWin(t *testing.T) {
	cfg := &ModelGateway{
		MaxRequestBytes:              1234,
		RequestTimeoutSeconds:        11,
		ResponseHeaderTimeoutSeconds: 7,
		RateLimit: ModelGatewayRateLimit{
			PerMinute: 3, PerUserConcurrent: 1, GlobalConcurrent: 2,
		},
	}
	if cfg.EffectiveMaxRequestBytes() != 1234 {
		t.Error("configured body cap was not honoured")
	}
	if cfg.EffectiveRequestTimeoutSeconds() != 11 {
		t.Error("configured request timeout was not honoured")
	}
	if cfg.EffectiveResponseHeaderTimeoutSeconds() != 7 {
		t.Error("configured header timeout was not honoured")
	}
	bucket := cfg.EffectiveRateLimit()
	if bucket.PerMinute != 3 || bucket.PerUserConcurrent != 1 || bucket.GlobalConcurrent != 2 {
		t.Errorf("configured rate limit was not honoured: %+v", bucket)
	}
}
