package config

import "testing"

// canvas_test.go pins the headline contract (default values match the
// design doc, Resolve picks the matching bucket, unknown-op falls back
// to Chat, zero/negative fields get filled, GlobalConcurrent fallback).
// These fill the quieter gate invariants a silent regression would slip
// past:
//
//   • Resolve unknown-op branch takes `user = c.Chat`, NOT
//     `canvasDefaults.Chat`. So when the user HAS customised Chat in
//     YAML, an unknown op uses the USER'S Chat settings (which then
//     fall through the same zero-fill pass). Pin this subtle
//     observable — a refactor that made unknown-op always use
//     canvasDefaults.Chat would diverge for users who tightened Chat
//     (e.g., Chat=10/1 in YAML → unknown-op gets 10/1, not the
//     design-doc 30/3).
//   • The op-name match is BYTE-exact and CASE-SENSITIVE: "Chat",
//     "CHAT", " chat", "chat\n", non-ASCII ops all route to default.
//     Pin so a refactor that added ToLower / TrimSpace would surface.
//     Security-relevant: typo'd route names in middleware wiring MUST
//     fall back to rate-limited default, never wide open.
//   • Resolve returns a VALUE: mutating the returned bucket does not
//     leak into the receiver or the package-level canvasDefaults.
//     Pin the no-alias observable.
//   • Resolve is side-effect free on the receiver: passing a zero
//     CanvasRateLimit to Resolve("chat") must not populate the
//     receiver's Chat field. Pin so a refactor to pointer-receiver
//     Resolve would surface.
//   • Zero-fill gate is `<= 0` — so exactly-1 passes (1/1 survives
//     Resolve). Pin the minimum positive boundary so a refactor to
//     `< 1` or `< 2` would surface.
//   • Per-bucket YAML isolation: setting Agent custom values but
//     leaving Chat zero means Resolve("chat") returns Chat defaults
//     (30/3), NOT Agent's custom values. Pin cross-bucket isolation.
//   • EffectiveGlobalConcurrent boundary: exactly-1 passes the `> 0`
//     gate and returns 1 (no floor). MaxInt values pass verbatim
//     (no ceiling). Pin so an introduced safety clamp would surface.
//   • canvasDefaults package-level var is not mutated by any call
//     chain — repeated CanvasDefaults() + Resolve() calls still
//     return the original shipping values. Pin invariance.

func TestCanvasRateLimit_Resolve_UnknownOpUsesUserChatNotCanvasDefaults(t *testing.T) {
	// User tightened Chat to 10/1. An unknown op falls back to
	// Chat BUT it's the USER's Chat (10/1), not canvasDefaults.Chat
	// (30/3). If user Chat had zeros, those zeros then default-fill
	// from canvasDefaults.Chat.
	rl := CanvasRateLimit{
		Chat: CanvasRateLimitBucket{PerMinute: 10, PerUserConcurrent: 1},
	}
	got := rl.Resolve("totally-unknown")
	if got.PerMinute != 10 || got.PerUserConcurrent != 1 {
		t.Errorf("unknown op should use user's Chat (10/1), got %d/%d", got.PerMinute, got.PerUserConcurrent)
	}
}

func TestCanvasRateLimit_Resolve_OpMatchIsCaseSensitive(t *testing.T) {
	// Canonical op is lowercase — any case variant routes to default
	// branch. Pin so a refactor adding ToLower would surface.
	rl := CanvasRateLimit{
		Chat:       CanvasRateLimitBucket{PerMinute: 10, PerUserConcurrent: 1},
		Generation: CanvasRateLimitBucket{PerMinute: 77, PerUserConcurrent: 9},
	}
	// These should all fall to the default branch (Chat).
	for _, op := range []string{"Chat", "CHAT", "ChAt", "Generation", "GENERATION"} {
		got := rl.Resolve(op)
		if got.PerMinute != 10 || got.PerUserConcurrent != 1 {
			t.Errorf("op=%q should case-miss and fall to Chat (10/1), got %d/%d",
				op, got.PerMinute, got.PerUserConcurrent)
		}
	}
}

func TestCanvasRateLimit_Resolve_WhitespacePaddedOpFallsThrough(t *testing.T) {
	// " chat" / "chat\n" / "chat " aren't byte-exact matches — they
	// fall to the default branch. Pin so a refactor adding TrimSpace
	// would surface (typo'd wiring MUST not bypass limiter).
	rl := CanvasRateLimit{
		Chat: CanvasRateLimitBucket{PerMinute: 10, PerUserConcurrent: 1},
	}
	for _, op := range []string{" chat", "chat ", "chat\n", "\tchat"} {
		got := rl.Resolve(op)
		// Falls to default branch → uses c.Chat (10/1).
		if got.PerMinute != 10 || got.PerUserConcurrent != 1 {
			t.Errorf("op=%q must fall through default branch; got %d/%d", op, got.PerMinute, got.PerUserConcurrent)
		}
	}
}

func TestCanvasRateLimit_Resolve_NonASCIIOpFallsThrough(t *testing.T) {
	// Security: a non-ASCII op string must NOT match any case — it
	// falls through to the default (rate-limited) branch. Pin so a
	// refactor that used a fuzzy match (e.g., substring / Unicode
	// folding) wouldn't silently accept look-alikes.
	rl := CanvasRateLimit{}
	got := rl.Resolve("聊天") // "chat" in Chinese
	// Empty receiver → default branch → Chat → zero-fill from
	// canvasDefaults.Chat (30/3).
	if got.PerMinute != 30 || got.PerUserConcurrent != 3 {
		t.Errorf("non-ASCII op must fall to default (30/3), got %d/%d", got.PerMinute, got.PerUserConcurrent)
	}
}

func TestCanvasRateLimit_Resolve_EmptyStringOpFallsThrough(t *testing.T) {
	// Literal "" routes to default branch. Pin — a refactor that
	// added an early-return for "" would silently change the shape.
	rl := CanvasRateLimit{}
	got := rl.Resolve("")
	if got.PerMinute != 30 || got.PerUserConcurrent != 3 {
		t.Errorf("empty op should fall to Chat defaults; got %d/%d", got.PerMinute, got.PerUserConcurrent)
	}
}

func TestCanvasRateLimit_Resolve_ReceiverNotMutated(t *testing.T) {
	// Resolve has a value receiver — calls must not mutate the
	// receiver or the package-level canvasDefaults. Pin so a refactor
	// to a pointer receiver that accidentally wrote back would
	// surface (and corrupt shared config across goroutines).
	rl := CanvasRateLimit{
		Chat: CanvasRateLimitBucket{PerMinute: 0, PerUserConcurrent: 0},
	}
	_ = rl.Resolve("chat")
	if rl.Chat.PerMinute != 0 || rl.Chat.PerUserConcurrent != 0 {
		t.Errorf("Resolve mutated the receiver's Chat; got {%d,%d}",
			rl.Chat.PerMinute, rl.Chat.PerUserConcurrent)
	}
	// And canvasDefaults is untouched.
	if canvasDefaults.Chat.PerMinute != 30 || canvasDefaults.Chat.PerUserConcurrent != 3 {
		t.Fatalf("canvasDefaults.Chat was mutated: %+v", canvasDefaults.Chat)
	}
}

func TestCanvasRateLimit_Resolve_ReturnsByValueNoAlias(t *testing.T) {
	// Mutating the returned bucket does not affect canvasDefaults or
	// subsequent Resolve calls. Pin.
	rl := CanvasRateLimit{}
	got := rl.Resolve("chat")
	got.PerMinute = 9999
	got.PerUserConcurrent = 8888
	fresh := rl.Resolve("chat")
	if fresh.PerMinute != 30 || fresh.PerUserConcurrent != 3 {
		t.Errorf("mutating returned bucket leaked; fresh={%d,%d}", fresh.PerMinute, fresh.PerUserConcurrent)
	}
}

func TestCanvasRateLimit_Resolve_ExactlyOneSurvivesGate(t *testing.T) {
	// Gate is `<= 0` — value 1 passes untouched. Pin the minimum
	// positive boundary so a refactor to `< 1` or `< 2` would flip.
	rl := CanvasRateLimit{
		Chat: CanvasRateLimitBucket{PerMinute: 1, PerUserConcurrent: 1},
	}
	got := rl.Resolve("chat")
	if got.PerMinute != 1 || got.PerUserConcurrent != 1 {
		t.Errorf("value 1 should survive the gate; got %d/%d", got.PerMinute, got.PerUserConcurrent)
	}
}

func TestCanvasRateLimit_Resolve_PerBucketIsolation(t *testing.T) {
	// Setting Agent customs must NOT affect Resolve("chat"). Pin the
	// per-bucket YAML isolation so a refactor that shared user-state
	// across buckets would surface.
	rl := CanvasRateLimit{
		Agent: CanvasRateLimitBucket{PerMinute: 777, PerUserConcurrent: 9},
	}
	got := rl.Resolve("chat")
	if got.PerMinute != 30 || got.PerUserConcurrent != 3 {
		t.Errorf("Chat should use defaults (30/3) despite Agent customs; got %d/%d",
			got.PerMinute, got.PerUserConcurrent)
	}
}

func TestCanvasRateLimit_EffectiveGlobalConcurrent_ExactlyOneSurvives(t *testing.T) {
	// Gate is `> 0` — exactly 1 passes verbatim, no floor applied.
	got := (CanvasRateLimit{GlobalConcurrent: 1}).EffectiveGlobalConcurrent()
	if got != 1 {
		t.Errorf("GlobalConcurrent=1 should survive, got %d", got)
	}
}

func TestCanvasRateLimit_EffectiveGlobalConcurrent_LargeValueSurvives(t *testing.T) {
	// No upper clamp — arbitrarily-large values pass verbatim. Pin so
	// a refactor that added a safety ceiling would surface as a
	// deliberate change.
	got := (CanvasRateLimit{GlobalConcurrent: 1_000_000}).EffectiveGlobalConcurrent()
	if got != 1_000_000 {
		t.Errorf("large GlobalConcurrent should survive, got %d", got)
	}
}

func TestCanvasDefaults_InvariantAcrossCalls(t *testing.T) {
	// Sanity: repeated calls return bytes-identical values (by-value
	// return, but also a pin against any future "lazy init" refactor
	// that might produce divergent copies).
	a := CanvasDefaults()
	b := CanvasDefaults()
	if a != b {
		t.Errorf("CanvasDefaults drift between calls; a=%+v b=%+v", a, b)
	}
}

func TestCanvasDefaults_UntouchedByResolveCallChain(t *testing.T) {
	// Run a bunch of Resolve calls with zero-filled user and verify
	// canvasDefaults is unchanged at the end. Pin so a future refactor
	// that cached or mutated canvasDefaults in the hot path would
	// surface as shared-state corruption.
	rl := CanvasRateLimit{}
	for _, op := range []string{"chat", "agent", "quote", "generation", "unknown"} {
		_ = rl.Resolve(op)
	}
	if canvasDefaults.Chat.PerMinute != 30 ||
		canvasDefaults.Agent.PerMinute != 20 ||
		canvasDefaults.Quote.PerMinute != 120 ||
		canvasDefaults.Generation.PerMinute != 60 ||
		canvasDefaults.GlobalConcurrent != 200 {
		t.Fatalf("canvasDefaults drifted after Resolve calls: %+v", canvasDefaults)
	}
}
