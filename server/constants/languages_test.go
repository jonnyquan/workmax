package constants

import (
	"testing"
)

// languages.go exposes the SupportedLanguages catalog and three helpers
// that drive admin_system_api's /languages endpoints and the frontend
// locale dropdowns. Silent drift in any of these would either show the
// wrong flag next to a locale, hide a supported locale from the admin
// "multi-language content" list, or miss-classify an incoming language
// code as unsupported. Pin the contract here rather than relying on the
// admin endpoint's integration coverage alone.

// Every entry must have value/name/flag — an entry missing any of these
// would render as a blank option in the UI. Values must also be unique
// so GetLanguageByCode can treat the list as a map.
func TestSupportedLanguages_SchemaAndUniqueness(t *testing.T) {
	if len(SupportedLanguages) == 0 {
		t.Fatalf("SupportedLanguages must not be empty — admin dropdowns depend on it")
	}

	seen := make(map[string]struct{}, len(SupportedLanguages))
	for i, lang := range SupportedLanguages {
		for _, key := range []string{"value", "name", "flag"} {
			v, ok := lang[key]
			if !ok {
				t.Fatalf("entry %d missing %q", i, key)
			}
			s, isStr := v.(string)
			if !isStr || s == "" {
				t.Fatalf("entry %d field %q must be non-empty string, got %T %v", i, key, v, v)
			}
		}
		code := lang["value"].(string)
		if _, dup := seen[code]; dup {
			t.Fatalf("duplicate language code %q — GetLanguageByCode assumes uniqueness", code)
		}
		seen[code] = struct{}{}
	}

	// English MUST be in the catalog because GetMultiLanguageList filters
	// it OUT; if it ever disappears, the multi-language logic silently
	// becomes a no-op instead of failing loudly.
	if _, ok := seen["en"]; !ok {
		t.Fatalf(`SupportedLanguages must contain "en" — GetMultiLanguageList's exclusion depends on it`)
	}
}

// GetMultiLanguageList is the "locales we need to translate INTO" list —
// English is the source, so it's filtered out. Removing the filter would
// cause the admin UI to translate English content to English.
func TestGetMultiLanguageList_ExcludesEnglish(t *testing.T) {
	list := GetMultiLanguageList()

	if len(list) != len(SupportedLanguages)-1 {
		t.Fatalf("expected len == len(SupportedLanguages)-1 = %d, got %d",
			len(SupportedLanguages)-1, len(list))
	}

	for _, lang := range list {
		if lang["value"] == "en" {
			t.Fatalf("GetMultiLanguageList must NOT include English — it's the source locale")
		}
	}

	// Ordering follows SupportedLanguages, minus English. Pinning this
	// lets the admin UI rely on a stable order without an extra sort.
	expected := make([]string, 0, len(SupportedLanguages)-1)
	for _, lang := range SupportedLanguages {
		if lang["value"] != "en" {
			expected = append(expected, lang["value"].(string))
		}
	}
	for i, lang := range list {
		if lang["value"] != expected[i] {
			t.Fatalf("ordering drift at index %d: got %q, want %q", i, lang["value"], expected[i])
		}
	}
}

func TestGetLanguageByCode(t *testing.T) {
	cases := []struct {
		code     string
		wantNil  bool
		wantName string
	}{
		{"en", false, "English"},
		{"zh", false, "中文"},
		{"zh-tw", false, "繁體中文"},
		{"ja", false, "日本語"},
		// Case-sensitive lookup — "EN" must not match "en". If a future
		// consumer wants case-insensitive behaviour, it should normalize
		// BEFORE calling, rather than relaxing this contract and silently
		// accepting malformed codes into persistence.
		{"EN", true, ""},
		// Unknown code → nil (not an empty map), so callers can branch
		// on `if lang == nil`.
		{"xx", true, ""},
		{"", true, ""},
	}

	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			got := GetLanguageByCode(tc.code)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("GetLanguageByCode(%q) = %v, want nil", tc.code, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("GetLanguageByCode(%q) = nil, want non-nil", tc.code)
			}
			if got["name"] != tc.wantName {
				t.Fatalf("GetLanguageByCode(%q).name = %q, want %q", tc.code, got["name"], tc.wantName)
			}
			if got["value"] != tc.code {
				t.Fatalf("returned entry has wrong value: %q vs lookup key %q", got["value"], tc.code)
			}
		})
	}
}

// IsLanguageSupported is the thin bool wrapper most callers actually
// use — keep it symmetric with GetLanguageByCode so a lookup that
// returns nil is always IsLanguageSupported=false.
func TestIsLanguageSupported(t *testing.T) {
	cases := []struct {
		code string
		want bool
	}{
		{"en", true},
		{"zh", true},
		{"ja", true},
		{"pl", true}, // at the tail of the catalog — catches off-by-one
		{"EN", false},
		{"xx", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsLanguageSupported(tc.code); got != tc.want {
			t.Errorf("IsLanguageSupported(%q) = %v, want %v", tc.code, got, tc.want)
		}
	}
}
