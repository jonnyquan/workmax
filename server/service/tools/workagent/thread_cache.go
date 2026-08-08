package workagent

import (
	"container/list"
	"fmt"
	"sync"
	"time"

	"server/globals"
	"server/model/workagent"
)

// CachedThread represents a cached thread with expiration time
type CachedThread struct {
	thread   *workagent.ChatThread
	cachedAt time.Time
}

// ThreadCache implements an LRU cache for ChatThread objects
type ThreadCache struct {
	capacity int
	ttl      time.Duration
	mu       sync.RWMutex
	cache    map[string]*list.Element
	lru      *list.List
}

// cacheEntry is the value stored in the LRU list
type cacheEntry struct {
	key   string
	value *CachedThread
}

var (
	threadCacheInstance *ThreadCache
	threadCacheOnce     sync.Once
)

// GetThreadCache returns the singleton ThreadCache instance
func GetThreadCache() *ThreadCache {
	threadCacheOnce.Do(func() {
		threadCacheInstance = NewThreadCache(1000, 5*time.Minute)
		globals.Info("[ThreadCache] Initialized with capacity=1000, ttl=5m")
	})
	return threadCacheInstance
}

// NewThreadCache creates a new ThreadCache
func NewThreadCache(capacity int, ttl time.Duration) *ThreadCache {
	return &ThreadCache{
		capacity: capacity,
		ttl:      ttl,
		cache:    make(map[string]*list.Element),
		lru:      list.New(),
	}
}

// Get retrieves a thread from cache
func (tc *ThreadCache) Get(threadID string) (*workagent.ChatThread, bool) {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	// Check if exists in cache
	elem, ok := tc.cache[threadID]
	if !ok {
		return nil, false
	}

	// Check TTL
	entry := elem.Value.(*cacheEntry)
	if time.Since(entry.value.cachedAt) > tc.ttl {
		// Expired, remove from cache
		tc.lru.Remove(elem)
		delete(tc.cache, threadID)
		// Cache events are observability-quality — at chat scale a
		// per-get/set INFO line dwarfs real signal in production
		// logs. Same demotion pattern applied in e25f7d6a.
		globals.Debug(fmt.Sprintf("[ThreadCache] Expired: threadID=%s", threadID))
		return nil, false
	}

	// Move to front (most recently used)
	tc.lru.MoveToFront(elem)

	globals.Debug(fmt.Sprintf("[ThreadCache] Hit: threadID=%s", threadID))
	return entry.value.thread, true
}

// Set stores a thread in cache
func (tc *ThreadCache) Set(threadID string, thread *workagent.ChatThread) {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	// Check if already exists
	if elem, ok := tc.cache[threadID]; ok {
		// Update existing entry
		tc.lru.MoveToFront(elem)
		entry := elem.Value.(*cacheEntry)
		entry.value = &CachedThread{
			thread:   thread,
			cachedAt: time.Now(),
		}
		globals.Debug(fmt.Sprintf("[ThreadCache] Updated: threadID=%s", threadID))
		return
	}

	// Add new entry
	entry := &cacheEntry{
		key: threadID,
		value: &CachedThread{
			thread:   thread,
			cachedAt: time.Now(),
		},
	}
	elem := tc.lru.PushFront(entry)
	tc.cache[threadID] = elem

	// Evict oldest if over capacity
	if tc.lru.Len() > tc.capacity {
		oldest := tc.lru.Back()
		if oldest != nil {
			tc.lru.Remove(oldest)
			oldEntry := oldest.Value.(*cacheEntry)
			delete(tc.cache, oldEntry.key)
			globals.Debug(fmt.Sprintf("[ThreadCache] Evicted: threadID=%s", oldEntry.key))
		}
	}

	globals.Debug(fmt.Sprintf("[ThreadCache] Set: threadID=%s, size=%d", threadID, tc.lru.Len()))
}

// Invalidate removes a thread from cache
func (tc *ThreadCache) Invalidate(threadID string) {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	if elem, ok := tc.cache[threadID]; ok {
		tc.lru.Remove(elem)
		delete(tc.cache, threadID)
		globals.Debug(fmt.Sprintf("[ThreadCache] Invalidated: threadID=%s", threadID))
	}
}

// Clear removes all entries from cache
func (tc *ThreadCache) Clear() {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	tc.cache = make(map[string]*list.Element)
	tc.lru.Init()
	globals.Info("[ThreadCache] Cleared all entries")
}

// Stats returns cache statistics
func (tc *ThreadCache) Stats() map[string]interface{} {
	tc.mu.RLock()
	defer tc.mu.RUnlock()

	return map[string]interface{}{
		"size":     tc.lru.Len(),
		"capacity": tc.capacity,
		"ttl":      tc.ttl.String(),
	}
}
