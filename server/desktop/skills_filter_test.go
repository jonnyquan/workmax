//go:build desktop

package desktop

import "testing"

func TestIsAgentModeAllowed(t *testing.T) {
	cases := map[string]bool{
		"ppt":             true,
		"":                true, // empty = sidecar normalizes to DefaultDesktopAgentMode
		"flashCard":       false,
		"character":       false,
		"marketingPoster": false,
		"ai.workmax.ppt.svc": false, // future namespaced form is not the same string
		"PPT":             false, // case-sensitive on purpose; the cloud is too
	}
	for in, want := range cases {
		if got := IsAgentModeAllowed(in); got != want {
			t.Errorf("IsAgentModeAllowed(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestAllowedAgentModesSet_MatchesSlice(t *testing.T) {
	set := AllowedAgentModesSet()
	if len(set) != len(DesktopAllowedAgentModes) {
		t.Fatalf("set size %d != slice size %d", len(set), len(DesktopAllowedAgentModes))
	}
	for _, m := range DesktopAllowedAgentModes {
		if _, ok := set[m]; !ok {
			t.Errorf("set missing slice entry %q", m)
		}
	}
}

func TestNormalizeDesktopAgentMode(t *testing.T) {
	cases := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{in: "", want: "ppt", wantOK: true},
		{in: "ppt", want: "ppt", wantOK: true},
		{in: "flashCard", wantOK: false},
	}
	for _, tc := range cases {
		got, ok := NormalizeDesktopAgentMode(tc.in)
		if ok != tc.wantOK || got != tc.want {
			t.Errorf("NormalizeDesktopAgentMode(%q) = (%q, %v), want (%q, %v)",
				tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}

// Pin the current early-access invariant: exactly one allowed mode,
// named "ppt".
// Adding more requires deleting / updating this test, which is the
// signal to also update the desktop README's "what skills work today"
// section.
func TestDesktopAllowlistInvariant(t *testing.T) {
	if got, want := len(DesktopAllowedAgentModes), 1; got != want {
		t.Errorf("desktop allowlist length: got %d, want %d", got, want)
	}
	if !IsAgentModeAllowed("ppt") {
		t.Error("ppt must remain in the desktop allowlist")
	}
}
