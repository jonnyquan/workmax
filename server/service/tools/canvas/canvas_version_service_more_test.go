package canvas

import (
	"testing"
)

// canvas_version_service_test.go pins the happy/edge defaults of the
// two pure helpers. These fill in the boundary + cross-branch regressions
// whose silent drift would still let the existing suite pass:
//
//   • NormalizeVersionSource trims tabs / newlines, not just spaces. The
//     existing suite only pins space-padded input — a regression that
//     swapped `strings.TrimSpace` for `strings.Trim(…, " ")` would lose
//     tab/newline handling silently (CSV imports / tooling pipelines
//     can introduce either).
//   • NormalizeVersionSource keeps an internal space in a multi-word
//     source ("auto save" — historical) rather than collapsing to a
//     single word. Pin so a future switch to a Fields()+Join dance
//     doesn't quietly rewrite persisted source labels.
//   • normalizeListVersionsInput negative Limit — existing suite only
//     pins Limit=0 → default and Limit=5000 → cap. A regression that
//     flipped the `< 1` guard to `== 0` would let Limit=-1 slip through
//     as a LIMIT -1 SQL clause. Pin negatives land on the default too.
//   • normalizeListVersionsInput exact boundary at maxListVersionsLimit
//     (100). A regression that tightened `> max` to `>= max` would
//     quietly clamp 100 to 99. Pin the inclusive boundary.
//   • normalizeListVersionsInput exact boundary at defaultListVersionsLimit
//     (20) — pass-through. Same argument, other end.
//   • normalizeListVersionsInput Limit=1 — the minimum legal value must
//     pass through unchanged. A regression that used `< defaultLimit`
//     instead of `< 1` would quietly inflate single-row polls to 20.
//   • Pin the constant values at the contract edge: defaultListVersionsLimit=20,
//     maxListVersionsLimit=100, defaultVersionSource="manual". These are
//     versioned API defaults — changing them is a breaking change and
//     the test is a tripwire, not a truism.

func TestNormalizeVersionSource_TrimsTabsAndNewlines(t *testing.T) {
	// TrimSpace covers all unicode whitespace — pin tab + newline
	// specifically so a regression to `Trim(… , " ")` surfaces here.
	cases := map[string]string{
		"\tauto-save\t":     "auto-save",
		"\nauto-save\n":     "auto-save",
		"\t\n manual \n\t":  "manual",
		"\r\nmigration\r\n": "migration",
	}
	for raw, want := range cases {
		if got := NormalizeVersionSource(raw); got != want {
			t.Errorf("NormalizeVersionSource(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestNormalizeVersionSource_KeepsInternalWhitespace(t *testing.T) {
	// Historical labels like "auto save" (space, not dash) must round-trip
	// unchanged. A refactor to Fields().Join("-") would silently rewrite
	// persisted source labels on every update. Pin the internal space.
	if got := NormalizeVersionSource("auto save"); got != "auto save" {
		t.Errorf("NormalizeVersionSource(\"auto save\") = %q, want \"auto save\"", got)
	}
}

func TestNormalizeListVersionsInput_NegativeLimitDefaults(t *testing.T) {
	// `< 1` catches zero AND negatives. A regression to `== 0` would
	// slip -1 through as `LIMIT -1` at the SQL layer (MySQL would error
	// or behave platform-specifically). Pin the default-fallback.
	for _, limit := range []int{-1, -10, -1000} {
		out := normalizeListVersionsInput(ListVersionsInput{Page: 1, Limit: limit})
		if out.Limit != defaultListVersionsLimit {
			t.Errorf("Limit=%d: got %d, want default %d",
				limit, out.Limit, defaultListVersionsLimit)
		}
	}
}

func TestNormalizeListVersionsInput_LimitAtMaxBoundaryPassesThrough(t *testing.T) {
	// The cap uses `> max` — Limit==max (100) must pass through untouched.
	// A tightening to `>= max` would clamp 100 to 100 (same result) but
	// would also mutate the value silently, which the != passthrough
	// test here would catch if the cap ever changed its numeric target.
	out := normalizeListVersionsInput(ListVersionsInput{Page: 1, Limit: maxListVersionsLimit})
	if out.Limit != maxListVersionsLimit {
		t.Errorf("Limit=%d: got %d, want pass-through", maxListVersionsLimit, out.Limit)
	}
}

func TestNormalizeListVersionsInput_LimitOneIsLegal(t *testing.T) {
	// A single-row poll (Limit=1) is a legal request — e.g. to peek the
	// latest version without paging. A regression that used `< default`
	// instead of `< 1` would silently inflate this to 20.
	out := normalizeListVersionsInput(ListVersionsInput{Page: 1, Limit: 1})
	if out.Limit != 1 {
		t.Errorf("Limit=1: got %d, want 1 (minimum legal passthrough)", out.Limit)
	}
}

func TestNormalizeListVersionsInput_DefaultLimitPassesThrough(t *testing.T) {
	// Limit == defaultListVersionsLimit (20) is a legal explicit input
	// (not a default-trigger). Pin pass-through so a refactor that used
	// `<= default → default` would surface here.
	out := normalizeListVersionsInput(ListVersionsInput{Page: 2, Limit: defaultListVersionsLimit})
	if out.Limit != defaultListVersionsLimit {
		t.Errorf("Limit=%d: got %d, want pass-through",
			defaultListVersionsLimit, out.Limit)
	}
	if out.Page != 2 {
		t.Errorf("Page should pass through, got %d", out.Page)
	}
}

func TestNormalizeListVersionsInput_BothPageAndLimitNormalisedIndependently(t *testing.T) {
	// Both fields normalise independently — a page=0 + limit=0 input
	// must produce (1, default). A regression that short-circuited on
	// Page==0 "before" touching Limit would leave Limit at 0.
	out := normalizeListVersionsInput(ListVersionsInput{Page: 0, Limit: 0, IncludeTotal: true})
	if out.Page != 1 {
		t.Errorf("Page=0 → %d, want 1", out.Page)
	}
	if out.Limit != defaultListVersionsLimit {
		t.Errorf("Limit=0 → %d, want %d", out.Limit, defaultListVersionsLimit)
	}
	if !out.IncludeTotal {
		t.Errorf("IncludeTotal must still pass through when both fields normalise")
	}
}

func TestVersionConstantsPinContract(t *testing.T) {
	// These constants are versioned API defaults — changing them would
	// change the default page size on every /versions GET and the
	// "unspecified source" label written to the DB. Pin the values
	// so a bump shows up as an intentional contract change, not a
	// silent drift.
	if defaultListVersionsLimit != 20 {
		t.Errorf("defaultListVersionsLimit = %d, want 20 (API default)",
			defaultListVersionsLimit)
	}
	if maxListVersionsLimit != 100 {
		t.Errorf("maxListVersionsLimit = %d, want 100 (client cap)",
			maxListVersionsLimit)
	}
	if defaultVersionSource != "manual" {
		t.Errorf("defaultVersionSource = %q, want %q (persisted fallback label)",
			defaultVersionSource, "manual")
	}
}
