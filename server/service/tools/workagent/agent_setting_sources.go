package workagent

import (
	"strings"
	"sync"

	"server/globals"
)

// settingSourcesInvalidEntryWarnOnce gates the "invalid entry" warn
// so a per-turn validation pass doesn't repeat the same warning per
// build. One-shot per process is enough for an operator to spot the
// typo without log pollution.
var settingSourcesInvalidEntryWarnOnce sync.Once

// validSettingSources is the allow-list the SDK accepts for
// --setting-sources. Mirrors claudesdk.SettingSourceUser /
// SettingSourceProject / SettingSourceLocal — defined here too so we
// can reject typos at config-translation time rather than letting the
// SDK silently drop an invalid entry.
var validSettingSources = map[string]struct{}{
	"user":    {},
	"project": {},
	"local":   {},
}

// translateSettingSources cleans a raw config slice into the slice
// passed to claudesdk.WithSettingSources:
//   - strips whitespace and lowercases
//   - drops empty entries (so a stray YAML "- " doesn't disable the
//     lockdown by collapsing the slice to length 0)
//   - drops invalid entries with a one-shot warn so an operator typo
//     is visible in the log stream
//   - returns nil when the result is empty (caller skips the option)
//
// Validation here keeps the hot path (baseOptions) a simple
// length-check — no per-turn re-validation, no surprise SDK errors
// when an unknown source slipped through.
func translateSettingSources(raw []string) []string {
	if len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, entry := range raw {
		normalized := strings.ToLower(strings.TrimSpace(entry))
		if normalized == "" {
			continue
		}
		if _, ok := validSettingSources[normalized]; !ok {
			settingSourcesInvalidEntryWarnOnce.Do(func() {
				globals.Warn("[Agent] config.setting_sources contains an invalid entry; valid values are user/project/local — invalid entries are dropped")
			})
			continue
		}
		out = append(out, normalized)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
