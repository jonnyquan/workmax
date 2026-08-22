//go:build desktop

package desktop

import (
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"server/desktop/i18n"
)

// The window's language, and how it gets decided.
//
// Two inputs, in this order:
//
//  1. The stored preference (migration 0016). "en" or "zh" mean the user
//     chose, and nothing overrides that.
//  2. "system" — the default — means "ask the machine". The answer is already
//     arriving on every request the webview makes: WKWebView sends the OS
//     language in Accept-Language. So the sidecar reads the header it is
//     already being handed rather than shelling out to `defaults read` or
//     asking the renderer to report itself over a new route.
//
// Anything unresolvable is English, because the en catalog is the one the
// loader refuses to start without.
//
// The result lives in i18n's process-wide slot, which is the right shape here:
// the preference is machine-scoped (one row, CHECK (id = 1)), so a window has
// exactly one language and there is no per-request locale to thread anywhere.

// applyStoredLocale resolves the stored preference and records it. Called at
// boot and again whenever the preference changes, so a language switch reaches
// the next error message without a restart.
//
// A "system" preference records nothing: the request middleware below owns
// that case, and writing a guess here would briefly make the sidecar disagree
// with the shell about what the machine asked for.
func applyStoredLocale(language string) {
	if language == "" || language == LanguageSystem {
		return
	}
	if !ValidLanguage(language) {
		return
	}
	i18n.SetActiveLocale(language)
}

// localeFromRequests keeps the active locale in step with the machine while
// the preference is "system".
//
// It runs on every request rather than once at boot because the sidecar starts
// before the webview exists — at boot there is no Accept-Language to read. The
// first renderer request supplies one, and every request after that is a cheap
// re-confirmation: a string compare against what is already stored.
//
// A stored en/zh preference short-circuits it, so a user who chose a language
// does not have it quietly taken back by their OS.
func localeFromRequests(preference func() string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if preference() == LanguageSystem {
			if picked := localeFromAcceptLanguage(c.GetHeader("Accept-Language")); picked != "" {
				i18n.SetActiveLocale(picked)
			}
		}
		c.Next()
	}
}

// localeFromAcceptLanguage picks the best supported locale from an
// Accept-Language header, honouring q-values.
//
// Deliberately not server/utils.ResolveLocale: that one is written against a
// gin.Context for the web server's eighteen locales and a "lang" query
// parameter the desktop has no use for. This is the same RFC 7231 shape over
// the two locales the desktop chrome actually ships, in the package that owns
// the desktop's vocabulary.
//
// Returns "" when nothing matches, which leaves the previous answer standing
// rather than forcing English on a machine whose header we simply could not
// read.
func localeFromAcceptLanguage(header string) string {
	if strings.TrimSpace(header) == "" {
		return ""
	}
	type candidate struct {
		locale string
		weight float64
		order  int
	}
	var candidates []candidate
	for i, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		tag, weight := part, 1.0
		if semi := strings.Index(part, ";"); semi >= 0 {
			tag = strings.TrimSpace(part[:semi])
			for _, param := range strings.Split(part[semi+1:], ";") {
				param = strings.TrimSpace(param)
				if !strings.HasPrefix(param, "q=") {
					continue
				}
				if parsed, err := strconv.ParseFloat(strings.TrimPrefix(param, "q="), 64); err == nil {
					weight = parsed
				}
			}
		}
		// q=0 means "explicitly unacceptable", which is a refusal, not a
		// preference — an entry that says "not this one" must not be picked
		// simply because nothing else matched.
		if weight <= 0 {
			continue
		}
		normalized := i18n.NormalizeLocale(tag)
		if normalized == "" || normalized == "*" {
			continue
		}
		if !ValidLanguage(normalized) {
			continue
		}
		candidates = append(candidates, candidate{locale: normalized, weight: weight, order: i})
	}
	if len(candidates) == 0 {
		return ""
	}
	// Stable: equal q-values keep the order the header listed them in, which
	// is what a client means by writing them in that order.
	sort.SliceStable(candidates, func(a, b int) bool {
		if candidates[a].weight != candidates[b].weight {
			return candidates[a].weight > candidates[b].weight
		}
		return candidates[a].order < candidates[b].order
	})
	return candidates[0].locale
}

// languagePreferenceOrSystem reads the stored language, answering "system" for
// any state where the preference cannot be read. Failing to reach the database
// must degrade to following the machine, never to pinning a language.
func (s *Server) languagePreferenceOrSystem() string {
	if s == nil || s.cfg.UIPreferences == nil {
		return LanguageSystem
	}
	dto, err := s.cfg.UIPreferences.Get()
	if err != nil || !ValidLanguage(dto.Language) {
		return LanguageSystem
	}
	return dto.Language
}
