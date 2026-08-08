package middleware

import (
	"testing"

	"server/config"
)

// newTestRegistry builds a registry without touching globals.GraConf so
// tests don't depend on the YAML loader. Mirrors the shape of
// NewCanvasRateLimitRegistry but with an injected config.
func newTestRegistry(cfg config.CanvasRateLimit) *UserRateLimitRegistry {
	return &UserRateLimitRegistry{
		buckets:          map[string]*userBucket{},
		globalConcurrent: cfg.EffectiveGlobalConcurrent(),
		resolve:          cfg.Resolve,
	}
}

func TestUserRateLimit_PerMinuteCap(t *testing.T) {
	cfg := config.CanvasRateLimit{
		Chat: config.CanvasRateLimitBucket{PerMinute: 2, PerUserConcurrent: 5},
	}
	r := newTestRegistry(cfg)

	rel1, _, ok, _ := r.acquire(1, "chat")
	if !ok {
		t.Fatal("first acquire should succeed")
	}
	rel1()
	rel2, _, ok, _ := r.acquire(1, "chat")
	if !ok {
		t.Fatal("second acquire should succeed")
	}
	rel2()
	_, retryAfter, ok, reason := r.acquire(1, "chat")
	if ok {
		t.Fatal("third acquire should trip the per-minute cap")
	}
	if reason != "rate_limit" {
		t.Fatalf("expected rate_limit reason, got %q", reason)
	}
	if retryAfter < 1 {
		t.Fatalf("retryAfter should be >= 1, got %d", retryAfter)
	}
}

func TestUserRateLimit_ConcurrentCap(t *testing.T) {
	cfg := config.CanvasRateLimit{
		Agent: config.CanvasRateLimitBucket{PerMinute: 100, PerUserConcurrent: 2},
	}
	r := newTestRegistry(cfg)

	rel1, _, ok, _ := r.acquire(7, "agent")
	if !ok {
		t.Fatal("first acquire should succeed")
	}
	rel2, _, ok, _ := r.acquire(7, "agent")
	if !ok {
		t.Fatal("second acquire should succeed")
	}
	_, _, ok, reason := r.acquire(7, "agent")
	if ok {
		t.Fatal("third acquire should trip the concurrency cap")
	}
	if reason != "concurrent_limit" {
		t.Fatalf("expected concurrent_limit, got %q", reason)
	}

	// Releasing one slot must free a third acquire.
	rel1()
	rel3, _, ok, _ := r.acquire(7, "agent")
	if !ok {
		t.Fatal("acquire after release should succeed")
	}
	rel2()
	rel3()
}

func TestUserRateLimit_GlobalBusy(t *testing.T) {
	cfg := config.CanvasRateLimit{
		Chat:             config.CanvasRateLimitBucket{PerMinute: 100, PerUserConcurrent: 100},
		GlobalConcurrent: 1,
	}
	r := newTestRegistry(cfg)

	rel, _, ok, _ := r.acquire(1, "chat")
	if !ok {
		t.Fatal("first global slot should succeed")
	}
	_, _, ok, reason := r.acquire(2, "chat")
	if ok {
		t.Fatal("second acquire across users should hit global cap")
	}
	if reason != "global_busy" {
		t.Fatalf("expected global_busy, got %q", reason)
	}
	rel()
	rel2, _, ok, _ := r.acquire(2, "chat")
	if !ok {
		t.Fatal("global slot should free on release")
	}
	rel2()
}

func TestUserRateLimit_BucketsIsolated(t *testing.T) {
	cfg := config.CanvasRateLimit{
		Chat:  config.CanvasRateLimitBucket{PerMinute: 1, PerUserConcurrent: 5},
		Agent: config.CanvasRateLimitBucket{PerMinute: 1, PerUserConcurrent: 5},
	}
	r := newTestRegistry(cfg)

	rel, _, ok, _ := r.acquire(1, "chat")
	if !ok {
		t.Fatal("chat acquire should succeed")
	}
	rel()
	// Chat window is exhausted, but agent has its own bucket.
	rel2, _, ok, _ := r.acquire(1, "agent")
	if !ok {
		t.Fatal("agent bucket should be independent of chat bucket")
	}
	rel2()
}

func TestUserRateLimit_UnknownOpFallsBackToChat(t *testing.T) {
	cfg := config.CanvasRateLimit{
		Chat: config.CanvasRateLimitBucket{PerMinute: 1, PerUserConcurrent: 1},
	}
	r := newTestRegistry(cfg)

	rel, _, ok, _ := r.acquire(1, "mystery-op")
	if !ok {
		t.Fatal("unknown op should fall through to chat defaults")
	}
	rel()
	_, _, ok, reason := r.acquire(1, "mystery-op")
	if ok {
		t.Fatal("unknown op should share the chat bucket and hit the cap")
	}
	if reason != "rate_limit" {
		t.Fatalf("expected rate_limit, got %q", reason)
	}
}

func TestCanvasRateLimit_ResolveAppliesDefaults(t *testing.T) {
	// YAML leaves everything zero — Resolve must hand back the canvas
	// defaults so a fresh config behaves correctly.
	cfg := config.CanvasRateLimit{}
	b := cfg.Resolve("agent")
	def := config.CanvasDefaults().Agent
	if b.PerMinute != def.PerMinute || b.PerUserConcurrent != def.PerUserConcurrent {
		t.Fatalf("expected defaults %+v, got %+v", def, b)
	}
}
