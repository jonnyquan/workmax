//go:build desktop

package buildinfo

import (
	"strconv"
	"strings"
	"testing"

	"server/middleware"
)

// TestVersionDrift_FloorNotAboveSidecar pins the cross-package
// invariant that the cloud-side version floor never exceeds the
// version of the sidecar we ship. If we publish a sidecar at
// v0.5.0 and bump the cloud floor to v0.6.0, every customer's
// sidecar — including the freshly downloaded one — would see the
// '[desktop-stale]' WARN and (eventually, when we add soft-block)
// the upgrade nag. The path from 'cloud floor bumped' to 'sidecar
// stamp bumped' should never go the wrong direction.
//
// What the test allows:
//   - sidecar AHEAD of floor (typical: ship sidecar first, raise
//     floor in a later release once adoption settles)
//   - sidecar EQUAL to floor (mid-rollout)
//
// What the test forbids:
//   - floor STRICTLY ABOVE sidecar (refuses-self bug)
//
// Unparseable inputs (dev builds like 'main-dirty' as sidecar
// version) skip the check — same forgive-on-parse-fail policy
// the middleware itself uses.
func TestVersionDrift_FloorNotAboveSidecar(t *testing.T) {
	floor, ok := parseSemverTriple(middleware.DesktopMinSupportedVersion)
	if !ok {
		t.Skipf("middleware.DesktopMinSupportedVersion %q unparseable; skipping",
			middleware.DesktopMinSupportedVersion)
	}
	side, ok := parseSemverTriple(Version)
	if !ok {
		t.Skipf("buildinfo.Version %q unparseable (dev build?); skipping", Version)
	}
	for i := 0; i < 3; i++ {
		if floor[i] < side[i] {
			return // sidecar ahead of floor — good
		}
		if floor[i] > side[i] {
			t.Fatalf("cloud floor %v > shipped sidecar version %v — cloud would refuse "+
				"a freshly downloaded sidecar. Either lower middleware.DesktopMinSupportedVersion "+
				"or bump buildinfo.Version first.",
				middleware.DesktopMinSupportedVersion, Version)
		}
	}
	// equal — fine
}

func TestVersionDoesNotUseRetiredMilestoneLabel(t *testing.T) {
	lower := strings.ToLower(Version)
	for _, retired := range []string{"p0", "spike"} {
		if strings.Contains(lower, retired) {
			t.Fatalf("buildinfo.Version %q still contains retired milestone label %q; "+
				"this value is emitted in the handshake and X-WorkMax-Client-Version",
				Version, retired)
		}
	}
}

// parseSemverTriple mirrors middleware.parseSemverTriple (a private
// helper). Duplicated here rather than exported because the
// duplication is small + intent-aligned: this test exists to detect
// a divergence between the two constants; sharing the parser would
// hide the same drift behind a single source.
func parseSemverTriple(v string) ([3]int, bool) {
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}
