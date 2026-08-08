package middleware

import (
	"testing"
	"time"

	"server/config"
)

// user_rate_limit_test.go pins the headline contract (per-minute cap,
// per-user concurrent cap, global cap, per-op isolation, unknown-op
// fallback, Resolve defaults). These fill the quieter gate invariants
// a silent regression would slip past:
//
//   • Guard precedence when MULTIPLE caps would deny simultaneously.
//     Code order: perMinute → perUserConcurrent → globalConcurrent.
//     - perMinute vs perUserConcurrent → perMinute wins (reason:
//       "rate_limit"). Pin so a refactor that reordered (concurrent
//       first) would swap the signalling reason the client sees.
//     - perUserConcurrent vs globalConcurrent → concurrent_limit
//       wins. Pin.
//     The reason string is a stable protocol — telemetry and client
//     retry policy branch on it, so silent swapping would matter.
//
//   • release() double-call clamps to floor, never goes negative:
//     perUserConcurrent has `cur.concurrent > 0` guard, globalInUse
//     has `r.globalInUse > 0` guard. Pin so a refactor that dropped
//     either guard (e.g. trusting exact pairing) would leak negative
//     counters into the "unlimited" regime after the next release.
//
//   • perMinute=0 disables the per-minute gate entirely (unlimited).
//     The `b.perMinute > 0 &&` guard is load-bearing for ops that
//     opt out of minute-counting (historically used for internal
//     ops). Pin so a refactor to `>= 0` or `!= 0` would flip: `!= 0`
//     would still work, but removing the guard entirely would deny
//     the first request (since 0 requests >= 0 cap).
//
//   • perUserConcurrent=0 disables the per-user concurrency gate.
//     Same reasoning as perMinute — pin the "zero = unlimited"
//     convention.
//
//   • globalConcurrent=0 disables the global gate. The base test's
//     GlobalBusy case sets globalConcurrent=1; the zero-case is
//     important because EffectiveGlobalConcurrent defaults to 200 in
//     shipping config, but test registries built without calling
//     Effective can land at 0 and MUST treat that as "no cap".
//
//   • lastSeen is updated on DENIAL branches too — a user who is
//     repeatedly rate-limited must not be reaped by the janitor
//     (which deletes entries with concurrent==0 AND lastSeen older
//     than 30min). Pin so a refactor that only updated lastSeen on
//     the success path would let janitor evict an active-but-throttled
//     user, resetting their window on the next request.
//
//   • retryAfter has a hard floor of 1 second for rate_limit (even
//     when the minute window is < 1s away from rolling over, we
//     return 1). Pin so a refactor that removed the `if retry < 1`
//     clamp would emit Retry-After: 0, which some clients interpret
//     as "retry immediately" and hammer the server.
//
//   • retryAfter for concurrent_limit and global_busy is a constant 1
//     — no heuristic. Pin so a refactor that tried to be clever
//     (e.g., compute from in-flight count) would surface.
//
//   • TWO DIFFERENT UNKNOWN OPS get separate buckets: the map key is
//     the op string as given, not the resolved bucket type. So
//     "op-a" and "op-b" (both resolving to Chat defaults) do NOT
//     share user state. Pin the map-key identity — a refactor that
//     normalised unknown ops into "chat" before lookup would
//     accidentally merge their budgets.
//
//   • GLOBAL cap counts ACROSS OPS for the same user (and across
//     users for the same op — base already has cross-user). Pin
//     that user=1/chat + user=1/agent both count toward
//     globalInUse, so the second call hits global_busy at limit=1.

func TestUserRateLimit_GuardPrecedence_PerMinuteBeatsConcurrent(t *testing.T) {
	// PerMinute=1, PerUserConcurrent=5: after the first call exhausts
	// the minute, a second call would trip BOTH (requests>=1 AND
	// concurrent would've been checked next). Code checks perMinute
	// FIRST — reason must be "rate_limit", not "concurrent_limit".
	cfg := config.CanvasRateLimit{
		Chat: config.CanvasRateLimitBucket{PerMinute: 1, PerUserConcurrent: 5},
	}
	r := newTestRegistry(cfg)

	rel, _, ok, _ := r.acquire(1, "chat")
	if !ok {
		t.Fatal("first acquire should succeed")
	}
	// Don't release — keep the concurrent slot alive.
	_, _, ok, reason := r.acquire(1, "chat")
	if ok {
		t.Fatal("second acquire should deny")
	}
	if reason != "rate_limit" {
		t.Fatalf("reason = %q, want rate_limit (perMinute gate is checked FIRST)", reason)
	}
	rel()
}

func TestUserRateLimit_GuardPrecedence_ConcurrentBeatsGlobal(t *testing.T) {
	// PerUserConcurrent=1, GlobalConcurrent=1: after the first call,
	// a second call hits BOTH. Code checks perUserConcurrent before
	// global. Use PerMinute=100 so that guard doesn't fire first.
	cfg := config.CanvasRateLimit{
		Chat:             config.CanvasRateLimitBucket{PerMinute: 100, PerUserConcurrent: 1},
		GlobalConcurrent: 1,
	}
	r := newTestRegistry(cfg)

	rel, _, ok, _ := r.acquire(1, "chat")
	if !ok {
		t.Fatal("first acquire should succeed")
	}
	_, _, ok, reason := r.acquire(1, "chat")
	if ok {
		t.Fatal("second acquire should deny")
	}
	if reason != "concurrent_limit" {
		t.Fatalf("reason = %q, want concurrent_limit (per-user gate beats global)", reason)
	}
	rel()
}

func TestUserRateLimit_DoubleReleaseClampsAtFloor(t *testing.T) {
	// Call release() twice. The floor guards (cur.concurrent > 0 and
	// r.globalInUse > 0) must prevent negative values. A refactor
	// that trusted exact pairing would let globalInUse go to -1,
	// letting a subsequent acquire under GlobalConcurrent=1 succeed
	// TWICE before hitting the cap (since -1 + 1 = 0 < 1).
	cfg := config.CanvasRateLimit{
		Chat:             config.CanvasRateLimitBucket{PerMinute: 100, PerUserConcurrent: 5},
		GlobalConcurrent: 1,
	}
	r := newTestRegistry(cfg)

	rel, _, ok, _ := r.acquire(1, "chat")
	if !ok {
		t.Fatal("initial acquire should succeed")
	}
	rel()
	rel() // idempotent call — must not decrement past floor.

	// Global slot is free (=0), a fresh acquire must succeed and the
	// FOLLOWING one must hit global_busy at GlobalConcurrent=1.
	rel2, _, ok, _ := r.acquire(2, "chat")
	if !ok {
		t.Fatal("after double-release, fresh acquire should succeed (slot is free)")
	}
	_, _, ok, reason := r.acquire(3, "chat")
	if ok {
		t.Fatal("globalInUse must NOT have gone negative; second concurrent acquire should deny")
	}
	if reason != "global_busy" {
		t.Fatalf("reason = %q, want global_busy", reason)
	}
	rel2()
}

func TestUserRateLimit_PerMinuteZeroDisablesGate(t *testing.T) {
	// PerMinute=0 at the bucket level is load-bearing for "no per-minute
	// limit". We bypass cfg.Resolve (which fills zero with defaults) and
	// inject a raw resolve func so perMinute actually reaches the acquire
	// path as 0 — that's the guard the `b.perMinute > 0` check protects.
	r := &UserRateLimitRegistry{
		buckets:          map[string]*userBucket{},
		globalConcurrent: 10_000,
		resolve: func(op string) config.CanvasRateLimitBucket {
			return config.CanvasRateLimitBucket{PerMinute: 0, PerUserConcurrent: 1000}
		},
	}

	releasers := make([]func(), 0, 50)
	for i := 0; i < 50; i++ {
		rel, _, ok, reason := r.acquire(1, "chat")
		if !ok {
			t.Fatalf("call %d denied with reason %q — PerMinute=0 must be unlimited", i, reason)
		}
		releasers = append(releasers, rel)
	}
	for _, rel := range releasers {
		rel()
	}
}

func TestUserRateLimit_PerUserConcurrentZeroDisablesGate(t *testing.T) {
	// Same motivation: bypass cfg.Resolve's zero-fill so the bucket
	// genuinely has PerUserConcurrent=0 at acquire time.
	r := &UserRateLimitRegistry{
		buckets:          map[string]*userBucket{},
		globalConcurrent: 10_000,
		resolve: func(op string) config.CanvasRateLimitBucket {
			return config.CanvasRateLimitBucket{PerMinute: 10_000, PerUserConcurrent: 0}
		},
	}

	releasers := make([]func(), 0, 50)
	for i := 0; i < 50; i++ {
		rel, _, ok, reason := r.acquire(1, "chat")
		if !ok {
			t.Fatalf("concurrent acquire %d denied with %q — PerUserConcurrent=0 must be unlimited", i, reason)
		}
		releasers = append(releasers, rel)
	}
	for _, rel := range releasers {
		rel()
	}
}

func TestUserRateLimit_GlobalZeroDisablesGate(t *testing.T) {
	// Registry built with GlobalConcurrent=0 (e.g. via
	// EffectiveGlobalConcurrent on a zero-config in some test shapes)
	// must treat that as "no global cap". Run many concurrent acquires
	// across users — all must succeed.
	cfg := config.CanvasRateLimit{
		Chat:             config.CanvasRateLimitBucket{PerMinute: 10_000, PerUserConcurrent: 1000},
		GlobalConcurrent: 0,
	}
	r := &UserRateLimitRegistry{
		buckets:          map[string]*userBucket{},
		globalConcurrent: 0, // deliberately zero; bypass EffectiveGlobalConcurrent floor
		resolve:          cfg.Resolve,
	}

	releasers := make([]func(), 0, 50)
	for u := uint(1); u <= 50; u++ {
		rel, _, ok, reason := r.acquire(u, "chat")
		if !ok {
			t.Fatalf("user=%d denied with %q — globalConcurrent=0 must be unlimited", u, reason)
		}
		releasers = append(releasers, rel)
	}
	for _, rel := range releasers {
		rel()
	}
}

func TestUserRateLimit_LastSeenUpdatedOnDenialBranches(t *testing.T) {
	// A user who is repeatedly rate-limited MUST have lastSeen updated
	// on each denial so the janitor doesn't reap them. Probe by
	// reading the internal userState through the bucket map right
	// after a denial and verifying lastSeen advanced.
	cfg := config.CanvasRateLimit{
		Chat: config.CanvasRateLimitBucket{PerMinute: 1, PerUserConcurrent: 5},
	}
	r := newTestRegistry(cfg)

	rel, _, ok, _ := r.acquire(1, "chat")
	if !ok {
		t.Fatal("first acquire should succeed")
	}
	// Capture the lastSeen snapshot after the success acquire.
	firstSeen := r.buckets["chat"].users[1].lastSeen
	// Sleep a couple of ms so a post-denial update would yield a
	// visibly later timestamp.
	time.Sleep(5 * time.Millisecond)

	_, _, ok, _ = r.acquire(1, "chat") // denied on rate_limit
	if ok {
		t.Fatal("second acquire should have been denied")
	}
	secondSeen := r.buckets["chat"].users[1].lastSeen
	if !secondSeen.After(firstSeen) {
		t.Errorf("lastSeen did not advance on rate_limit denial: %v -> %v", firstSeen, secondSeen)
	}
	rel()
}

func TestUserRateLimit_RetryAfterHasFloorOfOne(t *testing.T) {
	// Regardless of how close the minute is to rolling over, retryAfter
	// must be >= 1 second. (The window-boundary `time.Until(next)`
	// computation can yield 0 or fractional values if called in the
	// final millisecond.)
	cfg := config.CanvasRateLimit{
		Chat: config.CanvasRateLimitBucket{PerMinute: 1, PerUserConcurrent: 5},
	}
	r := newTestRegistry(cfg)

	rel, _, ok, _ := r.acquire(1, "chat")
	if !ok {
		t.Fatal("first acquire should succeed")
	}
	// Run the denial many times across the minute — at least one
	// sample should be stress-closest to the boundary.
	for i := 0; i < 5; i++ {
		_, retry, ok, reason := r.acquire(1, "chat")
		if ok {
			t.Fatal("expected rate_limit denial")
		}
		if reason != "rate_limit" {
			t.Fatalf("reason = %q, want rate_limit", reason)
		}
		if retry < 1 {
			t.Fatalf("retryAfter = %d < 1 — the floor clamp was removed", retry)
		}
	}
	rel()
}

func TestUserRateLimit_RetryAfterConstantForConcurrentAndGlobal(t *testing.T) {
	// concurrent_limit and global_busy both return retryAfter=1 as a
	// constant. A refactor that tried to compute a heuristic (e.g.
	// "approx time to a release") would surface.
	{
		cfg := config.CanvasRateLimit{
			Chat: config.CanvasRateLimitBucket{PerMinute: 100, PerUserConcurrent: 1},
		}
		r := newTestRegistry(cfg)
		rel, _, ok, _ := r.acquire(1, "chat")
		if !ok {
			t.Fatal("first acquire should succeed")
		}
		_, retry, ok, reason := r.acquire(1, "chat")
		if ok || reason != "concurrent_limit" {
			t.Fatalf("expected concurrent_limit denial, got ok=%v reason=%q", ok, reason)
		}
		if retry != 1 {
			t.Errorf("concurrent_limit retryAfter = %d, want 1", retry)
		}
		rel()
	}
	{
		cfg := config.CanvasRateLimit{
			Chat:             config.CanvasRateLimitBucket{PerMinute: 100, PerUserConcurrent: 100},
			GlobalConcurrent: 1,
		}
		r := newTestRegistry(cfg)
		rel, _, ok, _ := r.acquire(1, "chat")
		if !ok {
			t.Fatal("first global acquire should succeed")
		}
		_, retry, ok, reason := r.acquire(2, "chat")
		if ok || reason != "global_busy" {
			t.Fatalf("expected global_busy denial, got ok=%v reason=%q", ok, reason)
		}
		if retry != 1 {
			t.Errorf("global_busy retryAfter = %d, want 1", retry)
		}
		rel()
	}
}

func TestUserRateLimit_TwoUnknownOpsHaveSeparateBuckets(t *testing.T) {
	// Both "op-a" and "op-b" resolve to Chat defaults (canvas defaults),
	// but the registry map keys on the op string literally. So a user
	// hitting each op's limit independently proves the buckets are
	// separate — they do NOT share a single "chat" bucket.
	// Use PerMinute=1 so each op gates after a single call.
	cfg := config.CanvasRateLimit{
		Chat: config.CanvasRateLimitBucket{PerMinute: 1, PerUserConcurrent: 5},
	}
	r := newTestRegistry(cfg)

	relA, _, okA, _ := r.acquire(1, "op-a")
	if !okA {
		t.Fatal("op-a first call should succeed")
	}
	relA()
	// op-a is now exhausted for this minute.
	_, _, ok, reason := r.acquire(1, "op-a")
	if ok || reason != "rate_limit" {
		t.Fatalf("op-a second call should hit rate_limit; got ok=%v reason=%q", ok, reason)
	}

	// op-b must still have its own unspent bucket — NOT shared with op-a.
	relB, _, okB, _ := r.acquire(1, "op-b")
	if !okB {
		t.Fatal("op-b must have its own bucket (not shared with op-a)")
	}
	relB()
}

func TestUserRateLimit_GlobalCapCountsAcrossOpsForSameUser(t *testing.T) {
	// GlobalConcurrent=1, user 1 acquires "chat", then tries "agent".
	// Both count against globalInUse — the second call must deny with
	// global_busy. Pin cross-op counting for a single user.
	cfg := config.CanvasRateLimit{
		Chat:             config.CanvasRateLimitBucket{PerMinute: 100, PerUserConcurrent: 100},
		Agent:            config.CanvasRateLimitBucket{PerMinute: 100, PerUserConcurrent: 100},
		GlobalConcurrent: 1,
	}
	r := newTestRegistry(cfg)

	rel, _, ok, _ := r.acquire(1, "chat")
	if !ok {
		t.Fatal("first acquire (chat) should succeed")
	}
	_, _, ok, reason := r.acquire(1, "agent")
	if ok {
		t.Fatal("agent for same user should hit global cap")
	}
	if reason != "global_busy" {
		t.Fatalf("reason = %q, want global_busy (cross-op counting is load-bearing for global cap)", reason)
	}
	rel()
}

func TestUserRateLimit_ReentrantSameUserConcurrentSlots(t *testing.T) {
	// PerUserConcurrent=3 — one user must be able to hold 3 slots
	// simultaneously. 4th denies with concurrent_limit.
	cfg := config.CanvasRateLimit{
		Chat: config.CanvasRateLimitBucket{PerMinute: 100, PerUserConcurrent: 3},
	}
	r := newTestRegistry(cfg)

	r1, _, ok, _ := r.acquire(1, "chat")
	if !ok {
		t.Fatal("slot 1 should succeed")
	}
	r2, _, ok, _ := r.acquire(1, "chat")
	if !ok {
		t.Fatal("slot 2 should succeed")
	}
	r3, _, ok, _ := r.acquire(1, "chat")
	if !ok {
		t.Fatal("slot 3 should succeed")
	}
	_, _, ok, reason := r.acquire(1, "chat")
	if ok {
		t.Fatal("slot 4 should deny")
	}
	if reason != "concurrent_limit" {
		t.Fatalf("reason = %q, want concurrent_limit", reason)
	}
	r1()
	r2()
	r3()
}
