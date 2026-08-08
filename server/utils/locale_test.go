package utils

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// Locale resolver is the read-side of the platform DB-i18n pattern —
// any handler reading translatable content depends on it returning a
// supported locale or DefaultLocale, never anything else. Test the
// priority chain (query param → Accept-Language → fallback) and the
// region-tag normalisation.

func newCtx(t *testing.T, query string, header string) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	url := "/?"
	if query != "" {
		url += query
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if header != "" {
		req.Header.Set("Accept-Language", header)
	}
	c.Request = req
	return c
}

func TestResolveLocale_QueryParamWinsOverHeader(t *testing.T) {
	// Explicit ?lang= must override the header — SPAs that already
	// know the active locale rely on this for round-trip determinism.
	c := newCtx(t, "lang=zh", "ja,en;q=0.9")
	if got := ResolveLocale(c); got != "zh" {
		t.Errorf("got %q, want zh", got)
	}
}

func TestResolveLocale_FallsBackToHeaderWhenNoQuery(t *testing.T) {
	c := newCtx(t, "", "fr-FR,en;q=0.8")
	if got := ResolveLocale(c); got != "fr" {
		t.Errorf("got %q, want fr (region stripped)", got)
	}
}

func TestResolveLocale_DefaultsWhenBothMissing(t *testing.T) {
	c := newCtx(t, "", "")
	if got := ResolveLocale(c); got != DefaultLocale {
		t.Errorf("got %q, want default %q", got, DefaultLocale)
	}
}

func TestResolveLocale_UnsupportedLocaleFallsThroughToDefault(t *testing.T) {
	// Query-param "klingon" isn't supported → fall through to the
	// header. Header is also unsupported → fall through to default.
	c := newCtx(t, "lang=tlh", "klingon")
	if got := ResolveLocale(c); got != DefaultLocale {
		t.Errorf("got %q, want default %q", got, DefaultLocale)
	}
}

func TestResolveLocale_UnsupportedQueryFallsToHeader(t *testing.T) {
	// Query says klingon (unsupported), header says zh — use header.
	c := newCtx(t, "lang=tlh", "zh-CN")
	if got := ResolveLocale(c); got != "zh" {
		t.Errorf("got %q, want zh from header", got)
	}
}

func TestResolveLocale_RegionTagsNormalise(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"en-US", "en"},
		{"EN_GB", "en"},
		{"zh-cn", "zh"},
		{"pt-BR", "pt"},
	} {
		c := newCtx(t, "lang="+tc.raw, "")
		if got := ResolveLocale(c); got != tc.want {
			t.Errorf("%q → %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestResolveLocale_AcceptLanguagePicksFirstSupportedByPriority(t *testing.T) {
	// Standard browser header: highest-priority entry wins. ja is in
	// SupportedLocales so it's returned; q-values aren't used to
	// promote en-US over ja here (ja's implicit q=1 outranks en-US's q=0.8).
	c := newCtx(t, "", "ja-JP,ja;q=0.9,en-US;q=0.8")
	if got := ResolveLocale(c); got != "ja" {
		t.Errorf("got %q, want ja", got)
	}
}

func TestResolveLocale_AcceptLanguageFallsThroughUnsupportedHead(t *testing.T) {
	// First entry unsupported (tlh = klingon). The pre-rewrite resolver
	// stopped here and dropped to DefaultLocale; the new resolver must
	// keep walking and return the next supported entry.
	c := newCtx(t, "", "tlh,en;q=0.9")
	if got := ResolveLocale(c); got != "en" {
		t.Errorf("got %q, want en (fall-through past unsupported head)", got)
	}
}

func TestResolveLocale_AcceptLanguageRespectsExplicitQOrdering(t *testing.T) {
	// Lower-priority listed FIRST but with higher q. RFC 7231 says q
	// sorts wins; both are supported, so q-priority decides.
	c := newCtx(t, "", "en;q=0.5,zh;q=0.9")
	if got := ResolveLocale(c); got != "zh" {
		t.Errorf("got %q, want zh (higher q wins regardless of listed order)", got)
	}
}

func TestResolveLocale_AcceptLanguageQZeroMeansSkip(t *testing.T) {
	// RFC 7231 §5.3.1: "q=0" explicitly rejects a language. We must
	// not return it even if it's the only entry.
	c := newCtx(t, "", "zh;q=0,en;q=0.5")
	if got := ResolveLocale(c); got != "en" {
		t.Errorf("got %q, want en (zh excluded by q=0)", got)
	}
	// Whole header rejected → DefaultLocale.
	c = newCtx(t, "", "zh;q=0,fr;q=0")
	if got := ResolveLocale(c); got != DefaultLocale {
		t.Errorf("got %q, want default %q (all q=0)", got, DefaultLocale)
	}
}
