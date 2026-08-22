//go:build desktop

package i18n

import (
	"regexp"
	"strings"
	"testing"
)

func TestCatalogsAgreeOnKeys(t *testing.T) {
	r, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	en := r.catalogs["en"]
	for locale, catalog := range r.catalogs {
		if locale == "en" {
			continue
		}
		for key := range en {
			if _, ok := catalog[key]; !ok {
				t.Errorf("%s is missing %q", locale, key)
			}
		}
		for key := range catalog {
			if _, ok := en[key]; !ok {
				t.Errorf("%s has %q, which en does not — en is the fallback and must be complete", locale, key)
			}
		}
	}
}

func TestResolveFallsBackThroughEnToKey(t *testing.T) {
	r, _ := Load()
	if got := r.Resolve("zh", "proxy.canceled"); got != "请求已取消。" {
		t.Errorf("zh exact: %q", got)
	}
	if got := r.Resolve("zh-CN", "proxy.canceled"); got != "请求已取消。" {
		t.Errorf("region tag must normalise: %q", got)
	}
	if got := r.Resolve("fr", "proxy.canceled"); got != "Request canceled." {
		t.Errorf("unknown locale must fall back to en: %q", got)
	}
	if got := r.Resolve("en", "nope.not.here"); got != "nope.not.here" {
		t.Errorf("a missing key must surface as itself: %q", got)
	}
	if got := r.Resolve("en", "gateway.upstream_error", 503); got == "" || got[len(got)-1] == '%' {
		t.Errorf("interpolation: %q", got)
	}
}

// A property of the COPY, checked in every locale at once.
//
// It used to live in model_gateway_profile_test.go as a substring check
// against one Chinese phrase, which asserted the property only for the
// language that test happened to be written in. The rule is the same either
// way: a refusal that names only the thing you cannot do is a dead end, so
// every gateway refusal must also name the way back to your own endpoint.
func TestGatewayRefusalsAlwaysNameTheWayOut(t *testing.T) {
	// The escape hatch, spelled the way each catalog spells it.
	wayOut := map[string]string{
		"en": "local endpoint",
		"zh": "本地模型 endpoint",
	}
	// The refusals that are about reaching the official gateway. The other
	// gateway.* keys are about a malformed request, which has no such
	// alternative to offer.
	refusals := []string{
		"gateway.unbound",
		"gateway.no_model",
		"gateway.forbidden",
		"gateway.retired_model",
		"gateway.unreachable",
		"gateway.expired",
		"gateway.unavailable",
	}
	r, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for locale, phrase := range wayOut {
		catalog, ok := r.catalogs[locale]
		if !ok {
			t.Fatalf("no %s catalog", locale)
		}
		for _, key := range refusals {
			msg, ok := catalog[key]
			if !ok {
				t.Errorf("%s is missing %q", locale, key)
				continue
			}
			if !strings.Contains(msg, phrase) {
				t.Errorf("%s/%s does not name the way out (%q): %q", locale, key, phrase, msg)
			}
		}
	}
}

// Every catalog entry that carries a fmt verb must carry the SAME verbs in
// every locale: a translation that drops a %d prints a sentence with a hole
// in it, and one that adds a verb prints "%!d(MISSING)" on a user's screen.
func TestVerbsMatchAcrossLocales(t *testing.T) {
	r, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	verbs := func(msg string) []string {
		return regexp.MustCompile(`%[a-zA-Z]`).FindAllString(msg, -1)
	}
	en := r.catalogs[FallbackLocale]
	for locale, catalog := range r.catalogs {
		if locale == FallbackLocale {
			continue
		}
		for key, enMsg := range en {
			msg, ok := catalog[key]
			if !ok {
				continue // key parity is TestCatalogsAgreeOnKeys's job
			}
			want, got := verbs(enMsg), verbs(msg)
			if len(want) != len(got) {
				t.Errorf("%s/%s has verbs %v, en has %v", locale, key, got, want)
				continue
			}
			for i := range want {
				if want[i] != got[i] {
					t.Errorf("%s/%s verb %d is %s, en has %s", locale, key, i, got[i], want[i])
				}
			}
		}
	}
}
