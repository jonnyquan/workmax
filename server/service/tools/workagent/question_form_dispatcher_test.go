package workagent

import (
	"encoding/json"
	"testing"

	"server/config"
	"server/service/tools/workagent/i18n"
	"server/service/tools/workagent/skills"
)

// makeTestDispatcher builds a dispatcher backed by the real i18n
// catalog. Tests that need a frozen "open-to-all" feature flag
// also call setOpenToAllFeatures().
func makeTestDispatcher(t *testing.T) *QuestionFormDispatcher {
	t.Helper()
	r, err := i18n.Load()
	if err != nil {
		t.Fatalf("load i18n: %v", err)
	}
	return NewQuestionFormDispatcher(r)
}

// setOpenToAllFeatures temporarily replaces the WorkAgentFeatures
// accessor with one that returns "question_form enabled, no
// whitelist" — i.e. open to all uids ≥ 1. Restores the original
// accessor on test cleanup.
func setOpenToAllFeatures(t *testing.T) {
	t.Helper()
	// Snapshot the struct, not the function — see the matching helper
	// in directions_picker_dispatcher_test.go for the recursion failure
	// mode the previous shape produced.
	prev := config.GetWorkAgentFeatures()
	config.SetWorkAgentFeaturesAccessor(func() *config.WorkAgentFeatures {
		return &config.WorkAgentFeatures{
			QuestionFormEnabled: true,
		}
	})
	t.Cleanup(func() {
		config.SetWorkAgentFeaturesAccessor(func() *config.WorkAgentFeatures {
			return prev
		})
	})
}

// captureSendEvent records every SSE write the dispatcher makes.
// Returns a SendEventFunc compatible with DispatchInput plus a slice
// the test can inspect.
type capturedEvent struct {
	Type       string
	MessageID  string
	BlockIndex int
	Block      json.RawMessage
	Result     json.RawMessage
}

func newCaptureSendEvent() (SendEventFunc, *[]capturedEvent) {
	events := []capturedEvent{}
	fn := func(eventType, messageID string, blockIndex int, block, result json.RawMessage) {
		events = append(events, capturedEvent{
			Type:       eventType,
			MessageID:  messageID,
			BlockIndex: blockIndex,
			Block:      block,
			Result:     result,
		})
	}
	return fn, &events
}

func TestDispatcher_AlwaysOnEmitsForm(t *testing.T) {
	d := makeTestDispatcher(t)
	send, events := newCaptureSendEvent()
	res := d.Dispatch(DispatchInput{
		UID:         42,
		SkillBundle: bundleWithForm(),
		SendEvent:   send,
	})
	if !res.Emitted {
		t.Errorf("expected form emission in always-on mode (reason=%s)", res.Reason)
	}
	if len(*events) == 0 {
		t.Error("expected SSE events")
	}
}

func TestDispatcher_NotFirstTurnAloneStillEmits(t *testing.T) {
	// G11 (2026-05-17) — the dispatcher no longer gates on
	// ThreadMessageCnt > 0. A skill switched mid-thread (turn 3,
	// 5, 7...) with no per-skill discovery row MUST be allowed to
	// emit its form. AlreadyAnswered (caller-set via the per-skill
	// FindLatestDiscoveryAnswersForSkill lookup) is now the
	// authoritative gate.
	setOpenToAllFeatures(t)
	d := makeTestDispatcher(t)
	send, events := newCaptureSendEvent()
	res := d.Dispatch(DispatchInput{
		UID:              42,
		ThreadMessageCnt: 3,                // third turn — pre-G11 would suppress
		SkillBundle:      bundleWithForm(), // current skill has a form
		AlreadyAnswered:  false,            // no discovery for this skill yet
		SendEvent:        send,
	})
	if !res.Emitted {
		t.Errorf("post-G11 dispatcher must emit when no per-skill discovery exists, got Emitted=false reason=%q", res.Reason)
	}
	if len(*events) == 0 {
		t.Errorf("expected SSE events to be emitted, got 0")
	}
}

func TestDispatcher_AlreadyAnsweredPassesThrough(t *testing.T) {
	setOpenToAllFeatures(t)
	d := makeTestDispatcher(t)
	send, _ := newCaptureSendEvent()
	res := d.Dispatch(DispatchInput{
		UID:             42,
		SkillBundle:     bundleWithForm(),
		AlreadyAnswered: true,
		SendEvent:       send,
	})
	if res.Emitted {
		t.Errorf("expected pass-through when already answered, got Emitted=true")
	}
}

func TestDispatcher_NoSkillFormPassesThrough(t *testing.T) {
	setOpenToAllFeatures(t)
	d := makeTestDispatcher(t)
	send, events := newCaptureSendEvent()
	bundle := &skills.SkillBundle{Name: "ppt"} // no QuestionForm field
	res := d.Dispatch(DispatchInput{
		UID:         42,
		SkillBundle: bundle,
		SendEvent:   send,
	})
	if res.Emitted {
		t.Errorf("expected pass-through when skill has no form, got Emitted=true")
	}
	if len(*events) != 0 {
		t.Errorf("expected no events, got %d", len(*events))
	}
}

func TestDispatcher_FormDisabledPassesThrough(t *testing.T) {
	setOpenToAllFeatures(t)
	d := makeTestDispatcher(t)
	send, _ := newCaptureSendEvent()
	bundle := &skills.SkillBundle{
		Name:         "ppt",
		QuestionForm: &skills.QuestionForm{Enabled: false},
	}
	res := d.Dispatch(DispatchInput{UID: 42, SkillBundle: bundle, SendEvent: send})
	if res.Emitted {
		t.Errorf("expected pass-through when form.enabled=false")
	}
}

func TestDispatcher_HappyPathEmitsBlockAndDone(t *testing.T) {
	setOpenToAllFeatures(t)
	d := makeTestDispatcher(t)
	send, events := newCaptureSendEvent()

	res := d.Dispatch(DispatchInput{
		UID:           42,
		SkillBundle:   bundleWithForm(),
		Locale:        "zh",
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

	// Block payload sanity
	var blockPayload map[string]interface{}
	if err := json.Unmarshal(block.Block, &blockPayload); err != nil {
		t.Fatalf("unmarshal block: %v", err)
	}
	if blockPayload["type"] != "question_form" {
		t.Errorf("block.type should be question_form, got %v", blockPayload["type"])
	}
	// G10 (2026-05-17) — form_id is now the skill name, not the
	// legacy constant "discovery". The test bundle is Name="ppt".
	if blockPayload["form_id"] != "ppt" {
		t.Errorf("block.form_id should be the skill name 'ppt', got %v", blockPayload["form_id"])
	}
	schema := blockPayload["schema"].(map[string]interface{})
	questions := schema["questions"].([]interface{})
	if len(questions) != 1 {
		t.Errorf("expected 1 resolved question, got %d", len(questions))
	}

	// Locale resolution check — zh should produce 受众 not "Audience"
	q0 := questions[0].(map[string]interface{})
	if q0["label"] != "受众" {
		t.Errorf("expected zh label '受众', got %q", q0["label"])
	}

	// Done event sanity
	var resultPayload map[string]interface{}
	if err := json.Unmarshal(done.Result, &resultPayload); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if resultPayload["stop_reason"] != "form_emitted" {
		t.Errorf("done.stop_reason should be form_emitted, got %v", resultPayload["stop_reason"])
	}
	if resultPayload["is_error"] != false {
		t.Errorf("done.is_error should be false, got %v", resultPayload["is_error"])
	}
}

func TestDispatcher_PartialInferencePrefillsMatchingQuestionDefault(t *testing.T) {
	setOpenToAllFeatures(t)
	d := makeTestDispatcher(t)
	send, events := newCaptureSendEvent()

	res := d.Dispatch(DispatchInput{
		UID:           42,
		SkillBundle:   bundleWithForm(),
		Locale:        "en",
		TempMessageID: "msg-prefill",
		PrefillAnswers: map[string]string{
			"audience": "investor",
		},
		SendEvent: send,
	})
	if !res.Emitted {
		t.Fatalf("expected Emitted=true, got false (reason=%s)", res.Reason)
	}
	if len(*events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(*events))
	}

	var blockPayload map[string]interface{}
	if err := json.Unmarshal((*events)[0].Block, &blockPayload); err != nil {
		t.Fatalf("unmarshal block: %v", err)
	}
	questions := blockPayload["schema"].(map[string]interface{})["questions"].([]interface{})
	q0 := questions[0].(map[string]interface{})
	if got := q0["default"]; got != "investor" {
		t.Fatalf("expected inferred prefill to override manifest default, got %v", got)
	}
}

func TestDispatcher_RejectsInvalidPrefillAnswer(t *testing.T) {
	setOpenToAllFeatures(t)
	d := makeTestDispatcher(t)
	send, events := newCaptureSendEvent()

	res := d.Dispatch(DispatchInput{
		UID:           42,
		SkillBundle:   bundleWithForm(),
		Locale:        "en",
		TempMessageID: "msg-prefill-invalid",
		PrefillAnswers: map[string]string{
			"audience": "cross_thread_option",
		},
		SendEvent: send,
	})
	if !res.Emitted {
		t.Fatalf("expected Emitted=true, got false (reason=%s)", res.Reason)
	}

	var blockPayload map[string]interface{}
	if err := json.Unmarshal((*events)[0].Block, &blockPayload); err != nil {
		t.Fatalf("unmarshal block: %v", err)
	}
	questions := blockPayload["schema"].(map[string]interface{})["questions"].([]interface{})
	q0 := questions[0].(map[string]interface{})
	if got := q0["default"]; got != "exec" {
		t.Fatalf("expected invalid prefill to preserve manifest default, got %v", got)
	}
}

func TestDispatcher_LocaleFallbackToEn(t *testing.T) {
	setOpenToAllFeatures(t)
	d := makeTestDispatcher(t)
	send, events := newCaptureSendEvent()

	d.Dispatch(DispatchInput{
		UID:         42,
		SkillBundle: bundleWithForm(),
		// After the 2026-05-12 i18n backfill all 18 platform
		// locales have catalog files, so the fallback path is now
		// exercised by a deliberately-bogus locale rather than
		// "ar"/"de"/etc. Previously this was "ar" (then
		// uncatalogued); kept the test to pin the fallback contract.
		Locale:        "totally-fake-locale-xyz",
		TempMessageID: "msg-1",
		SendEvent:     send,
	})

	if len(*events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(*events))
	}
	var blockPayload map[string]interface{}
	_ = json.Unmarshal((*events)[0].Block, &blockPayload)
	q := blockPayload["schema"].(map[string]interface{})["questions"].([]interface{})[0].(map[string]interface{})
	if q["label"] != "Audience" {
		t.Errorf("expected en fallback 'Audience', got %q", q["label"])
	}
}

func TestDispatcher_DropsMalformedQuestion(t *testing.T) {
	setOpenToAllFeatures(t)
	d := makeTestDispatcher(t)
	send, events := newCaptureSendEvent()

	bundle := &skills.SkillBundle{
		Name: "ppt",
		QuestionForm: &skills.QuestionForm{
			Enabled: true,
			Questions: []skills.QuestionFormField{
				{ID: "", LabelKey: "form.ppt.tone.label", Type: "single_select"}, // missing id → drop
				{ID: "audience", LabelKey: "form.ppt.audience.label", Type: "single_select",
					Options: []skills.QuestionFormOption{
						{Value: "exec", LabelKey: "form.ppt.audience.exec"},
					}},
				{ID: "empty_select", LabelKey: "form.ppt.tone.label", Type: "single_select"}, // no options → drop
			},
		},
	}

	d.Dispatch(DispatchInput{
		UID:           42,
		SkillBundle:   bundle,
		Locale:        "en",
		TempMessageID: "msg-1",
		SendEvent:     send,
	})
	if len(*events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(*events))
	}
	var blockPayload map[string]interface{}
	_ = json.Unmarshal((*events)[0].Block, &blockPayload)
	questions := blockPayload["schema"].(map[string]interface{})["questions"].([]interface{})
	if len(questions) != 1 {
		t.Errorf("expected 1 valid question after dropping malformed, got %d", len(questions))
	}
}

func TestDispatcher_NoQuestionsSkipsEmit(t *testing.T) {
	setOpenToAllFeatures(t)
	d := makeTestDispatcher(t)
	send, events := newCaptureSendEvent()

	bundle := &skills.SkillBundle{
		Name: "ppt",
		QuestionForm: &skills.QuestionForm{
			Enabled:   true,
			Questions: []skills.QuestionFormField{}, // empty
		},
	}
	res := d.Dispatch(DispatchInput{
		UID: 42, SkillBundle: bundle, SendEvent: send,
	})
	if res.Emitted {
		t.Errorf("expected pass-through with no questions, got Emitted=true")
	}
	if len(*events) != 0 {
		t.Errorf("expected no events emitted, got %d", len(*events))
	}
}

// bundleWithForm builds a skill bundle with a single ppt-style
// audience question. Used across the happy-path tests.
func bundleWithForm() *skills.SkillBundle {
	return &skills.SkillBundle{
		Name:    "ppt",
		Version: "test",
		QuestionForm: &skills.QuestionForm{
			Enabled: true,
			Questions: []skills.QuestionFormField{
				{
					ID:       "audience",
					LabelKey: "form.ppt.audience.label",
					Type:     "single_select",
					Required: true,
					Default:  "exec",
					Options: []skills.QuestionFormOption{
						{Value: "exec", LabelKey: "form.ppt.audience.exec"},
						{Value: "investor", LabelKey: "form.ppt.audience.investor"},
					},
				},
			},
		},
	}
}
