package i18n

import (
	"strings"
	"testing"
)

// TestLoad_Smoke verifies the embedded catalog loads cleanly. Failure
// here = malformed JSON committed to the tree (build-time mistake).
func TestLoad_Smoke(t *testing.T) {
	r, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	// At minimum we expect the 6 day-1 languages.
	expected := []string{"en", "zh", "ja", "ko", "es", "fr"}
	loaded := r.AvailableLocales()
	for _, want := range expected {
		found := false
		for _, got := range loaded {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected locale %q in loaded set, got %v", want, loaded)
		}
	}
}

// TestResolve_FullCatalogHit — zh.json mirrors en.json fully, so all
// keys must resolve to a non-key string.
func TestResolve_FullCatalogHit(t *testing.T) {
	r, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cases := []struct{ locale, key, want string }{
		{"zh", "form.skip", "跳过 — 让 Agent 自定"},
		{"zh", "form.ppt.audience.label", "受众"},
		{"zh", "form.ppt.audience.exec", "高管"},
		{"en", "form.skip", "Skip — let the agent decide"},
		{"en", "form.ppt.tone.modern_minimal", "Modern minimal"},
	}
	for _, tc := range cases {
		t.Run(tc.locale+"/"+tc.key, func(t *testing.T) {
			got := r.Resolve(tc.locale, tc.key)
			if got != tc.want {
				t.Errorf("Resolve(%q, %q) = %q, want %q", tc.locale, tc.key, got, tc.want)
			}
		})
	}
}

// TestResolve_FallbackToEn — keys absent from a locale's bundle must
// fall through to en.json. Previously this exercised the partial
// coverage of ja/ko/es/fr (which had labels but not option values);
// the 2026-05-12 i18n backfill filled those gaps, so the fallback
// path is now exercised by a synthetic locale that doesn't have any
// non-form.skip key — we use one of the smaller partial catalogs
// (nl) which intentionally ships only labels + form.skip and falls
// back to en for option-value enums.
func TestResolve_FallbackToEn(t *testing.T) {
	r, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// nl.json carries section labels but option-value enums
	// intentionally fall back to en (translation gap documented
	// in nl.json's _meta.purpose). Once those are translated
	// this assertion can be tightened.
	got := r.Resolve("nl", "form.productShot.product_type.electronics")
	want := "Electronics"
	if got != want {
		t.Errorf("expected en fallback %q, got %q", want, got)
	}
}

// TestResolve_UnknownLocale — locale codes the catalog doesn't carry
// must fall back to en. After the 2026-05-12 backfill all 18 platform
// locales have catalogs, so we use a deliberately-bogus code here.
func TestResolve_UnknownLocale(t *testing.T) {
	r, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := r.Resolve("totally-fake-locale-xyz", "form.skip")
	if got != "Skip — let the agent decide" {
		t.Errorf("expected en fallback, got %q", got)
	}
}

// TestResolve_BCP47Stripping — "en-US", "zh-CN" should normalize to
// "en", "zh" respectively. Real-world auth state often delivers
// region-suffixed codes.
func TestResolve_BCP47Stripping(t *testing.T) {
	r, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if r.Resolve("zh-CN", "form.skip") != "跳过 — 让 Agent 自定" {
		t.Errorf("zh-CN should resolve to zh bundle")
	}
	if r.Resolve("EN-US", "form.skip") != "Skip — let the agent decide" {
		t.Errorf("EN-US (mixed case) should normalize and hit en")
	}
}

// TestResolve_MissingFromAllLocales — a key that exists in NO catalog
// returns the key itself (programmer-error fallback).
func TestResolve_MissingFromAllLocales(t *testing.T) {
	r, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	missing := "form.nonexistent.key"
	got := r.Resolve("en", missing)
	if got != missing {
		t.Errorf("missing key should return verbatim, got %q", got)
	}
}

// TestResolve_NilSafety — defensive contract for nil receiver (see
// PR-1's WorkAgentFeatures pattern).
func TestResolve_NilSafety(t *testing.T) {
	var r *Resolver
	if got := r.Resolve("en", "form.skip"); got != "form.skip" {
		t.Errorf("nil receiver should return key verbatim, got %q", got)
	}
	if r.AvailableLocales() != nil {
		t.Errorf("nil receiver AvailableLocales should be nil")
	}
}

// TestDefault_Idempotent — Default() returns the same Resolver on
// repeated calls (sync.Once contract).
func TestDefault_Idempotent(t *testing.T) {
	r1 := Default()
	r2 := Default()
	if r1 != r2 {
		t.Errorf("Default() must return the same instance on repeated calls")
	}
}

// TestResolveMany — batch resolution preserves order.
func TestResolveMany(t *testing.T) {
	r, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	keys := []string{"form.skip", "form.ppt.audience.label", "form.ppt.tone.label"}
	got := r.ResolveMany("zh", keys)
	if len(got) != len(keys) {
		t.Fatalf("ResolveMany length mismatch: got %d, want %d", len(got), len(keys))
	}
	for i, v := range got {
		if v == keys[i] {
			t.Errorf("key %q didn't resolve (got verbatim)", keys[i])
		}
		if strings.TrimSpace(v) == "" {
			t.Errorf("key %q resolved to empty string", keys[i])
		}
	}
}

// TestNormalizeLocale — covers the BCP-47 / underscore / case
// variations the resolver tolerates.
func TestNormalizeLocale(t *testing.T) {
	cases := []struct{ in, want string }{
		{"en", "en"},
		{"EN", "en"},
		{"en-US", "en"},
		{"EN_US", "en"},
		{"  zh-CN  ", "zh"},
		{"", ""},
		{"zh", "zh"},
	}
	for _, tc := range cases {
		got := normalizeLocale(tc.in)
		if got != tc.want {
			t.Errorf("normalizeLocale(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
