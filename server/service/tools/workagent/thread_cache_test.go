package workagent

// thread_cache_test.go — coverage for the LRU+TTL ChatThread cache.
// The cache sits in front of every "load this thread by UUID" call
// from the SSE turn dispatchers; a stale-read bug here is the
// classic "I just saved this and the agent doesn't see it" failure.
//
// Six contracts pinned:
//   1. miss-on-empty
//   2. set-then-get hit
//   3. TTL expiry returns miss + removes entry
//   4. Set-on-existing-key updates value + bumps LRU position
//   5. Capacity overflow evicts oldest (LRU semantics)
//   6. Invalidate + Clear behave as advertised

import (
	"fmt"
	"testing"
	"time"

	workagentModel "server/model/workagent"
)

func newTestCache(capacity int, ttl time.Duration) *ThreadCache {
	return NewThreadCache(capacity, ttl)
}

func mkThread(uuid string) *workagentModel.ChatThread {
	t := &workagentModel.ChatThread{UUID: uuid, Name: "fixture"}
	return t
}

func TestThreadCache_MissOnEmpty(t *testing.T) {
	c := newTestCache(10, time.Minute)
	if _, ok := c.Get("nope"); ok {
		t.Errorf("Get on empty cache returned hit")
	}
}

func TestThreadCache_SetThenGetHits(t *testing.T) {
	c := newTestCache(10, time.Minute)
	c.Set("a", mkThread("a"))
	thread, ok := c.Get("a")
	if !ok {
		t.Fatalf("Get after Set returned miss")
	}
	if thread.UUID != "a" {
		t.Errorf("Get returned wrong thread: uuid=%q want %q", thread.UUID, "a")
	}
}

func TestThreadCache_TTLExpiry_RemovesAndMisses(t *testing.T) {
	// TTL=10ms so the sleep stays under most test budgets.
	c := newTestCache(10, 10*time.Millisecond)
	c.Set("ttl-key", mkThread("ttl-key"))

	time.Sleep(20 * time.Millisecond)

	if _, ok := c.Get("ttl-key"); ok {
		t.Errorf("Get after TTL expiry returned hit")
	}
	// Stats should also show size=0 — the expired entry must be
	// removed, not just hidden behind the TTL check.
	stats := c.Stats()
	if stats["size"].(int) != 0 {
		t.Errorf("expired entry not evicted from underlying storage; size=%v", stats["size"])
	}
}

func TestThreadCache_SetOnExistingKey_UpdatesAndBumpsLRU(t *testing.T) {
	// Capacity=2 lets us prove the LRU-bump by inserting 3 items
	// after re-Setting the first: with the bump the FIRST should
	// survive, without it the FIRST would be the oldest and evicted.
	c := newTestCache(2, time.Minute)
	c.Set("first", mkThread("first-v1"))
	c.Set("second", mkThread("second"))

	// Re-Set "first" with new value — must update value AND move it
	// to MRU position. Without the bump, "first" is at the back and
	// will be evicted by the next Set.
	c.Set("first", mkThread("first-v2"))

	c.Set("third", mkThread("third"))

	// Expected survivors: "first" (bumped to MRU) + "third" (newest);
	// "second" was the oldest after the bump.
	if _, ok := c.Get("first"); !ok {
		t.Errorf("first should have survived eviction (was bumped to MRU)")
	}
	if got, ok := c.Get("first"); ok && got.UUID != "first-v2" {
		t.Errorf("first not updated to v2; got %q", got.UUID)
	}
	if _, ok := c.Get("second"); ok {
		t.Errorf("second should have been evicted (was LRU)")
	}
	if _, ok := c.Get("third"); !ok {
		t.Errorf("third should be present (just inserted)")
	}
}

func TestThreadCache_CapacityOverflow_EvictsOldest(t *testing.T) {
	// Capacity=3, insert 5; first two should be evicted in order.
	c := newTestCache(3, time.Minute)
	for i := 1; i <= 5; i++ {
		key := fmt.Sprintf("k%d", i)
		c.Set(key, mkThread(key))
	}

	// k1, k2 should be evicted; k3, k4, k5 present.
	for _, k := range []string{"k1", "k2"} {
		if _, ok := c.Get(k); ok {
			t.Errorf("%s should have been evicted at capacity overflow", k)
		}
	}
	for _, k := range []string{"k3", "k4", "k5"} {
		if _, ok := c.Get(k); !ok {
			t.Errorf("%s should still be cached", k)
		}
	}
	if c.Stats()["size"].(int) != 3 {
		t.Errorf("size = %v after overflow, want 3", c.Stats()["size"])
	}
}

func TestThreadCache_Invalidate_RemovesEntry(t *testing.T) {
	c := newTestCache(10, time.Minute)
	c.Set("inv-key", mkThread("inv-key"))
	if _, ok := c.Get("inv-key"); !ok {
		t.Fatalf("Set then Get returned miss")
	}
	c.Invalidate("inv-key")
	if _, ok := c.Get("inv-key"); ok {
		t.Errorf("Invalidated key still returns hit")
	}
}

func TestThreadCache_Invalidate_NonexistentNoOp(t *testing.T) {
	c := newTestCache(10, time.Minute)
	// Should not panic / not affect other entries.
	c.Set("keep", mkThread("keep"))
	c.Invalidate("does-not-exist")
	if _, ok := c.Get("keep"); !ok {
		t.Errorf("Invalidate of nonexistent key wiped unrelated entry")
	}
}

func TestThreadCache_Clear_RemovesAll(t *testing.T) {
	c := newTestCache(10, time.Minute)
	for i := 1; i <= 5; i++ {
		key := fmt.Sprintf("c%d", i)
		c.Set(key, mkThread(key))
	}
	c.Clear()
	if c.Stats()["size"].(int) != 0 {
		t.Errorf("size = %v after Clear, want 0", c.Stats()["size"])
	}
	if _, ok := c.Get("c1"); ok {
		t.Errorf("Get after Clear returned hit")
	}
}

// Get on the SAME key bumps it to MRU — guards against the LRU
// pattern degenerating into "first-in-first-out regardless of read
// access," which would evict still-hot threads.
func TestThreadCache_GetBumpsLRU(t *testing.T) {
	c := newTestCache(2, time.Minute)
	c.Set("hot", mkThread("hot"))
	c.Set("cold", mkThread("cold"))

	// Read "hot" to bump it.
	if _, ok := c.Get("hot"); !ok {
		t.Fatalf("Get on hot returned miss")
	}

	// Insert a third — "cold" should be evicted as the LRU,
	// not "hot" which was just read.
	c.Set("fresh", mkThread("fresh"))

	if _, ok := c.Get("hot"); !ok {
		t.Errorf("hot should survive eviction (bumped to MRU by Get)")
	}
	if _, ok := c.Get("cold"); ok {
		t.Errorf("cold should have been evicted (was LRU after Get(hot))")
	}
	if _, ok := c.Get("fresh"); !ok {
		t.Errorf("fresh should be present (just inserted)")
	}
}
