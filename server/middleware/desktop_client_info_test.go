package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func TestDesktopClientInfo_StashesHeaders(t *testing.T) {
	r := gin.New()
	var sawName, sawVersion string
	r.Use(DesktopClientInfo())
	r.GET("/probe", func(c *gin.Context) {
		sawName, _ = stringContextGet(c, ContextKeyWorkMaxClientName)
		sawVersion, _ = stringContextGet(c, ContextKeyWorkMaxClientVersion)
		c.Status(http.StatusOK)
	})

	req, _ := http.NewRequest("GET", "/probe", nil)
	req.Header.Set("X-WorkMax-Client", "desktop")
	req.Header.Set("X-WorkMax-Client-Version", "0.0.3-p0-spike")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if sawName != "desktop" {
		t.Errorf("client name in context: %q", sawName)
	}
	if sawVersion != "0.0.3-p0-spike" {
		t.Errorf("client version in context: %q", sawVersion)
	}
}

func TestDesktopClientInfo_AbsentHeadersAreNoop(t *testing.T) {
	r := gin.New()
	var hadName, hadVersion bool
	r.Use(DesktopClientInfo())
	r.GET("/probe", func(c *gin.Context) {
		_, hadName = c.Get(ContextKeyWorkMaxClientName)
		_, hadVersion = c.Get(ContextKeyWorkMaxClientVersion)
		c.Status(http.StatusOK)
	})

	req, _ := http.NewRequest("GET", "/probe", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Missing headers must NOT set the context keys — non-desktop
	// callers (curl smoke tests, future browser clients) shouldn't
	// trip the desktop-client log line or be classified as desktop.
	if hadName {
		t.Error("ContextKeyWorkMaxClientName should be unset for missing header")
	}
	if hadVersion {
		t.Error("ContextKeyWorkMaxClientVersion should be unset for missing header")
	}
}

// TestDesktopClientInfo_PartialHeaderStillCaptures pins behavior for
// the edge case of one header but not the other (e.g. a future
// client that omits the version). We still stash + log so the
// partial signal is visible.
func TestDesktopClientInfo_PartialHeaderStillCaptures(t *testing.T) {
	r := gin.New()
	var sawName string
	r.Use(DesktopClientInfo())
	r.GET("/probe", func(c *gin.Context) {
		sawName, _ = stringContextGet(c, ContextKeyWorkMaxClientName)
		c.Status(http.StatusOK)
	})

	req, _ := http.NewRequest("GET", "/probe", nil)
	req.Header.Set("X-WorkMax-Client", "desktop")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if sawName != "desktop" {
		t.Errorf("client name with missing version header: %q", sawName)
	}
}

func TestDesktopClientInfo_IgnoresMalformedOrDuplicateTelemetry(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*http.Request)
	}{
		{
			name: "oversized",
			prepare: func(request *http.Request) {
				request.Header.Set(headerWorkMaxClientVersion, string(make([]byte, maxDesktopVersionBytes+1)))
			},
		},
		{
			name: "control character",
			prepare: func(request *http.Request) {
				request.Header[headerWorkMaxClientVersion] = []string{"0.1.0\nforged-log-line"}
			},
		},
		{
			name: "duplicate",
			prepare: func(request *http.Request) {
				request.Header[headerWorkMaxClientName] = []string{"desktop", "forged"}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := gin.New()
			var captured bool
			r.Use(DesktopClientInfo())
			r.GET("/probe", func(c *gin.Context) {
				_, captured = c.Get(ContextKeyWorkMaxClientVersion)
				if _, ok := c.Get(ContextKeyWorkMaxClientName); ok {
					captured = true
				}
				c.Status(http.StatusOK)
			})
			request := httptest.NewRequest(http.MethodGet, "/probe", nil)
			test.prepare(request)
			response := httptest.NewRecorder()
			r.ServeHTTP(response, request)
			if response.Code != http.StatusOK || captured {
				t.Fatalf("response=%d captured=%v", response.Code, captured)
			}
		})
	}
}

// stringContextGet adapts gin.Context.Get to a string-typed result
// since the middleware always stashes strings. Keeps test reads tidy.
func stringContextGet(c *gin.Context, key string) (string, bool) {
	v, ok := c.Get(key)
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func TestIsVersionBelowFloor(t *testing.T) {
	cases := []struct {
		name   string
		actual string
		floor  string
		want   bool
	}{
		{"strictly older patch", "0.0.2", "0.0.3", true},
		{"strictly older minor", "0.4.99", "0.5.0", true},
		{"strictly older major", "0.9.9", "1.0.0", true},
		{"equal", "0.0.3", "0.0.3", false},
		{"newer patch", "0.0.4", "0.0.3", false},
		{"newer minor", "1.5.0", "1.0.0", false},
		{"newer major", "2.0.0", "1.99.99", false},
		{"prerelease stripped, equal triple", "0.0.3-p0-spike", "0.0.3", false},
		{"prerelease stripped, below floor", "0.0.2-rc1", "0.0.3", true},
		{"build metadata stripped", "0.0.3+sha.abc", "0.0.3", false},

		// Unparseable inputs → don't classify as stale. Intentional:
		// dev builds like "main-dirty" or empty headers from older
		// sidecars shouldn't warn-spam the production log.
		{"empty actual", "", "0.0.3", false},
		{"empty floor", "0.0.2", "", false},
		{"non-numeric", "main-dirty", "0.0.3", false},
		{"only two parts", "0.0", "0.0.3", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isVersionBelowFloor(tc.actual, tc.floor); got != tc.want {
				t.Errorf("isVersionBelowFloor(%q, %q) = %v, want %v",
					tc.actual, tc.floor, got, tc.want)
			}
		})
	}
}

// TestDesktopClientInfo_StaleClientStillSucceeds pins the warn-only
// contract: a sidecar version below the floor must NOT be rejected.
// Hard-rejection is a deliberate non-feature until we have a wire-
// shape change forcing it.
func TestDesktopClientInfo_StaleClientStillSucceeds(t *testing.T) {
	prevFloor := DesktopMinSupportedVersion
	DesktopMinSupportedVersion = "0.5.0"
	t.Cleanup(func() { DesktopMinSupportedVersion = prevFloor })

	r := gin.New()
	r.Use(DesktopClientInfo())
	r.GET("/probe", func(c *gin.Context) { c.Status(http.StatusOK) })

	req, _ := http.NewRequest("GET", "/probe", nil)
	req.Header.Set("X-WorkMax-Client", "desktop")
	req.Header.Set("X-WorkMax-Client-Version", "0.0.1")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("stale client should still succeed (warn-only), got status %d", w.Code)
	}
}
