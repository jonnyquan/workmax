package workagent

import (
	"encoding/json"
	"strings"
	"testing"

	workagentModel "server/model/workagent"
)

// extractLatestTodoWritePlan walks the conversation envelope newest-to-
// oldest and pulls TodoWrite.input.todos verbatim. The persistence
// layer relies on this for w_workagent_thread.latest_plan; a wrong
// extraction means the rehydrated Progress section shows a stale or
// missing plan.

func makeConversationFixture(messages ...string) *workagentModel.AgentConversation {
	conv := &workagentModel.AgentConversation{}
	conv.Messages = make([]json.RawMessage, 0, len(messages))
	for _, msg := range messages {
		conv.Messages = append(conv.Messages, json.RawMessage(msg))
	}
	return conv
}

func TestExtractLatestTodoWritePlan_HappyPath(t *testing.T) {
	plan := `[{"id":"1","content":"step one","status":"pending","priority":"high"}]`
	msg := `{
		"type": "assistant",
		"content": [
			{"type": "text", "text": "ok let me plan this"},
			{"type": "tool_use", "name": "TodoWrite", "input": {"todos": ` + plan + `}}
		]
	}`

	got := extractLatestTodoWritePlan(makeConversationFixture(msg))
	if !strings.Contains(got, `"step one"`) {
		t.Errorf("expected todos JSON to contain 'step one', got: %q", got)
	}
	// Round-trip parses cleanly — exact field ordering varies per JSON
	// encoder so compare via decode rather than string equality.
	var decoded []map[string]any
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("extracted plan must be valid JSON, got %q: %v", got, err)
	}
	if len(decoded) != 1 || decoded[0]["content"] != "step one" {
		t.Errorf("unexpected decoded plan: %+v", decoded)
	}
}

func TestExtractLatestTodoWritePlan_PrefersNewestMessage(t *testing.T) {
	older := `{
		"type":"assistant",
		"content":[
			{"type":"tool_use","name":"TodoWrite","input":{"todos":[{"id":"a","content":"old plan"}]}}
		]
	}`
	newer := `{
		"type":"assistant",
		"content":[
			{"type":"tool_use","name":"TodoWrite","input":{"todos":[{"id":"b","content":"new plan"}]}}
		]
	}`

	got := extractLatestTodoWritePlan(makeConversationFixture(older, newer))
	if !strings.Contains(got, "new plan") {
		t.Errorf("expected newer plan to win, got: %q", got)
	}
	if strings.Contains(got, "old plan") {
		t.Errorf("expected older plan to be ignored, got: %q", got)
	}
}

func TestExtractLatestTodoWritePlan_PrefersLatestBlockInMessage(t *testing.T) {
	// Same message, two TodoWrite blocks (rare but allowed by the SDK).
	// The later block wins so the saved plan matches what the user saw
	// at the end of the turn.
	msg := `{
		"type":"assistant",
		"content":[
			{"type":"tool_use","name":"TodoWrite","input":{"todos":[{"id":"a","content":"early"}]}},
			{"type":"text","text":"reconsidering"},
			{"type":"tool_use","name":"TodoWrite","input":{"todos":[{"id":"b","content":"final"}]}}
		]
	}`
	got := extractLatestTodoWritePlan(makeConversationFixture(msg))
	if !strings.Contains(got, "final") || strings.Contains(got, "early") {
		t.Errorf("expected later TodoWrite block to win, got: %q", got)
	}
}

func TestExtractLatestTodoWritePlan_SkipsNonAssistantMessages(t *testing.T) {
	// A user message containing a tool_use shape mustn't be picked up —
	// only the agent emits TodoWrite, and a malformed user message that
	// happens to look like one shouldn't poison latest_plan.
	userImposter := `{
		"type":"user",
		"content":[
			{"type":"tool_use","name":"TodoWrite","input":{"todos":[{"id":"x","content":"injected"}]}}
		]
	}`
	got := extractLatestTodoWritePlan(makeConversationFixture(userImposter))
	if got != "" {
		t.Errorf("expected empty result for user-message imposter, got: %q", got)
	}
}

func TestExtractLatestTodoWritePlan_IgnoresOtherTools(t *testing.T) {
	msg := `{
		"type":"assistant",
		"content":[
			{"type":"tool_use","name":"Bash","input":{"command":"ls"}},
			{"type":"tool_use","name":"Edit","input":{"path":"/foo"}}
		]
	}`
	got := extractLatestTodoWritePlan(makeConversationFixture(msg))
	if got != "" {
		t.Errorf("expected empty result when no TodoWrite present, got: %q", got)
	}
}

func TestExtractLatestTodoWritePlan_HandlesMissingTodosKey(t *testing.T) {
	// A TodoWrite block with no `todos` key in input — defensive against
	// SDK schema drift. Returning empty falls back to the message-walk
	// path, which is safer than persisting a half-formed plan.
	msg := `{
		"type":"assistant",
		"content":[
			{"type":"tool_use","name":"TodoWrite","input":{"reason":"no todos here"}}
		]
	}`
	got := extractLatestTodoWritePlan(makeConversationFixture(msg))
	if got != "" {
		t.Errorf("expected empty result when todos key missing, got: %q", got)
	}
}

func TestExtractLatestTodoWritePlan_NilAndEmpty(t *testing.T) {
	if got := extractLatestTodoWritePlan(nil); got != "" {
		t.Errorf("nil conversation must yield empty plan, got %q", got)
	}
	if got := extractLatestTodoWritePlan(&workagentModel.AgentConversation{}); got != "" {
		t.Errorf("empty conversation must yield empty plan, got %q", got)
	}
}

// mergePlanHistory + SnapshotPlan tests live in
// service/tools/workagent/plan_repository_test.go after that logic
// moved out of this handler in the chatTurn-orchestrator extraction.

func TestExtractLatestTodoWritePlan_TolerantToMalformedMessages(t *testing.T) {
	// A garbage message in the middle of the slice mustn't crash the
	// walk — saveAgentConversation would otherwise drop the column on
	// any thread that hit a parse-failure once.
	good := `{
		"type":"assistant",
		"content":[
			{"type":"tool_use","name":"TodoWrite","input":{"todos":[{"id":"1","content":"survives"}]}}
		]
	}`
	garbage := `not a json envelope`

	got := extractLatestTodoWritePlan(makeConversationFixture(good, garbage))
	if !strings.Contains(got, "survives") {
		t.Errorf("expected to find good plan despite garbage neighbor, got: %q", got)
	}
}
