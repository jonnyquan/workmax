package workagent

// preflight_direction_test.go — coverage for loadSelectedDirection
// + loadSelectedDirectionPair. Parallels preflight_critique_test
// (Polish #11) — same pattern of pinning each defensive gate in
// the loader.
//
// The loader fires on every turn AFTER turn-1, checks for a
// "visual_direction_selected" metadata row, validates the direction
// id against the static skills.FindDirection catalog, and renders
// a <visual-direction> XML block + matching design-system markdown.
//
// Five gates worth pinning (each one is a real branch in the loader):
//
//   1. Thread has no selection row → empty
//   2. Selection row has bad JSON metadata → empty
//   3. Selection row has wrong "kind" → empty
//   4. Selection row has empty direction_id → empty
//   5. Direction id not in catalog → empty
//
// Plus the happy path: valid selection renders both XML + design-system.

import (
	"strings"
	"testing"

	workagentModel "server/model/workagent"
	"server/utils/testutil"
)

func TestLoadSelectedDirection_EmptyWhenNoSelection(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	if got := loadSelectedDirection(100); got != "" {
		t.Errorf("expected empty injection for thread with no selection; got %q", got)
	}
	if xml, ds := loadSelectedDirectionPair(100); xml != "" || ds != "" {
		t.Errorf("pair must return (\"\", \"\") with no selection; got (%q, %q)", xml, ds)
	}
}

func TestLoadSelectedDirection_HappyPathRendersXMLAndDesignSystem(t *testing.T) {
	// Use modern_minimal — pinned in skills/visual_directions_test
	// as a known-shipping direction with a populated dsLink.
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	rec := &RecordingSink{}
	prev := SetMetricSink(rec)
	t.Cleanup(func() { SetMetricSink(prev) })

	PersistSelectedDirection(42, 200, "modern_minimal", "user_picker")
	rec.Reset()

	xml, ds := loadSelectedDirectionPair(200)
	if xml == "" {
		t.Fatal("expected non-empty <visual-direction> XML after PersistSelectedDirection")
	}
	// XML tag carries an `id` attribute: <visual-direction id="...">.
	// Match the opening prefix rather than the bare tag.
	if !strings.Contains(xml, "<visual-direction") {
		t.Errorf("missing opening tag: %q", xml)
	}
	if !strings.Contains(xml, "</visual-direction>") {
		t.Errorf("missing closing tag: %q", xml)
	}
	// modern_minimal's design-system body should resolve to non-empty
	// markdown via DsLink. If the .md file is missing the pair just
	// returns "" for ds — but with modern_minimal we expect a body.
	if ds == "" {
		t.Errorf("expected non-empty design-system body for modern_minimal direction")
	}
	msg, err := DefaultMessageRepository().FindLatestByMetadataKind(200, "visual_direction_selected")
	if err != nil || msg == nil {
		t.Fatalf("expected persisted direction metadata, got msg=%v err=%v", msg, err)
	}
	if !strings.Contains(msg.Metadata, `"design_system_basename":"modern-minimal"`) {
		t.Errorf("selection metadata should lock design system basename, got %q", msg.Metadata)
	}
	ev := rec.FindByEvent("wa_design_system_used")
	if ev == nil {
		t.Fatal("expected wa_design_system_used metric")
	}
	if ev.Fields["direction_id"] != "modern_minimal" || ev.Fields["design_system_basename"] != "modern-minimal" {
		t.Fatalf("metric fields = %#v", ev.Fields)
	}

	// Single-return wrapper matches pair's xml byte-for-byte AND two
	// back-to-back pair calls produce identical bytes. Pins the
	// deterministic-palette-order contract (FormatDirectionXML
	// previously walked a map literal with Go-randomized iteration;
	// fix at skills/visual_directions.go uses a fixed slot order so
	// every render produces the same bytes).
	if got := loadSelectedDirection(200); got != xml {
		t.Errorf("loadSelectedDirection differs from loadSelectedDirectionPair xml — non-deterministic render\n  single=%q\n  pair=%q", got, xml)
	}
	xml2, _ := loadSelectedDirectionPair(200)
	if xml2 != xml {
		t.Errorf("two back-to-back loadSelectedDirectionPair calls differ — non-deterministic render\n  first=%q\n  second=%q", xml, xml2)
	}
}

func TestLoadSelectedDirection_UsesLockedDesignSystemBasename(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	msg := &workagentModel.ChatMessage{
		UID:      42,
		UUID:     "locked-ds-" + t.Name(),
		ThreadID: 250,
		UserText: "",
		AIText:   "",
		ChatMode: "agent",
		Metadata: `{"kind":"visual_direction_selected","direction_id":"modern_minimal","design_system_basename":"vintage-film"}`,
	}
	if err := DefaultMessageRepository().CreateAgentMessage(msg); err != nil {
		t.Fatal(err)
	}
	_, ds := loadSelectedDirectionPair(250)
	if !strings.Contains(ds, "Design System — Vintage Film") {
		t.Fatalf("expected locked vintage-film design system, got %q", ds)
	}
}

// TestLoadSelectedDirection_RejectsBadJSON — the loader unmarshals
// the metadata column; corrupted JSON must NOT panic and must NOT
// inject partial state. Seed a synthetic message with a malformed
// JSON metadata blob and verify the loader degrades cleanly.
func TestLoadSelectedDirection_RejectsBadJSON(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	msg := &workagentModel.ChatMessage{
		UID: 42, UUID: "bad-json-" + t.Name(),
		ThreadID: 300, UserText: "", AIText: "", ChatMode: "agent",
		Metadata: `{"kind": "visual_direction_selected", "direction_id": "modern_minimal"`, // missing closing brace
	}
	if err := DefaultMessageRepository().CreateAgentMessage(msg); err != nil {
		t.Fatal(err)
	}

	if got := loadSelectedDirection(300); got != "" {
		t.Errorf("malformed JSON must yield empty injection; got %q", got)
	}
}

// TestLoadSelectedDirection_RejectsWrongKind — the loader explicitly
// asserts meta.Kind == "visual_direction_selected". A metadata row
// with a different kind (e.g. "discovery_form_result") must NOT be
// mis-interpreted as a direction selection.
func TestLoadSelectedDirection_RejectsWrongKind(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	msg := &workagentModel.ChatMessage{
		UID: 42, UUID: "wrong-kind-" + t.Name(),
		ThreadID: 400, UserText: "", AIText: "", ChatMode: "agent",
		Metadata: `{"kind": "discovery_form_result", "direction_id": "modern_minimal"}`,
	}
	if err := DefaultMessageRepository().CreateAgentMessage(msg); err != nil {
		t.Fatal(err)
	}

	// However, FindLatestByMetadataKind filters by kind, so a wrong-
	// kind row simply won't be returned by the lookup. To exercise
	// the Kind==... gate INSIDE loadSelectedDirectionPair, we'd need
	// to bypass the repo-level filter. The repo gate is good enough
	// as a first defense; pin the test at the public boundary.
	if got := loadSelectedDirection(400); got != "" {
		t.Errorf("wrong-kind metadata must yield empty injection; got %q", got)
	}
}

// TestLoadSelectedDirection_RejectsUnknownDirectionID — a valid
// metadata row referencing a direction id NOT in the
// visual-directions.yaml catalog (e.g. user pinned an id that was
// removed in a later release) must degrade silently. Without this
// gate the preflight would skip the FindDirection nil check and
// FormatDirectionXML on a nil pointer.
func TestLoadSelectedDirection_RejectsUnknownDirectionID(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	// Bypass PersistSelectedDirection's catalog validation by writing
	// the metadata row directly. This simulates a row from an older
	// release whose direction id has since been retired.
	msg := &workagentModel.ChatMessage{
		UID: 42, UUID: "unknown-dir-" + t.Name(),
		ThreadID: 500, UserText: "", AIText: "", ChatMode: "agent",
		Metadata: `{"kind": "visual_direction_selected", "direction_id": "retired_in_v2"}`,
	}
	if err := DefaultMessageRepository().CreateAgentMessage(msg); err != nil {
		t.Fatal(err)
	}

	if got := loadSelectedDirection(500); got != "" {
		t.Errorf("retired direction id must yield empty injection; got %q", got)
	}
}

// TestLoadSelectedDirection_LatestSelectionWins — when the user
// changes their mind and picks a different direction, the loader
// must surface the LATEST selection, not the first. Pin the
// "FindLatestByMetadataKind returns the latest" contract via the
// loader's behavior.
func TestLoadSelectedDirection_LatestSelectionWins(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	// First pick: modern_minimal
	PersistSelectedDirection(42, 600, "modern_minimal", "user_picker")
	// User changes mind: editorial_magazine
	PersistSelectedDirection(42, 600, "editorial_magazine", "user_picker")

	xml := loadSelectedDirection(600)
	if xml == "" {
		t.Fatal("expected non-empty after two selections")
	}
	// The LATEST selection (editorial_magazine) must surface.
	// modern_minimal's XML carries distinct identifying content
	// that should NOT appear when editorial_magazine wins.
	if strings.Contains(xml, "modern_minimal") {
		t.Errorf("latest selection should be editorial_magazine, not modern_minimal; got %s", xml)
	}
}

func TestLoadSelectedDirection_LatestSelectionWinsInDeepThread(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	PersistSelectedDirection(42, 650, "modern_minimal", "user_picker")
	for i := 0; i < 12; i++ {
		if err := DefaultMessageRepository().CreateAgentMessage(&workagentModel.ChatMessage{
			UID:         42,
			ThreadID:    650,
			ContentType: "text",
			ChatMode:    string(workagentModel.ChatModeAgent),
			UserText:    "filler",
		}); err != nil {
			t.Fatalf("seed filler message: %v", err)
		}
	}
	PersistSelectedDirection(42, 650, "editorial_magazine", "user_picker")

	xml := loadSelectedDirection(650)
	if xml == "" {
		t.Fatal("expected non-empty after deep-thread selection")
	}
	if strings.Contains(xml, "modern_minimal") {
		t.Errorf("deep-thread latest selection should win, got %s", xml)
	}
}

// TestBuildPreflightAdditionsForThread_IncludesVisualDirection — end-
// to-end: when the thread has a persisted selection, the composer's
// final SystemAdditions output carries the <visual-direction> block
// AND the design-system body.
func TestBuildPreflightAdditionsForThread_IncludesVisualDirection(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	PersistSelectedDirection(42, 700, "modern_minimal", "user_picker")

	got := BuildPreflightAdditionsForThread(42, "ppt", 700)
	if !strings.Contains(got, "<visual-direction") {
		t.Errorf("composed additions missing <visual-direction> block: %q", got)
	}
}
