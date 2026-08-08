// Package canvas hosts Canvas-specific service primitives that sit
// below the API layer. PromptGuard is the first resident — it enforces
// the §12.3 rules from canvas-tool-design.md (length caps, injection
// heuristics, sensitive-word rejection).
package canvas

import (
	"regexp"
	"strings"
	"sync/atomic"
	"unicode/utf8"
)

// Guard action outcomes. These map to the design doc's `action` field on
// GuardResult.
const (
	GuardActionAllow    = "allow"
	GuardActionTruncate = "truncate"
	GuardActionReject   = "reject"
)

// Reject reason codes surfaced to clients. The API layer converts these
// to HTTP errors with matching error codes.
const (
	RejectReasonPromptTooLong   = "CANVAS_VALIDATE_PROMPT_TOO_LONG"
	RejectReasonNegativeTooLong = "CANVAS_VALIDATE_NEGATIVE_TOO_LONG"
	RejectReasonSensitive       = "CANVAS_VALIDATE_SENSITIVE_CONTENT"
)

// Warning codes — surfaced in responses but do not block the request.
const (
	WarningPromptTruncated   = "CANVAS_PROMPT_TRUNCATED"
	WarningNegativeTruncated = "CANVAS_NEGATIVE_TRUNCATED"
	WarningInjectionSuspect  = "CANVAS_PROMPT_INJECTION_SUSPECT"
)

// GuardInput is what callers hand to PromptGuard.Check. Prompt is
// required; NegativePrompt may be empty.
type GuardInput struct {
	Prompt         string
	NegativePrompt string
}

// GuardResult tells the caller what to do.
//
//   - Action == GuardActionReject: RejectReason is populated; caller must
//     abort the request before reserving credits.
//   - Action == GuardActionAllow | GuardActionTruncate: CleanedPrompt and
//     CleanedNegativePrompt contain the text to forward to the LLM.
//     Warnings may list non-blocking hits (e.g., injection suspect, prompt
//     was truncated).
type GuardResult struct {
	Action                string
	RejectReason          string
	CleanedPrompt         string
	CleanedNegativePrompt string
	PromptLen             int
	NegativeLen           int
	Warnings              []string
}

// Config is the knob surface for PromptGuard. Zero values get swapped
// for the defaults documented in §12.3.
type Config struct {
	MaxPromptLen         int
	MaxNegativePromptLen int
	BlockedPatterns      []string
	SensitiveWords       []string
}

// Defaults returns the §12.3 defaults. Kept as a function (not a var)
// so callers cannot accidentally mutate the backing slices.
func Defaults() Config {
	return Config{
		MaxPromptLen:         8000,
		MaxNegativePromptLen: 2000,
		// Injection heuristics from §12.3. These are intentionally
		// conservative — they only warn, never reject, because false
		// positives on creative prompts are common.
		BlockedPatterns: []string{
			`(?i)^\s*(ignore|forget|disregard)\s+(previous|prior|above|all).*(instruction|prompt|rule)`,
			`(?i)\b(system|developer|assistant)\s*:\s*`,
			`(?i)\b(new\s+instruction|override\s+instruction|reveal\s+(system|your)\s+prompt)\b`,
		},
		// No sensitive words baked in by default — ops loads these from
		// a word list at startup. Tests inject values directly.
		SensitiveWords: nil,
	}
}

// guardStats tracks lifetime per-PromptGuard counters. All fields are
// atomic — Check is called concurrently across canvas surfaces (agent,
// optimize, etc.) and the snapshot must be safe to read without holding
// a lock. Increments are best-effort observability; a missed read of
// stats.Load() during a hot burst is acceptable as long as the count
// monotonically reflects every Check call once it eventually settles.
type guardStats struct {
	checks            atomic.Uint64 // total Check calls (allow + reject + truncate union)
	allowed           atomic.Uint64 // Check returned GuardActionAllow with no warnings
	truncatedPrompt   atomic.Uint64 // Prompt exceeded MaxPromptLen
	truncatedNegative atomic.Uint64 // NegativePrompt exceeded MaxNegativePromptLen
	rejectedSensitive atomic.Uint64 // GuardActionReject via SensitiveWords hit
	warnedInjection   atomic.Uint64 // BlockedPatterns matched on Prompt
}

// GuardStats is the public snapshot of guardStats. Returned by
// PromptGuard.Stats. Callers (admin endpoints, periodic log dump, future
// /metrics scrape) treat each Read as an atomic point-in-time view; the
// underlying counters keep advancing as new Check calls land.
//
// Fields are non-overlapping for "did the request finish in this state"
// EXCEPT for warnedInjection — a truncate result can also carry an
// injection warning, so Truncated* + WarnedInjection together can exceed
// the Checks total. Allowed counts only the clean-allow path (no
// warnings, no truncation).
type GuardStats struct {
	Checks            uint64
	Allowed           uint64
	TruncatedPrompt   uint64
	TruncatedNegative uint64
	RejectedSensitive uint64
	WarnedInjection   uint64
}

// PromptGuard is the stateful validator. Safe to hold as a package-level
// singleton; Check is read-only after construction (except for the
// best-effort atomic stats counters).
type PromptGuard struct {
	maxPromptLen         int
	maxNegativePromptLen int
	blocked              []*regexp.Regexp
	sensitive            []string
	// rawConfig holds the un-normalised cfg the guard was built with.
	// Returned by Snapshot() so the admin reload endpoint can preserve
	// other knobs when ops only wants to replace SensitiveWords.
	rawConfig Config
	stats     guardStats
}

// Snapshot returns the Config the guard was constructed from, mirroring
// `New(cfg)`'s input. Used by the admin reload endpoint so a partial
// update (e.g. sensitive_words only) can merge against the live config.
// Returns a zero value when the receiver is nil.
func (g *PromptGuard) Snapshot() Config {
	if g == nil {
		return Config{}
	}
	// Deep-copy slices so callers can't mutate the live config.
	cfg := g.rawConfig
	if len(cfg.SensitiveWords) > 0 {
		cfg.SensitiveWords = append([]string(nil), cfg.SensitiveWords...)
	}
	if len(cfg.BlockedPatterns) > 0 {
		cfg.BlockedPatterns = append([]string(nil), cfg.BlockedPatterns...)
	}
	return cfg
}

// SensitiveWordCount returns the number of normalised sensitive words
// the guard currently holds — useful for the admin /stats and reload
// endpoints to confirm the latest config landed.
func (g *PromptGuard) SensitiveWordCount() int {
	if g == nil {
		return 0
	}
	return len(g.sensitive)
}

// New builds a PromptGuard from cfg. Missing fields fall back to the
// values in Defaults(). An invalid regex in cfg.BlockedPatterns is
// silently dropped — we prefer to keep the guard running rather than
// panic at startup.
func New(cfg Config) *PromptGuard {
	def := Defaults()
	if cfg.MaxPromptLen <= 0 {
		cfg.MaxPromptLen = def.MaxPromptLen
	}
	if cfg.MaxNegativePromptLen <= 0 {
		cfg.MaxNegativePromptLen = def.MaxNegativePromptLen
	}
	if cfg.BlockedPatterns == nil {
		cfg.BlockedPatterns = def.BlockedPatterns
	}

	compiled := make([]*regexp.Regexp, 0, len(cfg.BlockedPatterns))
	for _, p := range cfg.BlockedPatterns {
		if re, err := regexp.Compile(p); err == nil {
			compiled = append(compiled, re)
		}
	}

	// Case-fold sensitive words once so Check is a cheap contains loop.
	sens := make([]string, 0, len(cfg.SensitiveWords))
	for _, w := range cfg.SensitiveWords {
		w = strings.ToLower(strings.TrimSpace(w))
		if w != "" {
			sens = append(sens, w)
		}
	}

	return &PromptGuard{
		maxPromptLen:         cfg.MaxPromptLen,
		maxNegativePromptLen: cfg.MaxNegativePromptLen,
		blocked:              compiled,
		sensitive:            sens,
		rawConfig:            cfg,
	}
}

// Check applies the §12.3 rules in order: sensitive-word reject,
// length-based truncate, injection-suspect warn. Sensitive hits are
// terminal — we never truncate past a sensitive word because the prefix
// may itself be unsafe.
//
// Also stamps best-effort atomic counters on the PromptGuard for
// observability — see Stats() for the snapshot shape.
func (g *PromptGuard) Check(in GuardInput) GuardResult {
	res := GuardResult{
		Action:                GuardActionAllow,
		CleanedPrompt:         in.Prompt,
		CleanedNegativePrompt: in.NegativePrompt,
		PromptLen:             utf8.RuneCountInString(in.Prompt),
		NegativeLen:           utf8.RuneCountInString(in.NegativePrompt),
	}

	if g == nil {
		return res
	}

	g.stats.checks.Add(1)

	// 1. Sensitive-word scan. Case-insensitive substring — cheap and
	//    good enough for the startup lexicon size (~hundreds to low
	//    thousands of terms).
	if len(g.sensitive) > 0 {
		haystack := strings.ToLower(in.Prompt + "\n" + in.NegativePrompt)
		for _, w := range g.sensitive {
			if strings.Contains(haystack, w) {
				res.Action = GuardActionReject
				res.RejectReason = RejectReasonSensitive
				g.stats.rejectedSensitive.Add(1)
				return res
			}
		}
	}

	// 2. Length rules. Truncate rather than reject — creative users hit
	//    the cap often, and silently shrinking is less disruptive than
	//    rejecting a 30-min paste.
	if g.maxPromptLen > 0 && res.PromptLen > g.maxPromptLen {
		res.CleanedPrompt = truncateRunes(in.Prompt, g.maxPromptLen)
		res.Action = GuardActionTruncate
		res.Warnings = append(res.Warnings, WarningPromptTruncated)
		g.stats.truncatedPrompt.Add(1)
	}
	if g.maxNegativePromptLen > 0 && res.NegativeLen > g.maxNegativePromptLen {
		res.CleanedNegativePrompt = truncateRunes(in.NegativePrompt, g.maxNegativePromptLen)
		res.Action = GuardActionTruncate
		res.Warnings = append(res.Warnings, WarningNegativeTruncated)
		g.stats.truncatedNegative.Add(1)
	}

	// 3. Injection heuristics — warn, don't block. The LLM system
	//    prompt handles most of these; this is defense in depth.
	if len(g.blocked) > 0 {
		for _, re := range g.blocked {
			if re.MatchString(in.Prompt) {
				res.Warnings = append(res.Warnings, WarningInjectionSuspect)
				g.stats.warnedInjection.Add(1)
				break
			}
		}
	}

	// Allowed counts ONLY the clean-allow path (no warnings, no
	// truncate). A truncate-with-injection-warning increments both
	// truncatedPrompt and warnedInjection but NOT allowed — that lets
	// callers compute "clean rate" as allowed/checks.
	if res.Action == GuardActionAllow && len(res.Warnings) == 0 {
		g.stats.allowed.Add(1)
	}

	return res
}

// Stats returns a point-in-time snapshot of the PromptGuard's lifetime
// counters. Safe to call concurrently; each Load is atomic but the
// snapshot is NOT a transactionally consistent view across fields —
// counters may advance between the per-field Loads. Acceptable for
// observability (a Grafana dashboard or admin /stats endpoint); not
// acceptable for invariant-style assertions.
func (g *PromptGuard) Stats() GuardStats {
	if g == nil {
		return GuardStats{}
	}
	return GuardStats{
		Checks:            g.stats.checks.Load(),
		Allowed:           g.stats.allowed.Load(),
		TruncatedPrompt:   g.stats.truncatedPrompt.Load(),
		TruncatedNegative: g.stats.truncatedNegative.Load(),
		RejectedSensitive: g.stats.rejectedSensitive.Load(),
		WarnedInjection:   g.stats.warnedInjection.Load(),
	}
}

// truncateRunes returns the first n runes of s. Never splits a
// multi-byte rune, unlike a naive s[:n].
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}

// ── Default instance wiring ────────────────────────────────────────────

// defaultGuard holds the process-wide PromptGuard. atomic.Pointer instead
// of sync.Once so that startup wiring (initialize.OtherInit → Configure)
// can swap the singleton from the empty Defaults() into a config-driven
// build that carries the operator's SensitiveWords list.
var defaultGuard atomic.Pointer[PromptGuard]

// Default returns the process-wide PromptGuard. If Configure has run, it
// returns that build; otherwise it lazy-installs one from Defaults() so
// callers in tests / one-off tools never see a nil guard.
func Default() *PromptGuard {
	if g := defaultGuard.Load(); g != nil {
		return g
	}
	fallback := New(Defaults())
	if defaultGuard.CompareAndSwap(nil, fallback) {
		return fallback
	}
	return defaultGuard.Load()
}

// Configure installs cfg as the process-wide guard. Designed to be called
// once during server startup (initialize.OtherInit), before any request
// handler runs. Subsequent Default() calls return the configured guard.
// Safe to call multiple times — later calls win, useful for hot-reload
// of the sensitive-word list.
func Configure(cfg Config) {
	defaultGuard.Store(New(cfg))
}
