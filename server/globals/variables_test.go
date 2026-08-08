package globals

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// variables.go exposes a handful of pure helpers that guard CORS,
// open-path routing, and model-capability branching: OriginIsAllowed,
// OriginIsOpen, IsOpenAIDalleModel, IsVisionModel, and the unexported
// `in` helper that backs the first two. They have zero tests, but they
// ship in the request hot-path — a silent change to `in`'s
// substring-match semantics would either leak origins past CORS or
// mis-classify a model's capabilities at request time.

func TestIn_ExactOrSubstringMatch(t *testing.T) {
	// `in` matches on exact equality OR substring containment (value
	// contains item). That's a quirky contract — subtle but load-bearing
	// for IsVisionModel ("gpt-4o-2024-05-13" is NOT in the list, but
	// "gpt-4o" IS, and the substring-contains on "gpt-4o-..." catches it).
	if !in("exact", []string{"exact"}) {
		t.Errorf("exact match failed")
	}
	if !in("gpt-4o-2024-05-13", []string{"gpt-4o"}) {
		t.Errorf("substring match (item inside value) failed")
	}
	// Reverse direction MUST NOT match — `strings.Contains(value, item)`
	// only checks whether item is inside value, not the other way around.
	if in("gpt-4", []string{"gpt-4-turbo"}) {
		t.Errorf("reverse substring must NOT match (item longer than value)")
	}
	if in("foo", []string{"xyz", "qqq"}) {
		t.Errorf("non-matching short strings must not match via contains")
	}
	if in("anything", nil) {
		t.Errorf("empty slice must never match")
	}
}

func TestOriginIsAllowed(t *testing.T) {
	// Save & restore the package-global AllowedOrigins so tests don't
	// bleed into each other.
	original := AllowedOrigins
	t.Cleanup(func() { AllowedOrigins = original })

	// Fail-open: empty allow-list means "allow all" — this is the
	// default in dev and a silent flip to fail-closed would break every
	// deploy that didn't set AllowedOrigins explicitly.
	AllowedOrigins = nil
	if !OriginIsAllowed("https://anywhere.example.com") {
		t.Errorf("empty AllowedOrigins must allow all")
	}

	AllowedOrigins = []string{"example.com", "trusted.io"}

	cases := []struct {
		name string
		uri  string
		want bool
	}{
		// www.-prefix stripping: "www.example.com" is compared against
		// "example.com" after the leading "www." is chopped.
		{"www. prefix stripped before match", "https://www.example.com/path", true},
		{"bare host match", "https://example.com", true},
		{"second entry match", "https://trusted.io", true},

		// Localhost is always allowed regardless of AllowedOrigins —
		// dev servers and tooling rely on this.
		{"localhost always allowed", "http://localhost:3000", true},
		// file:// scheme always allowed (desktop/mobile embeds).
		{"file scheme always allowed", "file:///Users/me/index.html", true},

		{"unlisted host rejected", "https://evil.example.org", false},

		// Parse failure → false (fail-closed for malformed input). The
		// empty string parses to an empty URL with empty host which
		// doesn't appear in the list.
		{"empty string rejected", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := OriginIsAllowed(tc.uri); got != tc.want {
				t.Errorf("OriginIsAllowed(%q) = %v, want %v", tc.uri, got, tc.want)
			}
		})
	}
}

func TestOriginIsOpen(t *testing.T) {
	gin.SetMode(gin.TestMode)
	make := func(path string) *gin.Context {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		req := httptest.NewRequest("GET", path, nil)
		c.Request = req
		return c
	}

	cases := []struct {
		path string
		want bool
	}{
		// Open path prefixes: /v1, /dashboard, /mj. Anything else must
		// require a fuller auth/CORS check upstream.
		{"/v1/chat/completions", true},
		{"/v1", true},
		{"/dashboard/notifications", true},
		{"/mj/submit/imagine", true},

		{"/api/admin", false},
		{"/", false},
		{"/v2/foo", false}, // not /v1

		// Prefix-match only — "/foo/v1" is NOT open despite containing /v1.
		{"/foo/v1", false},
	}
	for _, tc := range cases {
		if got := OriginIsOpen(make(tc.path)); got != tc.want {
			t.Errorf("OriginIsOpen(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// IsOpenAIDalleModel has an explicit carve-out: "gpt-4-dalle" must NOT
// be classified as a dalle image model, because routing it through the
// image-generation API would skip the chat path it actually belongs to.
func TestIsOpenAIDalleModel(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		{"dalle", true},
		{"dall-e-2", true},
		{"dall-e-3", true},
		// Substring-match via `in` would otherwise classify these as dalle
		// (because "dalle" is a substring of "gpt-4-dalle"), but the
		// !Contains("gpt-4-dalle") guard blocks them.
		{"gpt-4-dalle", false},
		// Unrelated models don't hit the list at all.
		{"gpt-4o", false},
		{"claude-3", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsOpenAIDalleModel(tc.model); got != tc.want {
			t.Errorf("IsOpenAIDalleModel(%q) = %v, want %v", tc.model, got, tc.want)
		}
	}
}

// IsVisionModel mixes substring-match ON the allow-list with exact skip
// entries: gpt-4-turbo-preview is in the allow-list via substring of
// "gpt-4-turbo" but is explicitly skipped to avoid false positives on
// the non-vision turbo-preview variant.
func TestIsVisionModel(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		{"gpt-4-vision-preview", true},
		{"gpt-4-turbo", true},
		{"gpt-4-turbo-2024-04-09", true}, // substring of allow-list entry
		{"gpt-4o", true},
		{"gpt-4o-2024-05-13", true},
		{"gemini-pro-vision", true},
		{"claude-3", true},

		// The skip list must win even though "gpt-4-turbo-preview"
		// contains "gpt-4-turbo" (which is in the allow list). A drift
		// that dropped the skip check would re-route non-vision turbo
		// preview calls into the vision pipeline.
		{"gpt-4-turbo-preview", false},

		{"gpt-3.5-turbo", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsVisionModel(tc.model); got != tc.want {
			t.Errorf("IsVisionModel(%q) = %v, want %v", tc.model, got, tc.want)
		}
	}
}
