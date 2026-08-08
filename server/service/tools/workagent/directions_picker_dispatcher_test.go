package workagent

import (
	"encoding/json"
	"strings"
	"testing"

	"server/config"
	"server/service/tools/workagent/i18n"
	"server/service/tools/workagent/skills"
)

func makeDirectionsDispatcher(t *testing.T) *DirectionsPickerDispatcher {
	t.Helper()
	r, err := i18n.Load()
	if err != nil {
		t.Fatalf("load i18n: %v", err)
	}
	return NewDirectionsPickerDispatcher(r)
}

func setDirectionsFallbackEnabled(t *testing.T) {
	t.Helper()
	// Snapshot the CURRENT features struct (not the package-level
	// GetWorkAgentFeatures function), so cleanup restores by value.
	// Capturing the function value led to infinite recursion: cleanup
	// would set the accessor to `func() { return prev() }` where prev
	// resolves back through GetWorkAgentFeatures → workAgentFeaturesAccessor →
	// the same lambda → ... blowing the stack on the next read.
	prev := config.GetWorkAgentFeatures()
	config.SetWorkAgentFeaturesAccessor(func() *config.WorkAgentFeatures {
		return &config.WorkAgentFeatures{
			DirectionsFallbackEnabled: true,
		}
	})
	t.Cleanup(func() {
		config.SetWorkAgentFeaturesAccessor(func() *config.WorkAgentFeatures {
			return prev
		})
	})
}

func bundleWithDirections() *skills.SkillBundle {
	return &skills.SkillBundle{
		Name:    "ppt",
		Version: "test",
		DirectionsFallback: &skills.DirectionsFallback{
			Enabled: true,
			SkipKey: "form.skip",
		},
	}
}

func TestDirectionsDispatcher_AlwaysOnEmitsPicker(t *testing.T) {
	d := makeDirectionsDispatcher(t)
	send, events := newCaptureSendEvent()
	res := d.Dispatch(DirectionsDispatchInput{
		UID:         42,
		SkillBundle: bundleWithDirections(),
		SendEvent:   send,
	})
	if !res.Emitted {
		t.Errorf("expected directions picker emission in always-on mode")
	}
	if len(*events) == 0 {
		t.Error("expected SSE events")
	}
}

func TestDirectionsDispatcher_NoSkillFallback(t *testing.T) {
	setDirectionsFallbackEnabled(t)
	d := makeDirectionsDispatcher(t)
	send, _ := newCaptureSendEvent()
	bundle := &skills.SkillBundle{Name: "ppt"} // no DirectionsFallback
	res := d.Dispatch(DirectionsDispatchInput{
		UID:         42,
		SkillBundle: bundle,
		SendEvent:   send,
	})
	if res.Emitted {
		t.Errorf("expected pass-through when skill has no directions_fallback")
	}
}

func TestDirectionsDispatcher_FallbackDisabled(t *testing.T) {
	setDirectionsFallbackEnabled(t)
	d := makeDirectionsDispatcher(t)
	send, _ := newCaptureSendEvent()
	bundle := &skills.SkillBundle{
		Name:               "ppt",
		DirectionsFallback: &skills.DirectionsFallback{Enabled: false},
	}
	res := d.Dispatch(DirectionsDispatchInput{UID: 42, SkillBundle: bundle, SendEvent: send})
	if res.Emitted {
		t.Errorf("expected pass-through when fallback.enabled=false")
	}
}

func TestDirectionsDispatcher_BrandContextSkipsPicker(t *testing.T) {
	setDirectionsFallbackEnabled(t)
	d := makeDirectionsDispatcher(t)
	send, events := newCaptureSendEvent()
	res := d.Dispatch(DirectionsDispatchInput{
		UID:             42,
		SkillBundle:     bundleWithDirections(),
		HasBrandContext: true,
		SendEvent:       send,
	})
	if res.Emitted {
		t.Errorf("expected skip when brand context active, got Emitted=true")
	}
	if !strings.Contains(res.Reason, "brand context active") {
		t.Errorf("reason should mention brand context, got %q", res.Reason)
	}
	if len(*events) != 0 {
		t.Errorf("expected no events, got %d", len(*events))
	}
}

func TestDirectionsDispatcher_HappyPathEmitsPickerWithFiveDirections(t *testing.T) {
	setDirectionsFallbackEnabled(t)
	d := makeDirectionsDispatcher(t)
	send, events := newCaptureSendEvent()

	res := d.Dispatch(DirectionsDispatchInput{
		UID:           42,
		SkillBundle:   bundleWithDirections(),
		Locale:        "en",
		TempMessageID: "msg-test-1",
		SendEvent:     send,
	})
	if !res.Emitted {
		t.Fatalf("expected Emitted=true, got false (reason=%s)", res.Reason)
	}
	if len(*events) != 2 {
		t.Fatalf("expected 2 events (block + done), got %d", len(*events))
	}

	block, done := (*events)[0], (*events)[1]
	if block.Type != "block" {
		t.Errorf("first event should be block, got %s", block.Type)
	}
	if done.Type != "done" {
		t.Errorf("second event should be done, got %s", done.Type)
	}

	// Verify the block carries the 5 directions from yaml.
	var blockPayload map[string]interface{}
	if err := json.Unmarshal(block.Block, &blockPayload); err != nil {
		t.Fatalf("unmarshal block: %v", err)
	}
	if blockPayload["type"] != "directions_picker" {
		t.Errorf("expected type=directions_picker, got %v", blockPayload["type"])
	}
	if blockPayload["picker_id"] != "fallback_5" {
		t.Errorf("expected picker_id=fallback_5, got %v", blockPayload["picker_id"])
	}
	schema := blockPayload["schema"].(map[string]interface{})
	directions := schema["directions"].([]interface{})
	if len(directions) != 5 {
		t.Errorf("expected 5 directions in fallback_5, got %d", len(directions))
	}

	// Verify each direction carries hex palette + id (load-bearing
	// for the frontend renderer).
	for i, d := range directions {
		dir := d.(map[string]interface{})
		if dir["id"] == "" || dir["id"] == nil {
			t.Errorf("direction[%d] missing id", i)
		}
		palette := dir["palette"].(map[string]interface{})
		for _, slot := range []string{"bg", "fg", "accent", "muted"} {
			hex := palette[slot]
			if hex == nil || hex == "" {
				t.Errorf("direction[%d] palette.%s empty", i, slot)
			}
		}
	}

	// Verify done event has the right stop_reason.
	var resultPayload map[string]interface{}
	_ = json.Unmarshal(done.Result, &resultPayload)
	if resultPayload["stop_reason"] != "directions_emitted" {
		t.Errorf("expected stop_reason=directions_emitted, got %v", resultPayload["stop_reason"])
	}
}

func TestDirectionsDispatcher_LocaleResolvedLabels(t *testing.T) {
	setDirectionsFallbackEnabled(t)
	d := makeDirectionsDispatcher(t)
	send, events := newCaptureSendEvent()

	d.Dispatch(DirectionsDispatchInput{
		UID:           42,
		SkillBundle:   bundleWithDirections(),
		Locale:        "zh",
		TempMessageID: "msg-1",
		SendEvent:     send,
	})

	if len(*events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(*events))
	}
	var blockPayload map[string]interface{}
	_ = json.Unmarshal((*events)[0].Block, &blockPayload)
	directions := blockPayload["schema"].(map[string]interface{})["directions"].([]interface{})
	if len(directions) == 0 {
		t.Fatal("no directions emitted")
	}
	// Each direction should carry SOME label — either resolved
	// from i18n or fallback to the id (in case the locale catalog
	// hasn't been seeded for these direction keys yet). Just verify
	// nothing is empty.
	for _, d := range directions {
		dir := d.(map[string]interface{})
		if dir["label"] == "" || dir["label"] == nil {
			t.Errorf("direction missing label: %v", dir)
		}
	}
}

// TestDirectionsDispatcher_EmitsSkipButtonNotInTitle pins the wire-
// shape fix from 2026-05-12. Previously the skip-CTA label was
// hijacked into schema.title (because the schema lacked a
// skip_button field), and the FE renderer displayed "Skip" as the
// picker heading. After the fix, skip_button.label carries the
// label and title is left unset.
func TestDirectionsDispatcher_EmitsSkipButtonNotInTitle(t *testing.T) {
	setDirectionsFallbackEnabled(t)
	d := makeDirectionsDispatcher(t)
	send, events := newCaptureSendEvent()

	d.Dispatch(DirectionsDispatchInput{
		UID:           42,
		SkillBundle:   bundleWithDirections(),
		Locale:        "en",
		TempMessageID: "msg-skip-button",
		SendEvent:     send,
	})

	if len(*events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(*events))
	}
	var blockPayload map[string]interface{}
	if err := json.Unmarshal((*events)[0].Block, &blockPayload); err != nil {
		t.Fatalf("unmarshal block: %v", err)
	}
	schema := blockPayload["schema"].(map[string]interface{})

	// title must NOT carry the skip label (the old wrong path).
	if title, ok := schema["title"]; ok && title != nil && title != "" {
		t.Errorf("schema.title should be unset, got %v (regression: skip label hijacked into title slot)", title)
	}

	// skip_button.label must carry the resolved Skip CTA.
	skipButton, ok := schema["skip_button"].(map[string]interface{})
	if !ok {
		t.Fatalf("schema.skip_button missing or wrong type: %v", schema["skip_button"])
	}
	if label, _ := skipButton["label"].(string); label == "" {
		t.Errorf("schema.skip_button.label empty: %v", skipButton)
	}
}
