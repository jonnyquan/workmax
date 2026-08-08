package workagent

import (
	"container/list"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// filesContextCache memoizes the output of buildIntelligentFilesContext
// so we don't rebuild the same ~30-Sprintf, 7-bucket grouping work on
// every chat turn when the file set hasn't changed (which is the
// common case — files are pinned at thread creation and only change
// when the user explicitly attaches new ones).
//
// The bigger win is upstream: the system prompt the SDK forwards to
// Claude includes filesContext, so any per-turn churn here mutates
// the prompt and busts Claude's prompt cache. A stable filesContext
// per (sorted file ID + size + name + type) tuple keeps the prompt
// stable across turns and lets the cache hit.
type filesContextCache struct {
	mu       sync.Mutex
	cache    map[string]*list.Element
	lru      *list.List
	capacity int
}

type filesContextEntry struct {
	key   string
	value string
}

const filesContextCacheCapacity = 1024

var (
	filesContextCacheInstance *filesContextCache
	filesContextCacheOnce     sync.Once
)

func getFilesContextCache() *filesContextCache {
	filesContextCacheOnce.Do(func() {
		filesContextCacheInstance = newFilesContextCache(filesContextCacheCapacity)
	})
	return filesContextCacheInstance
}

// newFilesContextCache constructs a fresh cache with the supplied
// capacity. Exposed at package scope so tests can build small caches
// to exercise eviction without poking the singleton.
func newFilesContextCache(capacity int) *filesContextCache {
	return &filesContextCache{
		cache:    make(map[string]*list.Element, capacity),
		lru:      list.New(),
		capacity: capacity,
	}
}

// fingerprint produces a content-addressed key for a file slice.
// Sort by (ID, Path) before joining so the order in which the caller
// listed the files doesn't affect cache identity.
//
// Hash and ModTime ride along to bust the cache when an upload replaces
// a file in place — same row ID, same name, same size, but new bytes.
// Without them the cache hits and the system prompt keeps pointing at
// the old preview, which is exactly the silent-stale failure mode this
// cache was almost designed to enable.
//
// We used to SHA1 the joined string; the input space is tiny (a slice
// of bounded structs from a single thread) so cryptographic compression
// only obscured the key. Use the joined form directly — a hex digest
// in pprof and traces tells you nothing, the raw key shows you what
// the cache is actually doing.
func filesContextFingerprint(files []AgentFileInfo) string {
	if len(files) == 0 {
		return ""
	}
	sorted := make([]AgentFileInfo, len(files))
	copy(sorted, files)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].ID != sorted[j].ID {
			return sorted[i].ID < sorted[j].ID
		}
		return sorted[i].Path < sorted[j].Path
	})

	// Unit separator (\x1F, reserved for exactly this) keeps a
	// name/path containing the separator from colliding with an
	// adjacent field. Record separator (\x1E) does the same between
	// entries.
	const us = "\x1F"
	const rs = "\x1E"
	var b strings.Builder
	for _, f := range sorted {
		b.WriteString(f.ID)
		b.WriteString(us)
		b.WriteString(f.Path)
		b.WriteString(us)
		b.WriteString(f.Name)
		b.WriteString(us)
		b.WriteString(f.Type)
		b.WriteString(us)
		b.WriteString(strconv.FormatInt(f.Size, 10))
		b.WriteString(us)
		b.WriteString(f.Hash)
		b.WriteString(us)
		b.WriteString(strconv.FormatInt(f.ModTime, 10))
		b.WriteString(rs)
	}
	return b.String()
}

// get returns the cached context for fingerprint, or "", false.
func (c *filesContextCache) get(fingerprint string) (string, bool) {
	if fingerprint == "" {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, ok := c.cache[fingerprint]; ok {
		c.lru.MoveToFront(elem)
		return elem.Value.(*filesContextEntry).value, true
	}
	return "", false
}

// put writes context under fingerprint and evicts the LRU tail when
// capacity is exceeded.
func (c *filesContextCache) put(fingerprint, context string) {
	if fingerprint == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, ok := c.cache[fingerprint]; ok {
		elem.Value.(*filesContextEntry).value = context
		c.lru.MoveToFront(elem)
		return
	}

	entry := &filesContextEntry{key: fingerprint, value: context}
	elem := c.lru.PushFront(entry)
	c.cache[fingerprint] = elem

	for c.lru.Len() > c.capacity {
		oldest := c.lru.Back()
		if oldest == nil {
			break
		}
		c.lru.Remove(oldest)
		delete(c.cache, oldest.Value.(*filesContextEntry).key)
	}
}
