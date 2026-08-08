package utils

import (
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// SupportedLocales is the canonical Server locale list. Order is not
// significant; Desktop clients discover/project supported locales from the
// Server contract rather than maintaining an independent catalog.
var SupportedLocales = []string{
	"en", "zh", "es", "fr", "de", "ja", "ko", "ru", "it", "pt",
	"nl", "ar", "th", "vi", "sv", "tr", "pl", "he",
}

// DefaultLocale is the fallback when a request supplies no locale or
// supplies an unsupported one. Also the default lang for system
// content rows in the DB-side i18n pattern.
const DefaultLocale = "en"

var supportedLocaleSet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(SupportedLocales))
	for _, l := range SupportedLocales {
		m[l] = struct{}{}
	}
	return m
}()

// ResolveLocale picks the active locale for a request, in priority
// order:
//
//  1. ?lang= query param (explicit override; preferred for SPAs that
//     already know which locale the user is in and want round-trips
//     to be deterministic).
//  2. Accept-Language header — full RFC 7231 q-value parse: every
//     entry is normalised, sorted by descending q (defaulting to
//     1.0), and the first entry that maps to a SupportedLocales
//     value wins. Unsupported entries are skipped, not treated as
//     terminal, so e.g. "tlh,en;q=0.9" correctly returns 'en'
//     rather than falling through to the platform default.
//  3. DefaultLocale.
//
// Region tags are stripped: en-US → en, zh-CN → zh. Unknown locales
// fall through to the next source rather than being reflected back
// (so handlers can trust the result is in SupportedLocales without
// an extra check).
func ResolveLocale(c *gin.Context) string {
	if v := normaliseLocale(c.Query("lang")); v != "" {
		return v
	}
	if header := c.GetHeader("Accept-Language"); header != "" {
		if v := pickFromAcceptLanguage(header); v != "" {
			return v
		}
	}
	return DefaultLocale
}

// pickFromAcceptLanguage walks an Accept-Language header in q-priority
// order and returns the first entry that normalises to a supported
// locale. Returns "" when nothing matches so ResolveLocale can fall
// through to DefaultLocale.
//
// Header shape per RFC 7231 §5.3.5 — comma-separated entries, each
// "<tag>(;q=<float>)?". Missing q means q=1.0. Stable sort is required
// so ties preserve the client's listed order (matching browser
// expectations).
func pickFromAcceptLanguage(header string) string {
	type entry struct {
		tag string
		q   float64
	}
	var entries []entry
	for _, raw := range strings.Split(header, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		tag := raw
		q := 1.0
		if i := strings.IndexByte(raw, ';'); i >= 0 {
			tag = strings.TrimSpace(raw[:i])
			// Walk parameters; only q= matters for prioritisation.
			for _, param := range strings.Split(raw[i+1:], ";") {
				param = strings.TrimSpace(param)
				if !strings.HasPrefix(param, "q=") {
					continue
				}
				if parsed, err := strconv.ParseFloat(param[2:], 64); err == nil {
					q = parsed
				}
			}
		}
		// q=0 means "explicitly do not want this language" per RFC.
		if q <= 0 {
			continue
		}
		entries = append(entries, entry{tag: tag, q: q})
	}
	if len(entries) == 0 {
		return ""
	}
	// Stable sort by descending q so equal-q entries keep their listed
	// order (browsers send most-preferred first when q's tie).
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].q > entries[j].q
	})
	for _, e := range entries {
		if v := normaliseLocale(e.tag); v != "" {
			return v
		}
	}
	return ""
}

// normaliseLocale lower-cases the input, accepts a direct match, then
// falls back to the language portion of a region-tagged code (en-US
// → en). Returns "" if nothing in SupportedLocales matches so the
// caller can move to the next source.
func normaliseLocale(raw string) string {
	if raw == "" {
		return ""
	}
	lower := strings.ToLower(strings.TrimSpace(raw))
	if _, ok := supportedLocaleSet[lower]; ok {
		return lower
	}
	if i := strings.IndexAny(lower, "-_"); i > 0 {
		base := lower[:i]
		if _, ok := supportedLocaleSet[base]; ok {
			return base
		}
	}
	return ""
}
