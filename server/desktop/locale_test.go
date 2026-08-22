//go:build desktop

package desktop

import (
	"testing"

	"server/desktop/i18n"
)

// The header the webview already sends is the machine's answer. These are the
// shapes a real Accept-Language carries, including the two that must NOT be
// treated as preferences: a wildcard, and an explicit q=0 refusal.
func TestLocaleFromAcceptLanguage(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   string
	}{
		{"empty", "", ""},
		{"plain", "zh", "zh"},
		{"region tag normalises", "zh-CN", "zh"},
		{"first wins at equal weight", "en-US,zh;q=1.0", "en"},
		{"q values are honoured over order", "en;q=0.3,zh;q=0.9", "zh"},
		{"unsupported locales are skipped", "fr-FR,de;q=0.9,zh;q=0.5", "zh"},
		{"nothing supported answers nothing", "fr-FR,de;q=0.9", ""},
		// A wildcard says "anything", which is not a request for a language;
		// answering "" leaves whatever was already resolved standing.
		{"wildcard is not a choice", "*", ""},
		// q=0 is "explicitly unacceptable" — it must never be selected just
		// because it is the only supported tag present.
		{"q=0 is a refusal, not a preference", "zh;q=0", ""},
		{"refusal does not block a real choice", "zh;q=0,en", "en"},
		{"whitespace is tolerated", "  zh-Hans ;  q=0.8 , en ; q=0.2 ", "zh"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := localeFromAcceptLanguage(tc.header); got != tc.want {
				t.Errorf("localeFromAcceptLanguage(%q) = %q, want %q", tc.header, got, tc.want)
			}
		})
	}
}

// A stored choice is a choice: the machine's own language must not quietly
// take it back on the next request.
func TestApplyStoredLocaleIgnoresSystemAndJunk(t *testing.T) {
	for _, value := range []string{"", LanguageSystem, "klingon", "en-US-x-hacker"} {
		before := i18n.ActiveLocale()
		applyStoredLocale(value)
		if got := i18n.ActiveLocale(); got != before {
			t.Errorf("applyStoredLocale(%q) changed the active locale to %q", value, got)
		}
	}
	applyStoredLocale(LanguageZH)
	if got := i18n.ActiveLocale(); got != "zh" {
		t.Errorf("a stored zh must be applied, got %q", got)
	}
	applyStoredLocale(LanguageEN)
	if got := i18n.ActiveLocale(); got != "en" {
		t.Errorf("a stored en must be applied, got %q", got)
	}
}
