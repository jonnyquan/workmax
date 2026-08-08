// decision_log_test.go — pin the pure projection layer of the
// decision-log feature. The DB-backed end-to-end lives in
// decision_log_db_test.go; this file isolates the
// `projectJoinedRowsToDecisionLog` projection so a regression in
// status mapping, asset-binding summary, or totals arithmetic
// surfaces as a unit-test failure, not as a flaky end-to-end run.

package canvas

import (
	"testing"
	"time"

	"server/model"
)

func TestProjectJoinedRowsToDecisionLog_EmptyInputProducesZeroedTotals(t *testing.T) {
	// Empty project → empty entries + zero totals (NOT nil entries).
	// Wire shape contract: callers can iterate `entries` without a
	// nil-guard; absent rollup fields would force every consumer to
	// branch on missing-vs-zero.
	entries, totals := projectJoinedRowsToDecisionLog(nil)
	if len(entries) != 0 {
		t.Errorf("empty input should produce 0 entries, got %d", len(entries))
	}
	if totals.Tasks != 0 || totals.Credits != 0 || totals.Successful != 0 {
		t.Errorf("empty input should zero totals, got %+v", totals)
	}
}

func TestProjectJoinedRowsToDecisionLog_StatusBucketing(t *testing.T) {
	// All five TaskStatus values must route into the right bucket.
	// The four terminal statuses (completed/failed/cancelled) +
	// the two in-flight (pending/processing) — totals.Pending
	// collapses the latter two because the UI surfaces them
	// together as "in progress".
	now := time.Now()
	rows := []joinedRow{
		{TaskID: "t-ok", Status: model.TaskStatusCompleted, CreditsUsed: 3, CreatedAt: now},
		{TaskID: "t-fail", Status: model.TaskStatusFailed, CreditsUsed: 0, CreatedAt: now},
		{TaskID: "t-cancel", Status: model.TaskStatusCancelled, CreditsUsed: 0, CreatedAt: now},
		{TaskID: "t-wait", Status: model.TaskStatusPending, CreatedAt: now},
		{TaskID: "t-run", Status: model.TaskStatusProcessing, CreatedAt: now},
	}
	_, totals := projectJoinedRowsToDecisionLog(rows)
	if totals.Tasks != 5 {
		t.Errorf("Tasks = %d, want 5", totals.Tasks)
	}
	if totals.Successful != 1 || totals.Failed != 1 || totals.Cancelled != 1 || totals.Pending != 2 {
		t.Errorf("status totals wrong: %+v", totals)
	}
	if totals.Credits != 3 {
		t.Errorf("Credits should sum to 3 (only the completed task charged), got %d", totals.Credits)
	}
}

func TestProjectJoinedRowsToDecisionLog_SortsByCreatedAtDesc(t *testing.T) {
	// SQL ORDER BY ought to already sort, but the projection also
	// applies a stable secondary sort so tests don't flake on rows
	// with equal seconds. Pin both layers — most recent first.
	older := time.Now().Add(-2 * time.Hour)
	newer := time.Now()
	rows := []joinedRow{
		{TaskID: "older", Status: model.TaskStatusCompleted, CreatedAt: older},
		{TaskID: "newer", Status: model.TaskStatusCompleted, CreatedAt: newer},
	}
	entries, _ := projectJoinedRowsToDecisionLog(rows)
	if entries[0].TaskID != "newer" {
		t.Errorf("most-recent task should be first, got order %q,%q", entries[0].TaskID, entries[1].TaskID)
	}
}

func TestProjectJoinedRowsToDecisionLog_DecodesRequestData(t *testing.T) {
	// Verify the JSONMap → entry decode pulls the human-readable
	// fields out of RequestData. These drive the UI's per-row
	// "what was this generation about" rendering — the most fragile
	// part of the projection because RequestData is loosely typed.
	now := time.Now()
	rows := []joinedRow{
		{
			TaskID:      "decoded",
			Status:      model.TaskStatusCompleted,
			CreditsUsed: 5,
			CreatedAt:   now,
			RequestData: model.JSONMap{
				"mediaType":      "video",
				"prompt":         "  saturated neon street  ",
				"negativePrompt": "blur, low quality",
				"aspectRatio":    "9:16",
				"resolution":     "1080p",
				"duration":       "5",
				"stylePreset":    "cinematic",
				"origin":         "canvas",
				"numberOfImages": float64(3),
				"referenceImages": []interface{}{
					map[string]interface{}{"url": "a"},
					map[string]interface{}{"url": "b"},
				},
				"assetBindings": map[string]interface{}{
					"characterIds": []interface{}{float64(1), float64(2)},
					"brandIds":     []interface{}{float64(7)},
				},
			},
		},
	}
	entries, _ := projectJoinedRowsToDecisionLog(rows)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.MediaType != "video" {
		t.Errorf("MediaType = %q, want 'video'", e.MediaType)
	}
	if e.Prompt != "saturated neon street" {
		t.Errorf("Prompt = %q, want trimmed", e.Prompt)
	}
	if e.AspectRatio != "9:16" {
		t.Errorf("AspectRatio = %q, want '9:16'", e.AspectRatio)
	}
	if e.NumberOfImages != 3 {
		t.Errorf("NumberOfImages = %d, want 3", e.NumberOfImages)
	}
	if e.ReferenceImageCount != 2 {
		t.Errorf("ReferenceImageCount = %d, want 2 (sum of referenceImages array)", e.ReferenceImageCount)
	}
	if e.AssetBindingSummary != "2 characters, 1 brand" {
		t.Errorf("AssetBindingSummary = %q, want '2 characters, 1 brand'", e.AssetBindingSummary)
	}
}

func TestProjectJoinedRowsToDecisionLog_DecodesAcceptedConsistencyVerdict(t *testing.T) {
	// #9 follow-up (2026-05-15): when the FE submits a video with
	// an accepted (Proceed-Anyway) consistency verdict, the verdict
	// rides on request_data.consistencyVerdict and the decision-log
	// projection MUST surface it so postmortem ("did we warn the
	// user about this shot?") can read it back.
	rows := []joinedRow{
		{
			TaskID:    "with-verdict",
			Status:    model.TaskStatusCompleted,
			CreatedAt: time.Now(),
			RequestData: model.JSONMap{
				"prompt": "snowy mountain",
				"consistencyVerdict": map[string]interface{}{
					"summary":  "Setting shift",
					"model":    "deepseek-chat",
					"accepted": true,
					"warnings": []interface{}{
						map[string]interface{}{"kind": "setting", "detail": "Prior shots are office interior."},
						map[string]interface{}{"kind": "character", "detail": "  "}, // empty detail → dropped
						map[string]interface{}{"detail": "Tone shift to bleak."},    // missing kind → defaults to "general"
					},
				},
			},
		},
	}
	entries, _ := projectJoinedRowsToDecisionLog(rows)
	v := entries[0].ConsistencyVerdict
	if v == nil {
		t.Fatalf("expected ConsistencyVerdict to be decoded, got nil")
	}
	if v.Summary != "Setting shift" {
		t.Errorf("Summary = %q, want 'Setting shift'", v.Summary)
	}
	if v.Model != "deepseek-chat" {
		t.Errorf("Model = %q, want 'deepseek-chat'", v.Model)
	}
	if !v.Accepted {
		t.Errorf("Accepted should be true when the FE sent accepted=true")
	}
	if len(v.Warnings) != 2 {
		t.Fatalf("Warnings length = %d, want 2 (one empty-detail row dropped)", len(v.Warnings))
	}
	if v.Warnings[0].Kind != "setting" || v.Warnings[0].Detail != "Prior shots are office interior." {
		t.Errorf("warning[0] = %+v, want kind=setting + the office-interior detail", v.Warnings[0])
	}
	if v.Warnings[1].Kind != "general" {
		t.Errorf("warning[1].Kind = %q, want 'general' (default when kind absent)", v.Warnings[1].Kind)
	}
}

func TestProjectJoinedRowsToDecisionLog_MissingVerdictStaysNil(t *testing.T) {
	// Most rows won't have a verdict (the FE only stamps it when
	// the user accepted warnings). Missing slot → nil; the
	// `omitempty` JSON tag then drops the field entirely on the
	// wire. A row that emitted an empty object would still parse
	// but would mislead a UI that branches on presence.
	rows := []joinedRow{
		{TaskID: "no-verdict", Status: model.TaskStatusCompleted, CreatedAt: time.Now(), RequestData: model.JSONMap{
			"prompt": "no warnings here",
		}},
	}
	entries, _ := projectJoinedRowsToDecisionLog(rows)
	if entries[0].ConsistencyVerdict != nil {
		t.Errorf("ConsistencyVerdict should be nil when request_data omits the slot, got %+v", entries[0].ConsistencyVerdict)
	}
}

func TestProjectJoinedRowsToDecisionLog_VerdictWithoutWarningsIsDropped(t *testing.T) {
	// A green-light verdict (ok=true, no warnings) carries no
	// audit signal; persisting it would bloat every row in the
	// table. The decoder collapses such verdicts to nil so the
	// audit view stays focused on rows where the user actually
	// saw a warning.
	rows := []joinedRow{
		{TaskID: "green-light", Status: model.TaskStatusCompleted, CreatedAt: time.Now(), RequestData: model.JSONMap{
			"prompt": "fine prompt",
			"consistencyVerdict": map[string]interface{}{
				"summary":  "",
				"model":    "deepseek-chat",
				"accepted": true,
				"warnings": []interface{}{},
			},
		}},
	}
	entries, _ := projectJoinedRowsToDecisionLog(rows)
	if entries[0].ConsistencyVerdict != nil {
		t.Errorf("zero-warning verdict should decode to nil, got %+v", entries[0].ConsistencyVerdict)
	}
}

func TestProjectJoinedRowsToDecisionLog_MalformedVerdictStaysNil(t *testing.T) {
	// Wire glitch: consistencyVerdict comes through as a string or
	// array instead of an object. The decoder must NOT panic and
	// must NOT surface a half-decoded record — fall through to nil
	// (the row is still useful for everything else).
	rows := []joinedRow{
		{TaskID: "malformed-1", Status: model.TaskStatusCompleted, CreatedAt: time.Now(), RequestData: model.JSONMap{
			"prompt":             "x",
			"consistencyVerdict": "not-an-object",
		}},
		{TaskID: "malformed-2", Status: model.TaskStatusCompleted, CreatedAt: time.Now(), RequestData: model.JSONMap{
			"prompt":             "y",
			"consistencyVerdict": []interface{}{"also wrong"},
		}},
	}
	entries, _ := projectJoinedRowsToDecisionLog(rows)
	for _, e := range entries {
		if e.ConsistencyVerdict != nil {
			t.Errorf("malformed verdict row %s should yield nil, got %+v", e.TaskID, e.ConsistencyVerdict)
		}
	}
}

func TestProjectJoinedRowsToDecisionLog_NoAssetBindingsSummaryIsEmpty(t *testing.T) {
	// Empty assetBindings → empty summary → omitempty drops the
	// field from JSON. A regression that emitted "" would still
	// look right in the body but a future regression that emitted
	// "0 characters" would leak. Pin "".
	rows := []joinedRow{
		{TaskID: "no-bindings", Status: model.TaskStatusCompleted, CreatedAt: time.Now(), RequestData: model.JSONMap{
			"prompt":        "just text",
			"assetBindings": map[string]interface{}{},
		}},
	}
	entries, _ := projectJoinedRowsToDecisionLog(rows)
	if entries[0].AssetBindingSummary != "" {
		t.Errorf("AssetBindingSummary = %q, want empty (omitempty)", entries[0].AssetBindingSummary)
	}
}

func TestPluralize(t *testing.T) {
	// Tiny helper but it shows up in every assetBindingSummary —
	// a regression here would corrupt every project's audit view.
	cases := []struct {
		n    int
		want string
	}{
		{1, "1 character"},
		{2, "2 characters"},
		{0, "0 characters"}, // weird but predictable
	}
	for _, c := range cases {
		got := pluralize(c.n, "character", "characters")
		if got != c.want {
			t.Errorf("pluralize(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}
