//go:build desktop

package desktop

// DesktopAllowedAgentModes is the closed allowlist of skill agentModes
// the desktop sidecar exposes for the current mac-first early-access
// build. The cloud catalog has ~14 skills today; we intentionally ship
// a single-skill surface until each additional mode has been validated
// in the slim Desktop renderer.
//
// Adding a skill is a deliberate product decision, not a config tweak:
//  1. Verify the skill's question-form + post-scripts work without
//     any web-specific UI components the desktop renderer doesn't ship.
//  2. Add the entry here.
//  3. Add a contract test pinning the new allowed mode.
//
// A later public-release/P2 pass can replace this allowlist with a
// per-user / per-tier gate computed at runtime.
const DefaultDesktopAgentMode = "ppt"

var DesktopAllowedAgentModes = []string{
	DefaultDesktopAgentMode,
}

// IsAgentModeAllowed returns true when the given mode is in the desktop
// allowlist. Empty string is accepted only because it normalizes to
// DefaultDesktopAgentMode before the sidecar forwards the request.
//
// This is the only chokepoint the chat handler should call; do NOT
// duplicate the allowlist literal anywhere else.
func IsAgentModeAllowed(mode string) bool {
	_, ok := NormalizeDesktopAgentMode(mode)
	return ok
}

// NormalizeDesktopAgentMode maps the renderer's optional chat_mode
// field onto the explicit Desktop mode the sidecar will forward and
// cache. Empty means "use the Desktop default", not "let the cloud
// choose any default" — otherwise an omitted field could bypass the
// Desktop allowlist by relying on cloud-side defaults.
func NormalizeDesktopAgentMode(mode string) (string, bool) {
	if mode == "" {
		return DefaultDesktopAgentMode, true
	}
	for _, m := range DesktopAllowedAgentModes {
		if m == mode {
			return mode, true
		}
	}
	return "", false
}

// AllowedAgentModesSet returns the allowlist as a string-keyed map
// for callers that prefer O(1) lookup (the skills catalog filter
// uses this to drop non-allowlisted items in one pass over the
// upstream response).
func AllowedAgentModesSet() map[string]struct{} {
	out := make(map[string]struct{}, len(DesktopAllowedAgentModes))
	for _, m := range DesktopAllowedAgentModes {
		out[m] = struct{}{}
	}
	return out
}
