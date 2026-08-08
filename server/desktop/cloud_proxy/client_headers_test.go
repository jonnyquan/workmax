//go:build desktop

package cloud_proxy

import (
	"net/http"
	"testing"

	"server/desktop/buildinfo"
)

func TestSetClientHeaders_StampsBoth(t *testing.T) {
	h := http.Header{}
	SetClientHeaders(h)

	if got := h.Get(HeaderClientName); got != "desktop" {
		t.Errorf("client name: got %q, want %q", got, "desktop")
	}
	if got := h.Get(HeaderClientVersion); got != buildinfo.Version {
		t.Errorf("client version: got %q, want %q", got, buildinfo.Version)
	}
	if buildinfo.Version == "" {
		t.Error("buildinfo.Version must not be empty — every cloud request would ship an empty version header")
	}
}

// TestSetClientHeaders_OverwritesExisting pins idempotent behavior:
// callers that re-stamp (e.g. after a token refresh rebuilds the
// request) shouldn't double-append. http.Header.Set replaces, but
// asserting it explicitly catches future migrations away from Set.
func TestSetClientHeaders_OverwritesExisting(t *testing.T) {
	h := http.Header{}
	h.Set(HeaderClientName, "wrong")
	h.Set(HeaderClientVersion, "wrong")
	SetClientHeaders(h)

	if got := h.Get(HeaderClientName); got != "desktop" {
		t.Errorf("client name: %q (overwrite expected)", got)
	}
	if got := h.Get(HeaderClientVersion); got != buildinfo.Version {
		t.Errorf("client version: %q (overwrite expected)", got)
	}
	// Single value, not appended.
	if len(h.Values(HeaderClientName)) != 1 {
		t.Errorf("client name appended instead of replaced: %v", h.Values(HeaderClientName))
	}
	if len(h.Values(HeaderClientVersion)) != 1 {
		t.Errorf("client version appended instead of replaced: %v", h.Values(HeaderClientVersion))
	}
}
