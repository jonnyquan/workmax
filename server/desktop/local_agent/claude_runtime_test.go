//go:build desktop

package local_agent

import (
	"context"
	"strings"
	"testing"

	claudesdk "github.com/jonnyquan/claude-agent-sdk-go/pkg/claudesdk"

	cloudproxy "server/desktop/cloud_proxy"
)

func resultWithSession(sessionID string) *claudesdk.ResultMessage {
	ok := "OK"
	return &claudesdk.ResultMessage{NumTurns: 1, SessionID: sessionID, Result: &ok}
}

func streamText(delta string) *claudesdk.StreamEvent {
	return &claudesdk.StreamEvent{Event: map[string]any{
		"type":  "content_block_delta",
		"delta": map[string]any{"type": "text_delta", "text": delta},
	}}
}

func streamThinking(delta string) *claudesdk.StreamEvent {
	return &claudesdk.StreamEvent{Event: map[string]any{
		"type":  "content_block_delta",
		"delta": map[string]any{"type": "thinking_delta", "thinking": delta},
	}}
}

func thinking(s string) *claudesdk.AssistantMessage {
	return &claudesdk.AssistantMessage{Content: []claudesdk.ContentBlock{
		&claudesdk.ThinkingBlock{Type: "thinking", Thinking: s},
	}}
}

// chatReqWithTurn is chatReq with a caller-chosen turn UUID, so multi-turn
// tests do not collide on the idempotency key.
func chatReqWithTurn(turnUUID string) cloudproxy.ChatRequest {
	req := chatReq()
	req.TurnUUID = turnUUID
	return req
}

// scriptedTurn runs one Chat against a scripted stream, capturing the prompt
// and resolved SDK options the seam received.
func scriptedTurn(t *testing.T, engine *Engine, req cloudproxy.ChatRequest, iter *scriptedIterator, dst *memSSEWriter) (prompt string, opts claudesdk.Options, err error) {
	t.Helper()
	engine.claudeCfg().query = func(_ context.Context, p string, optFns ...claudesdk.Option) (claudesdk.MessageIterator, error) {
		prompt = p
		for _, fn := range optFns {
			fn(&opts)
		}
		return iter, nil
	}
	err = engine.Chat(context.Background(), req, dst)
	return prompt, opts, err
}

// The session lifecycle: turn one stores the reported ref; turn two resumes
// it and stops flattening history; a failed resumed turn clears it so the
// next turn self-heals onto the flatten path.
func TestChat_SessionResumeLifecycle(t *testing.T) {
	db := openTestDB(t)
	seedThreadRow(t, db)
	// A completed prior pair so the flatten path has something to flatten.
	if err := db.Exec(
		`INSERT INTO w_workagent_message (uid, uuid, thread_id, user_text, ai_text) VALUES (?, 'm-1', 1, 'earlier question', 'earlier answer')`,
		testUID,
	).Error; err != nil {
		t.Fatal(err)
	}
	engine, _ := newScriptedEngine(t, db, &scriptedIterator{})

	// Turn 1: no ref yet — history flattens in, and the reported session id
	// is stored.
	prompt, opts, err := scriptedTurn(t, engine,
		chatReqWithTurn("de305d54-75b4-431b-adb2-eb6b9e546001"),
		&scriptedIterator{messages: []claudesdk.Message{
			text("first answer"),
			resultWithSession("sess-1"),
		}}, &memSSEWriter{})
	if err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	if !strings.Contains(prompt, "Previous conversation") || !strings.Contains(prompt, "earlier question") {
		t.Errorf("turn 1 prompt must flatten history, got %q", prompt)
	}
	if opts.Resume != nil {
		t.Errorf("turn 1 must not resume, got %v", *opts.Resume)
	}
	if got := loadSessionRef(db, "thr_l2", "claude"); got != "sess-1" {
		t.Fatalf("stored ref = %q, want sess-1", got)
	}

	// Turn 2: the ref resumes the CLI session and history stays out of the
	// prompt — the session already holds it.
	prompt, opts, err = scriptedTurn(t, engine,
		chatReqWithTurn("de305d54-75b4-431b-adb2-eb6b9e546002"),
		&scriptedIterator{messages: []claudesdk.Message{
			text("second answer"),
			resultWithSession("sess-2"),
		}}, &memSSEWriter{})
	if err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	if opts.Resume == nil || *opts.Resume != "sess-1" {
		t.Errorf("turn 2 must resume sess-1, got %v", opts.Resume)
	}
	if strings.Contains(prompt, "Previous conversation") {
		t.Errorf("a resumed turn must not flatten history, got %q", prompt)
	}
	if got := loadSessionRef(db, "thr_l2", "claude"); got != "sess-2" {
		t.Errorf("ref after turn 2 = %q, want sess-2 (latest wins)", got)
	}

	// Turn 3: a failed resumed turn drops the ref.
	_, _, err = scriptedTurn(t, engine,
		chatReqWithTurn("de305d54-75b4-431b-adb2-eb6b9e546003"),
		&scriptedIterator{messages: []claudesdk.Message{
			result(true, "boom"),
		}}, &memSSEWriter{})
	if err == nil {
		t.Fatal("turn 3 must fail")
	}
	if got := loadSessionRef(db, "thr_l2", "claude"); got != "" {
		t.Errorf("ref after failed resume = %q, want cleared", got)
	}
}

// With partial messages on, stream deltas are the product and the full
// blocks that repeat them stay silent; thinking rides its own frame type.
func TestChat_PartialDeltasSupersedeFullBlocks(t *testing.T) {
	db := openTestDB(t)
	seedThreadRow(t, db)
	engine, _ := newScriptedEngine(t, db, &scriptedIterator{})

	dst := &memSSEWriter{}
	_, _, err := scriptedTurn(t, engine, chatReq(), &scriptedIterator{messages: []claudesdk.Message{
		streamThinking("mulling"),
		thinking("mulling"),
		streamText("He"),
		streamText("llo"),
		text("Hello"), // the full block repeating the deltas — must not re-emit
		resultWithSession("sess-x"),
	}}, dst)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	frames, _ := dst.snapshot()
	var kinds, deltas []string
	for _, f := range frames {
		kinds = append(kinds, f.Type)
		deltas = append(deltas, f.Data)
	}
	want := []string{"reasoning_delta", "text_delta", "text_delta", "done"}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("frame order = %v, want %v (data: %v)", kinds, want, deltas)
	}
	var state, aiText string
	_ = db.Raw(`SELECT streaming_state, ai_text FROM w_workagent_message WHERE message_idempotency_key = ?`,
		"desktop-turn:"+testTurnUUID).Row().Scan(&state, &aiText)
	if state != "complete" || aiText != "Hello" {
		t.Errorf("cache row = (%q, %q), want (complete, Hello) — dedup must not halve the cached answer", state, aiText)
	}
}

// A stream with no partial deltas (older CLI, stripping gateway) falls back
// to block-sized frames instead of silence.
func TestChat_FullBlocksWithoutPartialsStillEmit(t *testing.T) {
	db := openTestDB(t)
	seedThreadRow(t, db)
	engine, _ := newScriptedEngine(t, db, &scriptedIterator{})

	dst := &memSSEWriter{}
	_, _, err := scriptedTurn(t, engine, chatReq(), &scriptedIterator{messages: []claudesdk.Message{
		thinking("brief thought"),
		text("block answer"),
		resultWithSession(""),
	}}, dst)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	frames, _ := dst.snapshot()
	var kinds []string
	for _, f := range frames {
		kinds = append(kinds, f.Type)
	}
	want := []string{"reasoning_delta", "text_delta", "done"}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("frame order = %v, want %v", kinds, want)
	}
}

// The system prompt and partial-message flag are part of every query; the
// options are the contract the CLI subprocess is launched under.
func TestChat_QueryOptionContract(t *testing.T) {
	db := openTestDB(t)
	seedThreadRow(t, db)
	engine, _ := newScriptedEngine(t, db, &scriptedIterator{})

	_, opts, err := scriptedTurn(t, engine, chatReq(), &scriptedIterator{messages: []claudesdk.Message{
		text("hi"),
		resultWithSession(""),
	}}, &memSSEWriter{})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if opts.SystemPrompt != localAgentSystemPrompt {
		t.Errorf("system prompt = %v", opts.SystemPrompt)
	}
	if !opts.IncludePartialMessages {
		t.Error("partial messages must be requested")
	}
	if _, ok := opts.ExtraEnv["ANTHROPIC_BASE_URL"]; !ok {
		t.Error("base URL must reach the subprocess env")
	}
}

// Cancellation on a resumed turn keeps the ref: the previous turn's session
// is still the right resume target.
func TestChat_CancellationKeepsSessionRef(t *testing.T) {
	db := openTestDB(t)
	seedThreadRow(t, db)
	storeSessionRef(db, "thr_l2", "claude", "sess-keep")
	engine, _ := newScriptedEngine(t, db, &scriptedIterator{})

	_, _, err := scriptedTurn(t, engine, chatReq(), &scriptedIterator{
		messages: []claudesdk.Message{text("partial ")},
		terminal: context.Canceled,
	}, &memSSEWriter{})
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("err = %v", err)
	}
	if got := loadSessionRef(db, "thr_l2", "claude"); got != "sess-keep" {
		t.Errorf("ref after cancel = %q, want sess-keep", got)
	}
}
