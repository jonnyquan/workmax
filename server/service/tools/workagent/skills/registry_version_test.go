package skills

import "testing"

// registry_version_test.go — F4 (2026-05-17). Covers the
// observability helper ResolveVersion. The agent_processor log
// line pairs the skill name with this value so silent
// skill-version bumps land in the per-turn log stream.

// TestResolveVersion_KnownSkill returns the manifest version.
// Locked to ppt — the test will catch a regression that
// accidentally returns "unknown" when the bundle resolves fine.
func TestResolveVersion_KnownSkill(t *testing.T) {
	r := NewRegistry(nil)
	got := r.ResolveVersion("ppt")
	if got == "unknown" || got == "" {
		t.Errorf("ppt should resolve to a real version, got %q", got)
	}
	// All 14 user-facing skills today ship v2.0.0 (Stage D
	// graduation 2026-05-12). Loosen to "starts with 2." so a
	// future minor bump doesn't trip the test.
	if len(got) < 2 || got[0] != '2' || got[1] != '.' {
		t.Errorf("ppt version should be 2.x today, got %q", got)
	}
}

// TestResolveVersion_EmptyModeShortCircuits — defensive guard,
// no DB / fs lookup attempted.
func TestResolveVersion_EmptyModeShortCircuits(t *testing.T) {
	r := NewRegistry(nil)
	if got := r.ResolveVersion(""); got != "unknown" {
		t.Errorf("empty mode must short-circuit to 'unknown', got %q", got)
	}
}

// TestResolveVersion_NonExistentSkill — a skill name with no
// manifest is now an authoring/caller error. ResolveVersion is
// observability-only, so it collapses that failure to "unknown"
// instead of propagating.
func TestResolveVersion_NonExistentSkill(t *testing.T) {
	r := NewRegistry(nil)
	if got := r.ResolveVersion("definitely-not-a-skill"); got != "unknown" {
		t.Errorf("nonexistent skill must surface as 'unknown', got %q", got)
	}
}
