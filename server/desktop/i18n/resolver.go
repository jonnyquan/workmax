//go:build desktop

// Package i18n resolves the desktop sidecar's user-facing copy.
//
// Why a second resolver, when server/service/tools/workagent/i18n already
// exists and ships eighteen locales: that one imports server/globals, which
// pulls in redis and gorm-MySQL. server/desktop imports server/globals exactly
// zero times, and that count is a maintained boundary — the sidecar is a
// SQLite-only binary behind the `desktop` build tag, and the first edge into
// the web server's globals would drag its whole dependency tree in behind it.
// The shapes below are deliberately the same (flat dotted keys, an embedded
// catalog per locale, an exact → en → key fallback chain) so the two read as
// one idea in two places rather than two ideas.
//
// What this one adds is interpolation. The Server's resolver answers static
// labels; the sidecar's copy names files, endpoints and counts ("pi exited
// before the turn finished", "%s HTTP %d"), so a catalog of flat strings would
// have forced those back into Go string concatenation — which is precisely
// where a translation loses its word order.
//
// Adding a locale is a file drop into messages/, plus the vocabulary in
// local_ui_preference.go and migration 0016 (the value reaches <html lang>, so
// it is a closed set rather than anything the catalog happens to contain).
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"sync"
	"sync/atomic"
)

// messagesFS embeds the catalogs at build time. Colocated with this file for
// the same reason the Server's are: //go:embed resolves relative to the source
// file, so splitting catalog from resolver would force a runtime path lookup
// that breaks whenever the working directory and the binary disagree — which
// on a packaged Mac app is always.
//
//go:embed messages/*.json
var messagesFS embed.FS

const embeddedDir = "messages"

// FallbackLocale is the locale every lookup falls back to, and the one the
// loader refuses to start without.
const FallbackLocale = "en"

// Resolver holds one catalog per locale.
type Resolver struct {
	catalogs map[string]map[string]string

	// missing warns once per (locale, key) rather than on every lookup: a
	// missing key in a hot path would otherwise turn a copy gap into a log
	// flood, and the first report is the only one that carries information.
	missing sync.Map
}

var (
	defaultOnce sync.Once
	defaultRes  *Resolver
	defaultErr  error
)

// Default returns the process-wide resolver, loading it on first use.
//
// A load failure is remembered rather than retried: the catalogs are embedded,
// so a failure is a build-time mistake that will not fix itself at runtime.
func Default() (*Resolver, error) {
	defaultOnce.Do(func() { defaultRes, defaultErr = Load() })
	return defaultRes, defaultErr
}

// Load parses every embedded catalog.
func Load() (*Resolver, error) {
	entries, err := messagesFS.ReadDir(embeddedDir)
	if err != nil {
		return nil, fmt.Errorf("i18n: read catalog dir: %w", err)
	}
	r := &Resolver{catalogs: make(map[string]map[string]string, len(entries))}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := messagesFS.ReadFile(path.Join(embeddedDir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("i18n: read %s: %w", entry.Name(), err)
		}
		// Two-stage parse. "_meta" documents the file for whoever edits it
		// next — it is an object, not a message — so a direct
		// map[string]string unmarshal would reject the whole catalog on it.
		// Reading values as RawMessage first lets _meta be any shape while
		// still failing loudly on a MESSAGE that is not a string, which is
		// the mistake actually worth catching.
		var rawCatalog map[string]json.RawMessage
		if err := json.Unmarshal(raw, &rawCatalog); err != nil {
			return nil, fmt.Errorf("i18n: parse %s: %w", entry.Name(), err)
		}
		catalog := make(map[string]string, len(rawCatalog))
		for key, value := range rawCatalog {
			if key == "_meta" {
				continue
			}
			var msg string
			if err := json.Unmarshal(value, &msg); err != nil {
				return nil, fmt.Errorf("i18n: %s: %q is not a string: %w", entry.Name(), key, err)
			}
			catalog[key] = msg
		}
		r.catalogs[strings.TrimSuffix(entry.Name(), ".json")] = catalog
	}
	if len(r.catalogs[FallbackLocale]) == 0 {
		return nil, fmt.Errorf("i18n: %s catalog is missing or empty", FallbackLocale)
	}
	return r, nil
}

// Resolve returns the message for a key in a locale, formatted with args.
//
// The chain is exact → en → the key itself. Returning the key rather than an
// empty string is deliberate: a user who sees "agent.pi.start_failed" has been
// told something is wrong AND handed the term to report, whereas empty copy
// looks like the application forgot to say anything.
//
// Args are applied with fmt.Sprintf, so a catalog entry's verbs and their
// order belong to the translation — which is the whole point of not building
// these sentences by concatenation in Go.
func (r *Resolver) Resolve(locale, key string, args ...any) string {
	if r == nil {
		return format(key, args)
	}
	normalized := NormalizeLocale(locale)
	if catalog, ok := r.catalogs[normalized]; ok {
		if msg, ok := catalog[key]; ok {
			return format(msg, args)
		}
		r.warnMissing(normalized, key)
	}
	if catalog, ok := r.catalogs[FallbackLocale]; ok {
		if msg, ok := catalog[key]; ok {
			return format(msg, args)
		}
	}
	return format(key, args)
}

// AvailableLocales lists the catalogs that loaded.
func (r *Resolver) AvailableLocales() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.catalogs))
	for locale := range r.catalogs {
		out = append(out, locale)
	}
	return out
}

func (r *Resolver) warnMissing(locale, key string) {
	r.missing.LoadOrStore(locale+"\x00"+key, true)
}

// format applies args only when there are any: a message containing a literal
// percent sign must survive a lookup that passes nothing.
func format(msg string, args []any) string {
	if len(args) == 0 {
		return msg
	}
	return fmt.Sprintf(msg, args...)
}

// --- The active locale ------------------------------------------------------
//
// One process, one locale — and that is the model, not a shortcut. The
// language is a MACHINE-level preference (migration 0016, one row, CHECK
// (id = 1)), for the reason appearance and density are: the identities on a
// machine are one pair of eyes. There is no per-request locale to thread
// through, and threading one would invite the two halves of a single window
// to disagree.
//
// Held in an atomic rather than a mutex because the read is on the error path
// of every turn and the write happens when a preference changes.
var activeLocale atomic.Pointer[string]

// SetActiveLocale records the locale the window is currently speaking. An
// empty or unrecognised value means "not decided", and lookups fall back to
// en — which is also what a sidecar that never got told will answer.
func SetActiveLocale(locale string) {
	normalized := NormalizeLocale(locale)
	activeLocale.Store(&normalized)
}

// ActiveLocale reports the current locale, or "" if none has been set.
func ActiveLocale() string {
	if p := activeLocale.Load(); p != nil {
		return *p
	}
	return ""
}

// T resolves a key in the active locale. This is the form nearly every call
// site wants: the copy is user-facing, the window has one language, and a
// caller that had to fetch a resolver and a locale first would be a caller
// tempted to skip the lookup.
//
// A catalog that failed to load answers the key itself rather than panicking:
// an application that will not start because a translation is missing is
// worse than one that shows a key.
func T(key string, args ...any) string {
	r, err := Default()
	if err != nil {
		return format(key, args)
	}
	return r.Resolve(ActiveLocale(), key, args...)
}

// NormalizeLocale reduces a BCP-47 tag to the catalog's granularity: "zh-CN"
// and "zh_Hans" both resolve against "zh". Region-specific copy would need a
// region-specific catalog, and there is deliberately none yet.
func NormalizeLocale(locale string) string {
	locale = strings.ToLower(strings.TrimSpace(locale))
	if locale == "" {
		return ""
	}
	for _, sep := range []string{"-", "_"} {
		if i := strings.Index(locale, sep); i > 0 {
			return locale[:i]
		}
	}
	return locale
}
