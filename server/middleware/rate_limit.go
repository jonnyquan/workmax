package middleware

import (
	"crypto/sha256"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type visitor struct {
	tokens   float64
	lastSeen time.Time
}

const (
	maxRateLimitBuckets  = 16_384
	rateLimitCleanupTick = 5 * time.Minute
)

// RateLimitByKey is the underlying token-bucket primitive: maintains
// one bucket per `keyExtract(c)`. When the extracted key is empty the
// request passes through unmetered (caller decides what "no key" means
// — for IP-based limits c.ClientIP() is never empty, but for path-
// param-based limits the route may not have populated the param yet).
//
// Opportunistic cleanup evicts buckets idle for more than 2*window, while a
// hard capacity keeps adversarial high-cardinality keys from growing memory
// without bound. A full map fails closed until an idle bucket is reclaimed.
func RateLimitByKey(keyExtract func(*gin.Context) string, maxRequests int, window time.Duration) gin.HandlerFunc {
	return rateLimitByKeyWithCapacity(keyExtract, maxRequests, window, maxRateLimitBuckets)
}

func rateLimitByKeyWithCapacity(
	keyExtract func(*gin.Context) string,
	maxRequests int,
	window time.Duration,
	maxBuckets int,
) gin.HandlerFunc {
	if keyExtract == nil || maxRequests <= 0 || window <= 0 || maxBuckets <= 0 {
		panic("rate limiter requires a key extractor and positive request, window, and bucket limits")
	}
	var mu sync.Mutex
	visitors := make(map[[sha256.Size]byte]*visitor)
	lastCleanup := time.Now()

	refillRate := float64(maxRequests) / window.Seconds()
	retryAfterSeconds := int(math.Ceil(window.Seconds() / float64(maxRequests)))
	if retryAfterSeconds < 1 {
		retryAfterSeconds = 1
	}

	return func(c *gin.Context) {
		rawKey := keyExtract(c)
		if rawKey == "" {
			// No key to bucket on — pass through. The caller chose
			// the extractor; an empty result is the caller's signal
			// that this request shouldn't be rate-limited (e.g. the
			// path param wasn't populated for this route).
			c.Next()
			return
		}
		now := time.Now()
		key := sha256.Sum256([]byte(rawKey))

		mu.Lock()
		if now.Sub(lastCleanup) >= rateLimitCleanupTick {
			for candidate, value := range visitors {
				if now.Sub(value.lastSeen) > 2*window {
					delete(visitors, candidate)
				}
			}
			lastCleanup = now
		}
		v, exists := visitors[key]
		if !exists {
			if len(visitors) >= maxBuckets {
				mu.Unlock()
				writeRateLimitExceeded(c, retryAfterSeconds)
				return
			}
			v = &visitor{tokens: float64(maxRequests), lastSeen: now}
			visitors[key] = v
		}

		elapsed := now.Sub(v.lastSeen).Seconds()
		v.tokens += elapsed * refillRate
		if v.tokens > float64(maxRequests) {
			v.tokens = float64(maxRequests)
		}
		v.lastSeen = now

		if v.tokens < 1 {
			mu.Unlock()
			writeRateLimitExceeded(c, retryAfterSeconds)
			return
		}

		v.tokens--
		mu.Unlock()

		c.Next()
	}
}

func writeRateLimitExceeded(c *gin.Context, retryAfterSeconds int) {
	c.Header("Retry-After", strconv.Itoa(retryAfterSeconds))
	c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
		"code":    0,
		"message": "Too many requests, please try again later",
	})
}

// RateLimit returns middleware that limits requests per IP using a token bucket algorithm.
// maxRequests is the bucket size, window is the refill period for the full bucket.
func RateLimit(maxRequests int, window time.Duration) gin.HandlerFunc {
	return RateLimitByKey(func(c *gin.Context) string {
		return c.ClientIP()
	}, maxRequests, window)
}

// RateLimitByPathParam returns middleware that buckets requests by the
// value of a Gin route param (e.g. ":threadId"). Used for per-resource
// limits — distributed scrape across many IPs hitting one share URL is
// bounded even though per-IP buckets aren't crossed. Unpopulated param
// → pass through.
func RateLimitByPathParam(paramName string, maxRequests int, window time.Duration) gin.HandlerFunc {
	return RateLimitByKey(func(c *gin.Context) string {
		return c.Param(paramName)
	}, maxRequests, window)
}
