package middleware

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"server/config"
	"server/globals"
	"server/utils"

	"github.com/gin-gonic/gin"
)

// user_rate_limit.go implements L2 (per-user bucket) and L3/L4 (per-user
// and global concurrency) of the Canvas rate-limit matrix documented in
// §12.2 of canvas-tool-design.md. L1 (IP bucket) is covered by the
// existing RateLimit middleware.
//
// The limiter is keyed by (uid, op), so a user hammering /chat doesn't
// starve /quote. Unauthenticated requests (uid==0) fall through to the
// IP-level limiter — we do not want to maintain a bucket for anonymous
// traffic here because the JWT middleware already rejected them.

// UserRateLimitRegistry holds every per-op bucket plus a global
// concurrency counter. One registry per process; routes grab buckets by
// op name via Limiter().
type UserRateLimitRegistry struct {
	mu               sync.Mutex
	buckets          map[string]*userBucket
	globalConcurrent int
	globalInUse      int
	resolve          func(op string) config.CanvasRateLimitBucket
}

type userBucket struct {
	op               string
	perMinute        int
	perUserConcurrent int
	users            map[uint]*userState
}

type userState struct {
	windowMinute int64
	requests     int
	concurrent   int
	lastSeen     time.Time
}

// NewCanvasRateLimitRegistry snapshots the Canvas rate-limit config once
// at router init. Changes to globals.GraConf after startup are not
// picked up — consistent with how generator_api wires generateGuard.
func NewCanvasRateLimitRegistry() *UserRateLimitRegistry {
	cfg := globals.GraConf.Canvas.RateLimit
	reg := &UserRateLimitRegistry{
		buckets:          map[string]*userBucket{},
		globalConcurrent: cfg.EffectiveGlobalConcurrent(),
		resolve:          cfg.Resolve,
	}
	go reg.janitor()
	return reg
}

func (r *UserRateLimitRegistry) bucket(op string) *userBucket {
	if b, ok := r.buckets[op]; ok {
		return b
	}
	cfg := r.resolve(op)
	b := &userBucket{
		op:                op,
		perMinute:         cfg.PerMinute,
		perUserConcurrent: cfg.PerUserConcurrent,
		users:             map[uint]*userState{},
	}
	r.buckets[op] = b
	return b
}

// janitor evicts idle users every 10 minutes. Cheap under the lock
// because both maps are small — a few thousand entries tops.
func (r *UserRateLimitRegistry) janitor() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		cutoff := time.Now().Add(-30 * time.Minute)
		r.mu.Lock()
		for _, b := range r.buckets {
			for uid, st := range b.users {
				if st.concurrent == 0 && st.lastSeen.Before(cutoff) {
					delete(b.users, uid)
				}
			}
		}
		r.mu.Unlock()
	}
}

// acquire attempts to reserve a slot for uid in op's bucket. release is
// nil on denial. retryAfter is seconds until the user's window refills
// or a concurrent slot frees; reason is one of rate_limit /
// concurrent_limit / global_busy.
func (r *UserRateLimitRegistry) acquire(uid uint, op string) (release func(), retryAfter int, ok bool, reason string) {
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()

	b := r.bucket(op)
	st := b.users[uid]
	if st == nil {
		st = &userState{windowMinute: now.Unix() / 60, lastSeen: now}
		b.users[uid] = st
	}

	windowMinute := now.Unix() / 60
	if st.windowMinute != windowMinute {
		st.windowMinute = windowMinute
		st.requests = 0
	}

	if b.perMinute > 0 && st.requests >= b.perMinute {
		next := time.Unix((windowMinute+1)*60, 0)
		retry := int(time.Until(next).Seconds())
		if retry < 1 {
			retry = 1
		}
		st.lastSeen = now
		return nil, retry, false, "rate_limit"
	}

	if b.perUserConcurrent > 0 && st.concurrent >= b.perUserConcurrent {
		st.lastSeen = now
		return nil, 1, false, "concurrent_limit"
	}

	if r.globalConcurrent > 0 && r.globalInUse >= r.globalConcurrent {
		st.lastSeen = now
		return nil, 1, false, "global_busy"
	}

	st.requests++
	st.concurrent++
	st.lastSeen = now
	r.globalInUse++

	release = func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if cur := b.users[uid]; cur != nil && cur.concurrent > 0 {
			cur.concurrent--
		}
		if r.globalInUse > 0 {
			r.globalInUse--
		}
	}
	return release, 0, true, ""
}

// Limiter returns a gin middleware that gates op. The middleware
// requires the JWT layer to have populated uid; unauthenticated
// requests skip the limiter so the response is still 401 rather than
// 429 (JWT middleware runs first anyway on the private group).
func (r *UserRateLimitRegistry) Limiter(op string) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid := utils.GetUserID(c)
		if uid == 0 {
			c.Next()
			return
		}

		release, retryAfter, ok, reason := r.acquire(uid, op)
		if !ok {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"code":    429,
				"message": "Canvas rate limit exceeded",
				"error": gin.H{
					"code":       "CANVAS_RATE_LIMIT_EXCEEDED",
					"reason":     reason,
					"retryAfter": retryAfter,
					"op":         op,
				},
			})
			return
		}
		defer release()
		c.Next()
	}
}
