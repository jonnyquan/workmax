package config

// Canvas groups all runtime settings for the Canvas tool.
//
// Today this carries rate-limit buckets (§12.2) and PromptGuard knobs
// (§12.3). Additional settings — allowed asset hosts, SSRF rules, audit
// config — can land here without polluting the top-level config.Server.
type Canvas struct {
	RateLimit   CanvasRateLimit   `mapstructure:"rate_limit" json:"rateLimit" yaml:"rate_limit"`
	PromptGuard CanvasPromptGuard `mapstructure:"prompt_guard" json:"promptGuard" yaml:"prompt_guard"`
}

// CanvasPromptGuard configures the §12.3 prompt guard at startup. Empty
// fields fall back to the built-in defaults inside the canvas service —
// a YAML deploy can ship just the SensitiveWords list and inherit the
// rest. The list itself is intentionally not bundled into the binary so
// the source of truth for sensitive content stays with ops.
type CanvasPromptGuard struct {
	// MaxPromptLen caps the user prompt at this many runes; longer
	// inputs get truncated with a warning. Zero → service default.
	MaxPromptLen int `mapstructure:"max_prompt_len" json:"maxPromptLen" yaml:"max_prompt_len"`
	// MaxNegativePromptLen does the same for negative prompts.
	MaxNegativePromptLen int `mapstructure:"max_negative_prompt_len" json:"maxNegativePromptLen" yaml:"max_negative_prompt_len"`
	// SensitiveWords trips a hard reject when any of these substrings
	// appears (case-insensitive) in prompt OR negative prompt. Empty
	// list means the reject path is inert — the guard still truncates
	// long inputs and warns on injection patterns.
	SensitiveWords []string `mapstructure:"sensitive_words" json:"sensitiveWords" yaml:"sensitive_words"`
	// BlockedPatterns are regexes that warn (not reject) on match —
	// injection heuristics. Empty list → service-default patterns.
	BlockedPatterns []string `mapstructure:"blocked_patterns" json:"blockedPatterns" yaml:"blocked_patterns"`
}

// CanvasRateLimit holds per-operation buckets plus a global concurrency cap.
//
// Buckets are keyed by semantic operation, not URL. `Chat`, `Agent`,
// `Quote`, and `Generation` map onto routes in canvas_router.go via the
// UserLimiter middleware constructor.
type CanvasRateLimit struct {
	Chat       CanvasRateLimitBucket `mapstructure:"chat" json:"chat" yaml:"chat"`
	Agent      CanvasRateLimitBucket `mapstructure:"agent" json:"agent" yaml:"agent"`
	Quote      CanvasRateLimitBucket `mapstructure:"quote" json:"quote" yaml:"quote"`
	Generation CanvasRateLimitBucket `mapstructure:"generation" json:"generation" yaml:"generation"`
	// GlobalConcurrent caps total in-flight slots across all buckets. Zero
	// disables the gate.
	GlobalConcurrent int `mapstructure:"global_concurrent" json:"globalConcurrent" yaml:"global_concurrent"`
}

// CanvasRateLimitBucket configures a single per-user bucket.
//
// PerMinute is a fixed-window counter; PerUserConcurrent gates in-flight
// requests per user. Zero on either disables that dimension.
type CanvasRateLimitBucket struct {
	PerMinute         int `mapstructure:"per_minute" json:"perMinute" yaml:"per_minute"`
	PerUserConcurrent int `mapstructure:"per_user_concurrent" json:"perUserConcurrent" yaml:"per_user_concurrent"`
}

// canvas default buckets match §12.2 of the design doc. They are applied at
// the middleware layer when the YAML leaves a bucket unset, so a fresh
// deploy gets sensible defaults without needing a config push.
var canvasDefaults = CanvasRateLimit{
	Chat:             CanvasRateLimitBucket{PerMinute: 30, PerUserConcurrent: 3},
	Agent:            CanvasRateLimitBucket{PerMinute: 20, PerUserConcurrent: 2},
	Quote:            CanvasRateLimitBucket{PerMinute: 120, PerUserConcurrent: 6},
	Generation:       CanvasRateLimitBucket{PerMinute: 60, PerUserConcurrent: 5},
	GlobalConcurrent: 200,
}

// CanvasDefaults returns the default rate-limit shape. Callers merge the
// YAML over this so any omitted field falls back to the built-in value.
func CanvasDefaults() CanvasRateLimit {
	return canvasDefaults
}

// Resolve returns the effective bucket for op, applying defaults where
// YAML left a field zero. Unknown op strings fall back to Chat defaults so
// a mis-wired route is rate-limited rather than wide open.
func (c CanvasRateLimit) Resolve(op string) CanvasRateLimitBucket {
	var (
		user    CanvasRateLimitBucket
		defBkt  CanvasRateLimitBucket
	)
	switch op {
	case "chat":
		user, defBkt = c.Chat, canvasDefaults.Chat
	case "agent":
		user, defBkt = c.Agent, canvasDefaults.Agent
	case "quote":
		user, defBkt = c.Quote, canvasDefaults.Quote
	case "generation":
		user, defBkt = c.Generation, canvasDefaults.Generation
	default:
		user, defBkt = c.Chat, canvasDefaults.Chat
	}
	if user.PerMinute <= 0 {
		user.PerMinute = defBkt.PerMinute
	}
	if user.PerUserConcurrent <= 0 {
		user.PerUserConcurrent = defBkt.PerUserConcurrent
	}
	return user
}

// EffectiveGlobalConcurrent returns the global cap, falling back to the
// default when YAML leaves it zero.
func (c CanvasRateLimit) EffectiveGlobalConcurrent() int {
	if c.GlobalConcurrent > 0 {
		return c.GlobalConcurrent
	}
	return canvasDefaults.GlobalConcurrent
}
