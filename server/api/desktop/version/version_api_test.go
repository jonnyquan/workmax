package version

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"server/middleware"

	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func TestVersionApi_Get_ReflectsMiddlewareFloor(t *testing.T) {
	prev := middleware.DesktopMinSupportedVersion
	middleware.DesktopMinSupportedVersion = "0.5.0"
	t.Cleanup(func() { middleware.DesktopMinSupportedVersion = prev })

	r := gin.New()
	r.GET("/api/desktop/version", VersionApi{}.Get)

	req, _ := http.NewRequest(http.MethodGet, "/api/desktop/version", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	if got := w.Header().Get("Cache-Control"); got == "" {
		t.Errorf("Cache-Control header missing (got %q)", got)
	}

	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v\nbody: %s", err, w.Body.String())
	}
	if body["min_supported"] != "0.5.0" {
		t.Errorf("min_supported: got %q, want %q", body["min_supported"], "0.5.0")
	}
	if body["latest_recommended"] != "0.5.0" {
		t.Errorf("latest_recommended: got %q, want %q", body["latest_recommended"], "0.5.0")
	}
}

// TestVersionApi_Get_ReleaseNotesURLFromEnv pins that the env
// override surfaces in the response. Ops sets this on cloud deploy
// to point the renderer's "What's new" link at the right changelog
// URL without a code change.
func TestVersionApi_Get_ReleaseNotesURLFromEnv(t *testing.T) {
	prev, prevSet := os.LookupEnv(ReleaseNotesURLEnv)
	if err := os.Setenv(ReleaseNotesURLEnv, "https://workmax.app/desktop/changelog"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if prevSet {
			os.Setenv(ReleaseNotesURLEnv, prev)
		} else {
			os.Unsetenv(ReleaseNotesURLEnv)
		}
	})

	r := gin.New()
	r.GET("/api/desktop/version", VersionApi{}.Get)

	req, _ := http.NewRequest(http.MethodGet, "/api/desktop/version", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["release_notes_url"] != "https://workmax.app/desktop/changelog" {
		t.Errorf("release_notes_url: %v", body["release_notes_url"])
	}
}

// TestVersionApi_Get_ReleaseNotesURLOmitted pins that the field is
// elided (not emitted as empty string) when the env isn't set.
// `json:"release_notes_url,omitempty"` is what makes the renderer's
// "What's new" button suppression a clean code path.
func TestVersionApi_Get_ReleaseNotesURLOmitted(t *testing.T) {
	prev, prevSet := os.LookupEnv(ReleaseNotesURLEnv)
	os.Unsetenv(ReleaseNotesURLEnv)
	t.Cleanup(func() {
		if prevSet {
			os.Setenv(ReleaseNotesURLEnv, prev)
		}
	})

	r := gin.New()
	r.GET("/api/desktop/version", VersionApi{}.Get)

	req, _ := http.NewRequest(http.MethodGet, "/api/desktop/version", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if _, present := body["release_notes_url"]; present {
		t.Errorf("release_notes_url should be omitted when env unset, got: %v", body)
	}
}

// TestVersionApi_Get_StableFieldShape pins the wire-shape contract:
// renaming either field is a coordinated breaking change for any
// renderer that reads the response. Locks the JSON keys explicitly
// so a typo like 'min_support' (missing 'ed') trips here, not in
// the renderer's silent fallback path.
func TestVersionApi_Get_StableFieldShape(t *testing.T) {
	r := gin.New()
	r.GET("/api/desktop/version", VersionApi{}.Get)

	req, _ := http.NewRequest(http.MethodGet, "/api/desktop/version", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var raw map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	required := []string{"min_supported", "latest_recommended"}
	for _, k := range required {
		if _, ok := raw[k]; !ok {
			t.Errorf("required field %q missing from response: %s", k, w.Body.String())
		}
	}
}
