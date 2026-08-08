package config

import "testing"

// canvas.go defines the runtime shape of Canvas rate limits and, more
// importantly, the default values quoted in §12.2 of the canvas design
// doc. If someone widens Chat from 30/min to 300/min by accident, the
// production rate limiter silently loosens and a single user can burn
// downstream quotas. These tests pin the exact defaults AND the
// Resolve() fallback behaviour that's load-bearing for security.

func TestCanvasDefaults_MatchDesignDocValues(t *testing.T) {
	// §12.2 quotes these as the shipping defaults. If these numbers
	// change, the design doc needs to change with them — that's the
	// invariant this test is here to enforce.
	d := CanvasDefaults()
	cases := []struct {
		name    string
		bucket  CanvasRateLimitBucket
		perMin  int
		concurr int
	}{
		{"chat", d.Chat, 30, 3},
		{"agent", d.Agent, 20, 2},
		{"quote", d.Quote, 120, 6},
		{"generation", d.Generation, 60, 5},
	}
	for _, c := range cases {
		if c.bucket.PerMinute != c.perMin {
			t.Errorf("%s.PerMinute = %d, want %d", c.name, c.bucket.PerMinute, c.perMin)
		}
		if c.bucket.PerUserConcurrent != c.concurr {
			t.Errorf("%s.PerUserConcurrent = %d, want %d", c.name, c.bucket.PerUserConcurrent, c.concurr)
		}
	}
	if d.GlobalConcurrent != 200 {
		t.Errorf("GlobalConcurrent default = %d, want 200", d.GlobalConcurrent)
	}
}

func TestCanvasDefaults_ReturnsCopyNotAlias(t *testing.T) {
	// CanvasDefaults returns by value — callers mutating the result must
	// not leak into the package-level canvasDefaults var. If it ever
	// changes to return a pointer, the per-request merge in Resolve()
	// would become a shared-memory race.
	d := CanvasDefaults()
	d.Chat.PerMinute = 999
	if CanvasDefaults().Chat.PerMinute != 30 {
		t.Fatalf("mutating the returned value leaked into canvasDefaults")
	}
}

func TestCanvasRateLimit_Resolve_PicksMatchingBucketByOp(t *testing.T) {
	// A fully-configured rate-limit — no zero-filling should kick in.
	rl := CanvasRateLimit{
		Chat:       CanvasRateLimitBucket{PerMinute: 10, PerUserConcurrent: 1},
		Agent:      CanvasRateLimitBucket{PerMinute: 11, PerUserConcurrent: 2},
		Quote:      CanvasRateLimitBucket{PerMinute: 12, PerUserConcurrent: 3},
		Generation: CanvasRateLimitBucket{PerMinute: 13, PerUserConcurrent: 4},
	}
	for _, c := range []struct {
		op      string
		wantMin int
		wantCon int
	}{
		{"chat", 10, 1},
		{"agent", 11, 2},
		{"quote", 12, 3},
		{"generation", 13, 4},
	} {
		got := rl.Resolve(c.op)
		if got.PerMinute != c.wantMin || got.PerUserConcurrent != c.wantCon {
			t.Errorf("Resolve(%q) = {%d,%d}, want {%d,%d}",
				c.op, got.PerMinute, got.PerUserConcurrent, c.wantMin, c.wantCon)
		}
	}
}

func TestCanvasRateLimit_Resolve_UnknownOpFallsBackToChatDefaults(t *testing.T) {
	// Security invariant from the design doc: an unknown op must NOT
	// bypass rate limiting. It falls back to Chat defaults. Rephrased:
	// a mis-wired route gets rate-limited, not wide open. If someone
	// ever changes the default branch to `return CanvasRateLimitBucket{}`,
	// this test flags it.
	rl := CanvasRateLimit{} // empty — forces full default-fill path
	got := rl.Resolve("totally-unknown-op")
	// Chat defaults are 30/3 — must surface here.
	if got.PerMinute != 30 || got.PerUserConcurrent != 3 {
		t.Errorf("unknown op should fall back to Chat defaults (30/3), got %d/%d",
			got.PerMinute, got.PerUserConcurrent)
	}
}

func TestCanvasRateLimit_Resolve_FillsZeroFieldsFromDefaults(t *testing.T) {
	// YAML might specify only PerMinute and leave PerUserConcurrent
	// empty (or vice versa). The per-field zero check must fill ONLY
	// the missing field, not clobber the user's explicit value.
	rl := CanvasRateLimit{
		Chat: CanvasRateLimitBucket{PerMinute: 99}, // concurrent left zero
	}
	got := rl.Resolve("chat")
	if got.PerMinute != 99 {
		t.Errorf("explicit PerMinute=99 was clobbered: got %d", got.PerMinute)
	}
	if got.PerUserConcurrent != 3 {
		t.Errorf("zero PerUserConcurrent should default to Chat=3, got %d", got.PerUserConcurrent)
	}

	rl2 := CanvasRateLimit{
		Generation: CanvasRateLimitBucket{PerUserConcurrent: 7}, // per-minute left zero
	}
	got = rl2.Resolve("generation")
	if got.PerMinute != 60 {
		t.Errorf("zero PerMinute should default to Generation=60, got %d", got.PerMinute)
	}
	if got.PerUserConcurrent != 7 {
		t.Errorf("explicit PerUserConcurrent=7 was clobbered: got %d", got.PerUserConcurrent)
	}
}

func TestCanvasRateLimit_Resolve_NegativeOrZeroCountsAsMissing(t *testing.T) {
	// The guard uses `<= 0` not `== 0`. A negative value in YAML is
	// nonsensical but must not silently disable the limiter — instead
	// it gets replaced with the default. This matches the design-doc
	// stance that rate limits are always-on.
	rl := CanvasRateLimit{
		Chat: CanvasRateLimitBucket{PerMinute: -5, PerUserConcurrent: -1},
	}
	got := rl.Resolve("chat")
	if got.PerMinute != 30 || got.PerUserConcurrent != 3 {
		t.Errorf("negative values should be treated as missing; got %d/%d, want 30/3",
			got.PerMinute, got.PerUserConcurrent)
	}
}

func TestCanvasRateLimit_EffectiveGlobalConcurrent_FallsBackWhenZero(t *testing.T) {
	// GlobalConcurrent zero → fall back to default (200). A non-zero
	// positive value overrides the default entirely.
	if got := (CanvasRateLimit{}).EffectiveGlobalConcurrent(); got != 200 {
		t.Errorf("zero → default 200, got %d", got)
	}
	if got := (CanvasRateLimit{GlobalConcurrent: 50}).EffectiveGlobalConcurrent(); got != 50 {
		t.Errorf("explicit 50 should override, got %d", got)
	}
	// Negative is treated as "not configured" just like zero — same
	// `> 0` gate as the per-bucket logic above.
	if got := (CanvasRateLimit{GlobalConcurrent: -10}).EffectiveGlobalConcurrent(); got != 200 {
		t.Errorf("negative should fall back to default 200, got %d", got)
	}
}
