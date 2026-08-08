package canvas

import "testing"

// Pins the canvas upload URL-prefix helpers + the matching root
// constants. These get embedded in canvas project documents (asset
// URLs) and read by the asset-resolver path checks; a silent format
// shift would orphan every historical document that already has the
// old shape baked in.

func TestCanvasUploadURLRoot_IsTheCanonicalPrefix(t *testing.T) {
	// The root literal lives in every saved canvas project file URL.
	// If a refactor moves uploads under /static/ or behind a CDN
	// rewrite, this test catches it before historical projects break.
	if canvasUploadURLRoot != "/uploads/canvas/uid/" {
		t.Errorf("canvasUploadURLRoot = %q, want /uploads/canvas/uid/", canvasUploadURLRoot)
	}
	if canvasUploadLegacyURLRoot != "/canvas/uid/" {
		t.Errorf("canvasUploadLegacyURLRoot = %q, want /canvas/uid/", canvasUploadLegacyURLRoot)
	}
}

func TestCanvasUploadURLPrefix_FormatsAsExpected(t *testing.T) {
	got := canvasUploadURLPrefix(42, "proj-abc")
	want := "/uploads/canvas/uid/42/proj-abc/"
	if got != want {
		t.Errorf("canvasUploadURLPrefix(42, proj-abc) = %q, want %q", got, want)
	}
}

func TestCanvasUploadURLPrefix_AlwaysEndsWithSlash(t *testing.T) {
	// HasPrefix-based path checks rely on the trailing slash so a
	// project UUID prefix doesn't accidentally match a longer UUID
	// (e.g. /uploads/canvas/uid/1/abc/ must NOT match
	// /uploads/canvas/uid/1/abcd/). Pin the trailing slash.
	cases := []struct {
		uid  int
		uuid string
	}{
		{1, "a"},
		{0, ""},
		{999999, "long-uuid-string-with-hyphens"},
	}
	for _, c := range cases {
		got := canvasUploadURLPrefix(c.uid, c.uuid)
		if got == "" || got[len(got)-1] != '/' {
			t.Errorf("canvasUploadURLPrefix(%d, %q) = %q, missing trailing slash", c.uid, c.uuid, got)
		}
	}
}

func TestCanvasUploadLegacyURLPrefix_DistinctFromCanonical(t *testing.T) {
	// Legacy path must NOT collide with canonical. A future refactor
	// that aligns the roots would silently route historical /canvas/uid/
	// paths through the canonical resolver — possibly intended, but
	// must update tests deliberately.
	canonical := canvasUploadURLPrefix(7, "uuid-x")
	legacy := canvasUploadLegacyURLPrefix(7, "uuid-x")
	if canonical == legacy {
		t.Errorf("canonical and legacy prefixes collided: %q", canonical)
	}
	if legacy != "/canvas/uid/7/uuid-x/" {
		t.Errorf("canvasUploadLegacyURLPrefix(7, uuid-x) = %q, want /canvas/uid/7/uuid-x/", legacy)
	}
}

func TestCanvasUploadURLPrefix_RoundTripsThroughLegacyRoot(t *testing.T) {
	// isCanvasProjectAssetPath accepts EITHER root + a matching
	// /{projectUUID}/ segment. Sanity-check that both prefixes
	// produced by the helpers carry the project UUID in the same
	// position so the resolver's strings.Contains check fires for
	// either input.
	prefixCanonical := canvasUploadURLPrefix(5, "proj-zzz")
	prefixLegacy := canvasUploadLegacyURLPrefix(5, "proj-zzz")
	for _, prefix := range []string{prefixCanonical, prefixLegacy} {
		if !contains(prefix, "/proj-zzz/") {
			t.Errorf("prefix %q does not contain /proj-zzz/ — resolver would miss this path", prefix)
		}
	}
}

func contains(haystack, needle string) bool {
	if len(needle) > len(haystack) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
