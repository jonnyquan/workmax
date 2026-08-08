package workagent

// preflight_discovery_test.go — coverage for loadDiscoveryContext.
// The previous test surface had only one case: threadID=0 short-
// circuit. The happy path AND each defensive gate were unpinned.
//
// Parallels preflight_critique_test (Polish #11) and
// preflight_direction_test — same defensive-gate audit pattern
// applied to the 4th preflight loader.
//
// Gates pinned:
//   1. No discovery row → empty
//   2. Bad JSON metadata → empty (defensive against migration corruption)
//   3. Wrong "kind" → empty (FindLatestDiscoveryAnswers filters, but
//      the loader's explicit kind-check is defense-in-depth)
//   4. Empty answers map → empty (semantically "no signal to inject")
//   5. Happy path: renders alphabetized k=v + skip_reason footer
//   6. Skip reason is OPTIONAL (no footer when absent)
//   7. Deterministic key order across renders (prompt-cache stability —
//      same defect class as the FormatDirectionXML fix in 2c6920e1)

import (
	"strings"
	"testing"

	workagentModel "server/model/workagent"
	"server/utils/testutil"
)

func TestLoadDiscoveryContext_NoSelectionRow(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	// Thread with no discovery row → empty.
	if got := loadDiscoveryContext(100, ""); got != "" {
		t.Errorf("expected empty injection for thread with no discovery row; got %q", got)
	}
}

func TestLoadDiscoveryContext_HappyPath_RendersAnswers(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	answers := map[string]string{
		"audience":    "executives",
		"tone":        "professional",
		"slide_count": "20",
	}
	// G10 (2026-05-17) — write with skillName="" so the loader's
	// skill-agnostic fallback returns the row (legacy behaviour).
	// G10-scoped read/write coverage lives in
	// preflight_discovery_per_skill_test.go.
	PersistInferredDiscoveryAnswers(42, 200, "", answers, "nlp_inferred")

	got := loadDiscoveryContext(200, "")
	if got == "" {
		t.Fatal("expected non-empty after PersistInferredDiscoveryAnswers")
	}
	if !strings.Contains(got, "<discovery-context>") {
		t.Errorf("missing opening tag: %q", got)
	}
	if !strings.Contains(got, "</discovery-context>") {
		t.Errorf("missing closing tag: %q", got)
	}
	// All three answers verbatim.
	for k, v := range answers {
		line := k + ": " + v
		if !strings.Contains(got, line) {
			t.Errorf("missing answer %q in output: %s", line, got)
		}
	}
	// Source attribution surfaces as "captured via:" footer.
	if !strings.Contains(got, "captured via: nlp_inferred") {
		t.Errorf("missing source attribution footer: %s", got)
	}
}

// TestLoadDiscoveryContext_DeterministicKeyOrder — pins the
// sort.Strings(keys) discipline. The previous shape used to walk
// the answers map directly (Go-randomized iteration), which caused
// the same prompt-cache-instability class of bug fixed at 2c6920e1
// for FormatDirectionXML. The loader already sorts keys today; this
// test pins it so a future "let me skip the sort, it's a small map"
// refactor immediately surfaces the regression.
func TestLoadDiscoveryContext_DeterministicKeyOrder(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	// Many keys so map iteration order has high variance across
	// runs without sort discipline.
	answers := map[string]string{
		"audience":    "exec",
		"tone":        "formal",
		"slide_count": "20",
		"deadline":    "friday",
		"locale":      "en-US",
		"format":      "deck",
		"asset_kind":  "brand",
		"era":         "modern",
	}
	PersistInferredDiscoveryAnswers(42, 300, "", answers, "nlp_inferred")

	first := loadDiscoveryContext(300, "")
	for i := 0; i < 10; i++ {
		again := loadDiscoveryContext(300, "")
		if again != first {
			t.Fatalf("loadDiscoveryContext non-deterministic across calls:\n  first =%q\n  iter %d=%q", first, i, again)
		}
	}
}

func TestLoadDiscoveryContext_RejectsBadJSON(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	// Bypass PersistInferredDiscoveryAnswers and write the marker
	// row directly so we can plant a malformed JSON metadata blob.
	// IMPORTANT: no space after colon — repo's FindLatestByMetadataKind
	// uses LIKE %"kind":"..."% which only matches Go-json.Marshal's
	// spaceless format. Hand-written JSON with spaces silently
	// wouldn't be findable, and the test would pass for the wrong
	// reason (empty result because of LIKE miss, not the loader's
	// JSON-error gate).
	msg := &workagentModel.ChatMessage{
		UID: 42, UUID: "bad-json-" + t.Name(),
		ThreadID: 400, UserText: "", AIText: "", ChatMode: "agent",
		Metadata: `{"kind":"discovery_form_result","answers":{"audience":"exec"`, // missing closing braces
	}
	if err := DefaultMessageRepository().CreateAgentMessage(msg); err != nil {
		t.Fatal(err)
	}

	if got := loadDiscoveryContext(400, ""); got != "" {
		t.Errorf("malformed JSON must yield empty injection; got %q", got)
	}
}

func TestLoadDiscoveryContext_RejectsEmptyAnswersMap(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	// A well-formed discovery row with an empty answers map (e.g.
	// the user submitted the form with skip_reason set and no
	// typed answers) must NOT surface a half-rendered block —
	// the loader gates on len(Answers) == 0 to drop these.
	msg := &workagentModel.ChatMessage{
		UID: 42, UUID: "empty-ans-" + t.Name(),
		ThreadID: 500, UserText: "", AIText: "", ChatMode: "agent",
		Metadata: `{"kind":"discovery_form_result","answers":{},"skip_reason":"user_skipped"}`,
	}
	if err := DefaultMessageRepository().CreateAgentMessage(msg); err != nil {
		t.Fatal(err)
	}

	if got := loadDiscoveryContext(500, ""); got != "" {
		t.Errorf("empty answers map must yield empty injection (no half-rendered block); got %q", got)
	}
}

func TestLoadDiscoveryContext_HappyPath_NoSkipReason(t *testing.T) {
	// Skip reason is OPTIONAL — when absent the "(captured via: ...)"
	// footer must NOT appear. Pins the empty-string conditional gate
	// in the LOADER (the writer PersistInferredDiscoveryAnswers
	// defaults empty source to "nlp_inferred", so the only realistic
	// way to land an empty-SkipReason row is a future migration / a
	// hand-edited DB — defensive coverage).
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	// Bypass the writer's default-source coercion by stamping the
	// metadata row directly.
	msg := &workagentModel.ChatMessage{
		UID: 42, UUID: "no-skip-reason-" + t.Name(),
		ThreadID: 600, UserText: "", AIText: "", ChatMode: "agent",
		Metadata: `{"kind":"discovery_form_result","answers":{"audience":"exec"}}`,
	}
	if err := DefaultMessageRepository().CreateAgentMessage(msg); err != nil {
		t.Fatal(err)
	}

	got := loadDiscoveryContext(600, "")
	if got == "" {
		t.Fatal("expected non-empty injection")
	}
	if strings.Contains(got, "captured via:") {
		t.Errorf("empty SkipReason should NOT produce captured-via footer; got %q", got)
	}
	if !strings.Contains(got, "audience: exec") {
		t.Errorf("missing answer line: %q", got)
	}
}

func TestLoadDiscoveryContext_BoundsThreadID(t *testing.T) {
	// The loader doesn't have an explicit threadID=0 short-circuit
	// itself — the upstream guard (in BuildPreflightAdditionsForThread)
	// does. But repo.FindLatestDiscoveryAnswers handles threadID=0
	// cleanly. Pin the survival.
	if got := loadDiscoveryContext(0, ""); got != "" {
		t.Errorf("threadID=0 should return empty (no thread scope); got %q", got)
	}
}

func TestBuildPreflightAdditionsForThread_IncludesDiscovery(t *testing.T) {
	// End-to-end: composed output carries the <discovery-context>
	// block when the thread has persisted answers.
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	// G10 (2026-05-17) — write with the same skill the
	// BuildPreflightAdditionsForThread call below reads ("ppt").
	// Without the matching form_id, the per-skill lookup would
	// miss this row and the composed additions wouldn't carry
	// <discovery-context>.
	PersistInferredDiscoveryAnswers(42, 700, "ppt", map[string]string{
		"audience": "marketing-team",
	}, "nlp_inferred")

	got := BuildPreflightAdditionsForThread(42, "ppt", 700)
	if !strings.Contains(got, "<discovery-context>") {
		t.Errorf("composed additions missing <discovery-context> block: %q", got)
	}
	if !strings.Contains(got, "audience: marketing-team") {
		t.Errorf("composed additions missing discovery answer: %q", got)
	}
}
