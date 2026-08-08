package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// TestRateLimitByPathParam_BucketsPerUUID verifies the contract added
// for D6's per-UUID share-link bucket. Two distinct UUIDs each have
// their own allotment; a third request to the FIRST UUID after the
// first two requests still gets through because the bucket size is 2.
func TestRateLimitByPathParam_BucketsPerUUID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/r/:uuid",
		RateLimitByPathParam("uuid", 2, time.Minute),
		func(c *gin.Context) { c.Status(http.StatusOK) },
	)

	send := func(uuid string) int {
		req := httptest.NewRequest(http.MethodGet, "/r/"+uuid, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}

	// UUID-A: capacity 2 → first two pass, third 429s.
	if c := send("aaa"); c != 200 {
		t.Errorf("aaa #1: status %d, want 200", c)
	}
	if c := send("aaa"); c != 200 {
		t.Errorf("aaa #2: status %d, want 200", c)
	}
	if c := send("aaa"); c != 429 {
		t.Errorf("aaa #3: status %d, want 429", c)
	}

	// UUID-B: separate bucket, must not be affected by aaa's burn.
	if c := send("bbb"); c != 200 {
		t.Errorf("bbb #1 after aaa exhausted: status %d, want 200", c)
	}
	if c := send("bbb"); c != 200 {
		t.Errorf("bbb #2: status %d, want 200", c)
	}
	if c := send("bbb"); c != 429 {
		t.Errorf("bbb #3: status %d, want 429", c)
	}
}

// TestRateLimitByPathParam_EmptyKeyPassesThrough — when the route
// param doesn't exist on this request (e.g. middleware mounted on a
// route that doesn't have that param), the bucket extractor returns
// empty and we pass through unmetered. Documents the contract so a
// future caller knows the empty-key fallback is intentional.
func TestRateLimitByPathParam_EmptyKeyPassesThrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// Mount on a path that doesn't actually have :uuid.
	r.GET("/no-param",
		RateLimitByPathParam("uuid", 1, time.Minute),
		func(c *gin.Context) { c.Status(http.StatusOK) },
	)

	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/no-param", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("call %d with empty key: status %d, want pass-through 200", i+1, w.Code)
		}
	}
}

// TestRateLimit_PerIPStillIsolated — the legacy RateLimit (now a thin
// wrapper around RateLimitByKey) must still bucket per-ClientIP. Pin
// to guard the refactor.
func TestRateLimit_PerIPStillIsolated(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/p",
		RateLimit(1, time.Minute),
		func(c *gin.Context) { c.Status(http.StatusOK) },
	)

	send := func(ip string) int {
		req := httptest.NewRequest(http.MethodGet, "/p", nil)
		req.RemoteAddr = ip + ":12345"
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}

	if c := send("1.1.1.1"); c != 200 {
		t.Errorf("1.1.1.1 #1: %d", c)
	}
	if c := send("1.1.1.1"); c != 429 {
		t.Errorf("1.1.1.1 #2: %d, want 429", c)
	}
	if c := send("2.2.2.2"); c != 200 {
		t.Errorf("2.2.2.2 isolated bucket: %d, want 200", c)
	}
}

func TestRateLimitByKeyFailsClosedAtBucketCapacity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/bounded",
		rateLimitByKeyWithCapacity(
			func(c *gin.Context) string { return c.Query("key") },
			1,
			time.Hour,
			2,
		),
		func(c *gin.Context) { c.Status(http.StatusOK) },
	)

	send := func(key string) int {
		req := httptest.NewRequest(http.MethodGet, "/bounded?key="+key, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	if got := send("first"); got != http.StatusOK {
		t.Fatalf("first bucket status = %d", got)
	}
	if got := send("second"); got != http.StatusOK {
		t.Fatalf("second bucket status = %d", got)
	}
	if got := send("third"); got != http.StatusTooManyRequests {
		t.Fatalf("capacity overflow status = %d, want 429", got)
	}
	// An existing bucket remains usable; capacity exhaustion must not erase or
	// replace live callers while refusing a new cardinality attack.
	if got := send("first"); got != http.StatusTooManyRequests {
		t.Fatalf("existing exhausted bucket status = %d, want 429", got)
	}
}

func TestRateLimitIncludesRetryAfter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/limited", RateLimit(1, time.Minute), func(c *gin.Context) { c.Status(http.StatusOK) })

	request := httptest.NewRequest(http.MethodGet, "/limited", nil)
	r.ServeHTTP(httptest.NewRecorder(), request)
	response := httptest.NewRecorder()
	r.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/limited", nil))
	if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") != "60" {
		t.Fatalf("response = %d Retry-After=%q", response.Code, response.Header().Get("Retry-After"))
	}
}
