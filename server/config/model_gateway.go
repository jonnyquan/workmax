package config

// ModelGateway configures the Desktop model gateway
// (POST /api/desktop/model-gateway/{anthropic,openai}/...).
//
// The gateway spends PLATFORM money on a user's behalf: it holds the
// provider credentials and the Desktop only ever sees our own URL. Every knob
// here is therefore a tap, not a tuning parameter — the defaults are chosen so
// that a deployment which never writes a `model_gateway:` block still has a
// body-size cap, a per-user rate limit, a per-user concurrency limit, a global
// concurrency limit and a wall-clock timeout.
type ModelGateway struct {
	// Enabled is a tri-state kill switch. nil (field absent) means enabled —
	// the routes are registered unconditionally and a disabled gateway
	// answers 503, so an operator can turn the tap off without a route
	// change. Explicit `enabled: false` disables it.
	Enabled *bool `mapstructure:"enabled" json:"enabled" yaml:"enabled"`

	// MaxRequestBytes caps the decoded request body. A model request is
	// mostly conversation history; 4 MiB is far above a legitimate turn and
	// far below "stream a file into our egress bill". Zero → default.
	MaxRequestBytes int64 `mapstructure:"max_request_bytes" json:"maxRequestBytes" yaml:"max_request_bytes"`

	// RequestTimeoutSeconds bounds the whole upstream exchange, streaming
	// included. It is the last line of defence against a hung upstream
	// holding one of our concurrency slots forever. Zero → default.
	RequestTimeoutSeconds int `mapstructure:"request_timeout_seconds" json:"requestTimeoutSeconds" yaml:"request_timeout_seconds"`

	// ResponseHeaderTimeoutSeconds bounds how long we wait for the upstream
	// to send its status line. Separate from the total timeout because a
	// stalled connect should fail fast while a legitimately long stream
	// should not. Zero → default.
	ResponseHeaderTimeoutSeconds int `mapstructure:"response_header_timeout_seconds" json:"responseHeaderTimeoutSeconds" yaml:"response_header_timeout_seconds"`

	// RateLimit is the per-user bucket plus the process-wide concurrency cap.
	RateLimit ModelGatewayRateLimit `mapstructure:"rate_limit" json:"rateLimit" yaml:"rate_limit"`
}

// ModelGatewayRateLimit mirrors CanvasRateLimitBucket's shape because the
// gateway reuses middleware.UserRateLimitRegistry — one mechanism, one set of
// semantics, rather than a second hand-rolled limiter.
type ModelGatewayRateLimit struct {
	// PerMinute is a fixed-window per-user request counter.
	PerMinute int `mapstructure:"per_minute" json:"perMinute" yaml:"per_minute"`
	// PerUserConcurrent caps a single user's in-flight upstream calls. Low by
	// design: one Desktop runs one tool loop at a time, and a client that
	// opens ten parallel streams is either broken or hostile.
	PerUserConcurrent int `mapstructure:"per_user_concurrent" json:"perUserConcurrent" yaml:"per_user_concurrent"`
	// GlobalConcurrent caps in-flight upstream calls across every user.
	GlobalConcurrent int `mapstructure:"global_concurrent" json:"globalConcurrent" yaml:"global_concurrent"`
}

// modelGatewayDefaults are applied per-field, so a YAML block that sets only
// `per_minute` still inherits every other guard.
var modelGatewayDefaults = ModelGateway{
	MaxRequestBytes:              4 << 20,
	RequestTimeoutSeconds:        600,
	ResponseHeaderTimeoutSeconds: 90,
	RateLimit: ModelGatewayRateLimit{
		PerMinute:         60,
		PerUserConcurrent: 2,
		GlobalConcurrent:  64,
	},
}

// ModelGatewayDefaults returns the built-in guard values.
func ModelGatewayDefaults() ModelGateway {
	return modelGatewayDefaults
}

// IsEnabled reports whether the gateway should serve traffic. A nil receiver
// (no config block at all) is enabled — the feature ships on by default and
// operators opt out, because a silently-off gateway looks identical to a
// broken one from the Desktop's side.
func (m *ModelGateway) IsEnabled() bool {
	if m == nil || m.Enabled == nil {
		return true
	}
	return *m.Enabled
}

// EffectiveMaxRequestBytes returns the body cap, defaulted.
func (m *ModelGateway) EffectiveMaxRequestBytes() int64 {
	if m != nil && m.MaxRequestBytes > 0 {
		return m.MaxRequestBytes
	}
	return modelGatewayDefaults.MaxRequestBytes
}

// EffectiveRequestTimeoutSeconds returns the total-exchange timeout, defaulted.
func (m *ModelGateway) EffectiveRequestTimeoutSeconds() int {
	if m != nil && m.RequestTimeoutSeconds > 0 {
		return m.RequestTimeoutSeconds
	}
	return modelGatewayDefaults.RequestTimeoutSeconds
}

// EffectiveResponseHeaderTimeoutSeconds returns the header timeout, defaulted.
func (m *ModelGateway) EffectiveResponseHeaderTimeoutSeconds() int {
	if m != nil && m.ResponseHeaderTimeoutSeconds > 0 {
		return m.ResponseHeaderTimeoutSeconds
	}
	return modelGatewayDefaults.ResponseHeaderTimeoutSeconds
}

// EffectiveRateLimit returns the per-user bucket with defaults filled in.
func (m *ModelGateway) EffectiveRateLimit() ModelGatewayRateLimit {
	bucket := ModelGatewayRateLimit{}
	if m != nil {
		bucket = m.RateLimit
	}
	if bucket.PerMinute <= 0 {
		bucket.PerMinute = modelGatewayDefaults.RateLimit.PerMinute
	}
	if bucket.PerUserConcurrent <= 0 {
		bucket.PerUserConcurrent = modelGatewayDefaults.RateLimit.PerUserConcurrent
	}
	if bucket.GlobalConcurrent <= 0 {
		bucket.GlobalConcurrent = modelGatewayDefaults.RateLimit.GlobalConcurrent
	}
	return bucket
}
