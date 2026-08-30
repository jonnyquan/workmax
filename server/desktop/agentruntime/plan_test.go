//go:build desktop

package agentruntime

import (
	"encoding/json"
	"strings"
	"testing"
)

// The task list is the one tool input that reaches the renderer, so what it
// lets through is worth stating exactly.
func TestParsePlanReadsTheToolsOwnShape(t *testing.T) {
	items, ok := ParsePlan(map[string]any{"todos": []any{
		map[string]any{"content": "Read the spec", "status": "completed"},
		map[string]any{"content": "Write the parser", "status": "in_progress"},
		map[string]any{"content": "Run the suite", "status": "pending"},
	}})
	if !ok {
		t.Fatal("a todos array is a plan")
	}
	want := []PlanItem{
		{Text: "Read the spec", State: PlanDone},
		{Text: "Write the parser", State: PlanActive},
		{Text: "Run the suite", State: PlanPending},
	}
	if len(items) != len(want) {
		t.Fatalf("items = %d, want %d", len(items), len(want))
	}
	for i, item := range items {
		if item != want[i] {
			t.Fatalf("item %d = %+v, want %+v", i, item, want[i])
		}
	}
}

// A state nobody here understands is "not started", not a dropped row: a list
// that renders beats a list that refuses over one unexpected word.
func TestParsePlanFallsBackRatherThanRefusing(t *testing.T) {
	items, ok := ParsePlan(map[string]any{"todos": []any{
		map[string]any{"content": "Something", "status": "blocked_on_vendor"},
		map[string]any{"content": "Another", "status": ""},
	}})
	if !ok || len(items) != 2 {
		t.Fatalf("ok=%v items=%d, want true/2", ok, len(items))
	}
	for _, item := range items {
		if item.State != PlanPending {
			t.Fatalf("unknown state became %q, want %q", item.State, PlanPending)
		}
	}
}

// The boolean is the whole reason the caller can tell "the agent cleared its
// plan" from "that call was not a plan at all" — and it decides whether the
// call is swallowed or recorded as an ordinary step.
func TestParsePlanSeparatesEmptyFromNotAPlan(t *testing.T) {
	items, ok := ParsePlan(map[string]any{"todos": []any{}})
	if !ok {
		t.Fatal("an empty todos array is still a plan: the agent cleared it")
	}
	if len(items) != 0 {
		t.Fatalf("items = %d, want 0", len(items))
	}
	for _, input := range []map[string]any{
		{},
		{"todos": "not an array"},
		{"file_path": "/tmp/x", "content": "this is a Write"},
	} {
		if _, ok := ParsePlan(input); ok {
			t.Fatalf("input %v parsed as a plan", input)
		}
	}
}

// Bounded, because this renders as rows in a 300px column and the text is
// model-authored. Not a safety cap on the stream — the answer itself streams
// verbatim — but a panel that stays readable.
func TestParsePlanIsBounded(t *testing.T) {
	long := strings.Repeat("x", maxPlanItemText+50)
	todos := make([]any, 0, maxPlanItems+10)
	for i := 0; i < maxPlanItems+10; i++ {
		todos = append(todos, map[string]any{"content": long, "status": "pending"})
	}
	items, ok := ParsePlan(map[string]any{"todos": todos})
	if !ok {
		t.Fatal("still a plan")
	}
	if len(items) != maxPlanItems {
		t.Fatalf("items = %d, want the cap %d", len(items), maxPlanItems)
	}
	if len(items[0].Text) > maxPlanItemText {
		t.Fatalf("text = %d chars, want ≤ %d", len(items[0].Text), maxPlanItemText)
	}
}

// An entry with nothing to say is dropped rather than drawn as a blank row.
func TestParsePlanDropsEmptyLines(t *testing.T) {
	items, _ := ParsePlan(map[string]any{"todos": []any{
		map[string]any{"content": "   ", "status": "pending"},
		map[string]any{"status": "pending"},
		"not an object",
		map[string]any{"content": "Real one", "status": "pending"},
	}})
	if len(items) != 1 || items[0].Text != "Real one" {
		t.Fatalf("items = %+v, want the one line that says something", items)
	}
}

// The wire shape, pinned like every other frame: the renderer parses this.
func TestBridgeSendsTheWholePlan(t *testing.T) {
	dst := &memWriter{}
	bridge := NewSSEBridge(dst, nil)
	if err := bridge.Emit(Event{Kind: EventPlan, Plan: []PlanItem{
		{Text: "Read the spec", State: PlanDone},
		{Text: "Write the parser", State: PlanActive},
	}}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if len(dst.frames) != 1 || dst.frames[0].Type != "plan" {
		t.Fatalf("frames = %+v, want one plan frame", dst.frames)
	}
	var got struct {
		Items []struct {
			Text  string `json:"text"`
			State string `json:"state"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(dst.frames[0].Data), &got); err != nil {
		t.Fatalf("frame is not the shape the renderer parses: %v", err)
	}
	if len(got.Items) != 2 ||
		got.Items[0].Text != "Read the spec" || got.Items[0].State != "done" ||
		got.Items[1].State != "active" {
		t.Fatalf("items = %+v", got.Items)
	}
}

// An emptied plan still has to reach the renderer: "the agent cleared its
// list" is a fact about the run, and a panel that keeps showing the old list
// because the new one was empty is a panel that lies.
func TestBridgeSendsAnEmptiedPlan(t *testing.T) {
	dst := &memWriter{}
	bridge := NewSSEBridge(dst, nil)
	if err := bridge.Emit(Event{Kind: EventPlan, Plan: nil}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if len(dst.frames) != 1 {
		t.Fatalf("frames = %d, want the cleared list to be sent", len(dst.frames))
	}
	if !strings.Contains(dst.frames[0].Data, `"items":[]`) {
		t.Fatalf("data = %s, want an explicit empty list", dst.frames[0].Data)
	}
}
