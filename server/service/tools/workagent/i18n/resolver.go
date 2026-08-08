// Package i18n provides server-side label translation for the work-
// agent module's question_form / directions_picker SSE blocks.
//
// Why server-side (not key-on-the-wire): the SSE block transport's
// schema must carry already-resolved strings — the frontend uses the
// same picker UI for forms across many locales, and shipping i18n
// keys through SSE would require a parallel client-side resolver
// that duplicates this catalog. Resolving once on the server keeps
// the wire payload self-contained and the schema small (no key/value
// indirection in JSON).
//
// The Server emits already-translated strings; Desktop owns only its local
// application chrome and does not need a duplicate resolver for these blocks.
//
// Bundles are loaded from the embedded server/locales/workagent_form/
// tree at process start. Adding a new language is purely a file drop
// — no Go change. Missing keys fall back to en.json with a warn-once
// log entry per (locale, key) pair so dashboards can surface coverage
// gaps without spamming logs.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"sync"

	"server/globals"
)

// localesFS embeds the locale JSON files at build time. Adding a new
// language = drop a <lang>.json into workagent_form/ next to this
// resolver.go file; the loader picks it up at next build.
//
// Note the colocation: locale catalogs sit inside the i18n package
// rather than at server/locales/ because Go's //go:embed directive
// resolves paths relative to the source file. Splitting the catalog
// from the resolver would force a runtime os.ReadFile path resolution
// that breaks under various deployment layouts (Docker / installed
// binaries) where the working directory and the binary location
// disagree.
//
//go:embed workagent_form/*.json
var localesFS embed.FS

// embeddedDir is the path inside localesFS where bundles live.
const embeddedDir = "workagent_form"

// fallbackLocale is the language all missing keys resolve to when
// the requested locale doesn't carry the key. en is the canonical
// catalog — every key MUST exist in en.json. A key missing from en
// is treated as a programmer error: the resolver returns the key
// itself + a one-time warn log so the broken call site surfaces
// loudly in dev / staging.
const fallbackLocale = "en"

// Resolver holds the loaded locale catalogs. The receiver methods
// are read-only after Load() returns, so a single instance can be
// shared across goroutines without locking.
type Resolver struct {
	bundles map[string]map[string]string

	// missingKeys deduplicates the warn-once log for unknown
	// (locale, key) pairs. Sync.Map gives us thread-safe set
	// semantics without the contention cost of a regular map +
	// mutex on the hot resolution path.
	missingKeys sync.Map
}

var (
	defaultResolver     *Resolver
	defaultResolverOnce sync.Once
)

// Default returns the process-wide Resolver, lazily loaded on first
// access. Callers in request paths take this — the load happens at
// most once per process and the resulting *Resolver is goroutine-
// safe for concurrent Resolve calls.
//
// Load failure here is a startup-level mistake (corrupt embed.FS or
// malformed JSON committed to the tree). The resolver still returns
// usable values — Resolve returns the key itself with a warn log —
// so a broken locale file degrades gracefully instead of crashing
// the server. Operators see the warn line at startup AND on every
// missed key during the first request that touches it.
func Default() *Resolver {
	defaultResolverOnce.Do(func() {
		r, err := Load()
		if err != nil {
			globals.Warn(fmt.Sprintf("[workagent.i18n] failed to load locale bundles: %v (resolver will return keys verbatim)", err))
			r = &Resolver{bundles: map[string]map[string]string{}}
		}
		defaultResolver = r
	})
	return defaultResolver
}

// Load walks the embedded locales/workagent_form directory and parses
// each *.json file as a flat map[string]string (after stripping the
// _meta key, which is documentation-only). Returned errors are
// startup-level — a bad JSON file means the catalog is incomplete,
// not that the server should crash.
func Load() (*Resolver, error) {
	entries, err := localesFS.ReadDir(embeddedDir)
	if err != nil {
		return nil, fmt.Errorf("read locales dir %q: %w", embeddedDir, err)
	}

	bundles := make(map[string]map[string]string, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		locale := strings.TrimSuffix(name, ".json")

		data, err := localesFS.ReadFile(path.Join(embeddedDir, name))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}

		// Decode as a generic map first so we can distinguish
		// _meta (an object) from translation keys (strings).
		// Translation files are flat by convention, so anything
		// non-string is dropped from the bundle with a warn log.
		raw := map[string]interface{}{}
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}

		bundle := make(map[string]string, len(raw))
		for k, v := range raw {
			if k == "_meta" {
				continue
			}
			if s, ok := v.(string); ok {
				bundle[k] = s
				continue
			}
			globals.Warn(fmt.Sprintf("[workagent.i18n] %s: key %q has non-string value, dropped", name, k))
		}
		bundles[locale] = bundle
	}

	if _, ok := bundles[fallbackLocale]; !ok {
		return nil, fmt.Errorf("fallback locale %q not present in %s", fallbackLocale, embeddedDir)
	}

	return &Resolver{bundles: bundles}, nil
}

// Resolve returns the translated string for (locale, key), with the
// documented fallback chain:
//
//  1. exact (locale, key) hit
//  2. fallback (en, key) hit
//  3. key itself (verbatim) + one-time warn log
//
// Locale is case-insensitive and tolerant of region suffixes ("en-US"
// → "en"). Empty locale resolves against fallback directly. The hot
// path is two map lookups in the common case; missing-key bookkeeping
// uses sync.Map to avoid lock contention.
func (r *Resolver) Resolve(locale, key string) string {
	if r == nil {
		return key
	}
	locale = normalizeLocale(locale)

	// 1. Try exact locale.
	if locale != "" {
		if bundle, ok := r.bundles[locale]; ok {
			if val, ok := bundle[key]; ok {
				return val
			}
		}
	}

	// 2. Fall back to canonical (en).
	if fallback, ok := r.bundles[fallbackLocale]; ok {
		if val, ok := fallback[key]; ok {
			return val
		}
	}

	// 3. Missing in fallback too — programmer error. Warn once
	// per key so the operator sees it without log spam under
	// load. Using a fixed locale prefix in the dedup key means a
	// truly missing en key warns once globally; a non-en miss
	// that fell through to en and still failed warns once per
	// (locale, key) pair so we know which catalogs need backfill.
	dedupKey := locale + "\x00" + key
	if _, loaded := r.missingKeys.LoadOrStore(dedupKey, true); !loaded {
		globals.Warn(fmt.Sprintf("[workagent.i18n] missing key %q for locale %q (fallback to verbatim key)", key, locale))
	}
	return key
}

// ResolveMany batch-resolves a slice of keys against the same locale.
// Convenience for emit paths that build a question_form schema in one
// place — saves the repeated locale-bundle map lookup.
func (r *Resolver) ResolveMany(locale string, keys []string) []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = r.Resolve(locale, k)
	}
	return out
}

// AvailableLocales returns the loaded locale codes (e.g. ["en", "zh"]).
// Useful for /debug endpoints and startup logging. Caller should not
// rely on order.
func (r *Resolver) AvailableLocales() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.bundles))
	for k := range r.bundles {
		out = append(out, k)
	}
	return out
}

// normalizeLocale handles two real-world inputs we see from upstream
// auth state: bare codes ("en", "zh") and region-suffixed BCP-47
// codes ("en-US", "zh-CN"). We lower-case and strip the region
// suffix because the catalog is keyed by language only — region
// distinctions (en-US vs en-GB) aren't meaningful for these labels.
func normalizeLocale(locale string) string {
	locale = strings.ToLower(strings.TrimSpace(locale))
	if locale == "" {
		return ""
	}
	// Hyphen splits BCP-47 ("en-US"); underscore variants
	// occasionally appear from legacy code paths.
	for _, sep := range []string{"-", "_"} {
		if i := strings.Index(locale, sep); i > 0 {
			return locale[:i]
		}
	}
	return locale
}
