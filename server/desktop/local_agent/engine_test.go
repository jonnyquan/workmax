//go:build desktop

package local_agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	claudesdk "github.com/jonnyquan/claude-agent-sdk-go/pkg/claudesdk"

	agentruntime "server/desktop/agentruntime"
	cloudproxy "server/desktop/cloud_proxy"
	localinference "server/desktop/local_inference"
	migrationsdesktop "server/desktop/migrations_desktop"
)

const (
	testUID      = uint64(42)
	testTurnUUID = "de305d54-75b4-431b-adb2-eb6b9e546014"
)

type stubProfile struct{ baseURL, modelID, apiKey string }

func (s stubProfile) LocalInferenceProfile() (string, string, string, string, error) {
	return "anthropic_compatible", s.baseURL, s.modelID, s.apiKey, nil
}

type memSSEWriter struct {
	mu     sync.Mutex
	frames []cloudproxy.SSEEvent
	errors []cloudproxy.ProxyError
}

func (m *memSSEWriter) WriteEvent(ev cloudproxy.SSEEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.frames = append(m.frames, ev)
	return nil
}

func (m *memSSEWriter) WriteProxyError(pe cloudproxy.ProxyError) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.errors = append(m.errors, pe)
	return nil
}

func (m *memSSEWriter) WriteKeepalive() error { return nil }

func (m *memSSEWriter) snapshot() ([]cloudproxy.SSEEvent, []cloudproxy.ProxyError) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]cloudproxy.SSEEvent(nil), m.frames...), append([]cloudproxy.ProxyError(nil), m.errors...)
}

// scriptedIterator plays back a fixed message sequence, then a terminal error.
type scriptedIterator struct {
	messages []claudesdk.Message
	terminal error
	index    int
}

func (s *scriptedIterator) Next(context.Context) (claudesdk.Message, error) {
	if s.index >= len(s.messages) {
		if s.terminal != nil {
			return nil, s.terminal
		}
		return nil, claudesdk.ErrNoMoreMessages
	}
	msg := s.messages[s.index]
	s.index++
	return msg, nil
}

func (s *scriptedIterator) Close() error { return nil }

func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "agent_test.db")), &gorm.Config{
		Logger: gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := migrationsdesktop.Apply(db); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	return db
}

// newScriptedEngine returns an engine whose SDK seam replays the given
// messages, plus a capture of the prompt the seam received.
func newScriptedEngine(t *testing.T, db *gorm.DB, iter *scriptedIterator) (*Engine, *string) {
	t.Helper()
	engine := NewEngine(stubProfile{baseURL: "http://127.0.0.1:1", modelID: "m"}, db, nil, nil,
		"/fake/claude", t.TempDir())
	var captured string
	engine.claudeCfg().query = func(_ context.Context, prompt string, _ ...claudesdk.Option) (claudesdk.MessageIterator, error) {
		captured = prompt
		return iter, nil
	}
	return engine, &captured
}

func text(s string) *claudesdk.AssistantMessage {
	return &claudesdk.AssistantMessage{Content: []claudesdk.ContentBlock{&claudesdk.TextBlock{Type: "text", Text: s}}}
}

func toolUse(name string) *claudesdk.AssistantMessage {
	return &claudesdk.AssistantMessage{Content: []claudesdk.ContentBlock{&claudesdk.ToolUseBlock{Type: "tool_use", ID: "t1", Name: name}}}
}

func result(isError bool, msg string) *claudesdk.ResultMessage {
	r := &claudesdk.ResultMessage{IsError: isError, NumTurns: 2}
	if msg != "" {
		r.Result = &msg
	}
	return r
}

func chatReq() cloudproxy.ChatRequest {
	return cloudproxy.ChatRequest{
		ThreadID: 1, ThreadUUID: "thr_l2", TurnUUID: testTurnUUID,
		UID: testUID, UserText: "do the work", ChatMode: "ppt",
	}
}

func seedThreadRow(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec(
		`INSERT INTO w_workagent_thread (id, uid, uuid, name) VALUES (1, ?, 'thr_l2', 'L2')`,
		testUID,
	).Error; err != nil {
		t.Fatal(err)
	}
}

// The stream contract: text and tool activity in order, one done terminal,
// and the cache holding the full assistant text as a complete row.
func TestChat_StreamsTextToolsAndDone(t *testing.T) {
	db := openTestDB(t)
	seedThreadRow(t, db)
	engine, _ := newScriptedEngine(t, db, &scriptedIterator{messages: []claudesdk.Message{
		toolUse("Write"),
		text("Wrote the file. "),
		text("All done."),
		result(false, "All done."),
	}})

	dst := &memSSEWriter{}
	if err := engine.Chat(context.Background(), chatReq(), dst); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	frames, perrs := dst.snapshot()
	if len(perrs) != 0 {
		t.Fatalf("unexpected proxy errors: %+v", perrs)
	}
	var kinds []string
	for _, f := range frames {
		kinds = append(kinds, f.Type)
	}
	want := []string{"tool_use", "text_delta", "text_delta", "done"}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("frame order = %v, want %v", kinds, want)
	}
	var tu struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(frames[0].Data), &tu); err != nil || tu.Name != "Write" {
		t.Errorf("tool_use payload = %q (%v)", frames[0].Data, err)
	}
	if frames[3].Data != doneEventData {
		t.Errorf("done frame = %q; it must be byte-identical to L1's so the cache classifier cannot tell routes apart", frames[3].Data)
	}

	var state, aiText string
	if err := db.Raw(
		`SELECT streaming_state, COALESCE(ai_text,'') FROM w_workagent_message WHERE message_idempotency_key = ?`,
		"desktop-turn:"+testTurnUUID,
	).Row().Scan(&state, &aiText); err != nil {
		t.Fatalf("cache row: %v", err)
	}
	if state != "complete" || aiText != "Wrote the file. All done." {
		t.Errorf("cache row = %q %q", state, aiText)
	}
}

// The prompt is the only channel for cross-turn context; its layout is a
// contract: history, then attachments, then the (retrieval-wrapped) request.
func TestChat_PromptCarriesHistoryBeforeTheRequest(t *testing.T) {
	db := openTestDB(t)
	seedThreadRow(t, db)
	if err := db.Exec(
		`INSERT INTO w_workagent_message (uid, uuid, thread_id, user_text, ai_text, message_idempotency_key)
		 VALUES (?, 'm1', 1, 'earlier question', 'earlier answer', 'key-1')`,
		testUID,
	).Error; err != nil {
		t.Fatal(err)
	}
	engine, prompt := newScriptedEngine(t, db, &scriptedIterator{messages: []claudesdk.Message{
		text("ok"), result(false, "ok"),
	}})

	if err := engine.Chat(context.Background(), chatReq(), &memSSEWriter{}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	p := *prompt
	histAt := strings.Index(p, "earlier answer")
	reqAt := strings.Index(p, "do the work")
	if histAt < 0 || reqAt < 0 || histAt > reqAt {
		t.Fatalf("prompt order wrong (hist=%d req=%d):\n%s", histAt, reqAt, p)
	}
}

// A model-declared failure is a failed turn: proxy_error out, non-nil error
// back, and the cache row marked partial rather than complete.
func TestChat_ResultErrorFailsTheTurn(t *testing.T) {
	db := openTestDB(t)
	seedThreadRow(t, db)
	engine, _ := newScriptedEngine(t, db, &scriptedIterator{messages: []claudesdk.Message{
		text("half an answer"),
		result(true, "budget exceeded"),
	}})

	dst := &memSSEWriter{}
	err := engine.Chat(context.Background(), chatReq(), dst)
	if err == nil {
		t.Fatal("a result error must fail the turn")
	}
	_, perrs := dst.snapshot()
	if len(perrs) != 1 || perrs[0].Message != "budget exceeded" {
		t.Fatalf("proxy errors = %+v", perrs)
	}
	var state string
	_ = db.Raw(`SELECT streaming_state FROM w_workagent_message WHERE message_idempotency_key = ?`,
		"desktop-turn:"+testTurnUUID).Row().Scan(&state)
	if state != "partial" {
		t.Errorf("cache state = %q, want partial", state)
	}
}

// A stream that just stops is an infrastructure break, not a completion.
func TestChat_StreamEndWithoutResultFails(t *testing.T) {
	db := openTestDB(t)
	seedThreadRow(t, db)
	engine, _ := newScriptedEngine(t, db, &scriptedIterator{messages: []claudesdk.Message{
		text("cut off"),
	}})

	dst := &memSSEWriter{}
	if err := engine.Chat(context.Background(), chatReq(), dst); err == nil {
		t.Fatal("ending without a ResultMessage must fail the turn")
	}
	frames, perrs := dst.snapshot()
	if len(perrs) != 1 {
		t.Fatalf("proxy errors = %+v", perrs)
	}
	for _, f := range frames {
		if f.Type == "done" {
			t.Error("a truncated stream must not emit done")
		}
	}
}

// Cancellation is the user stopping their own turn: no proxy_error bubble,
// but a non-nil return so the intent is marked interrupted.
func TestChat_CancellationIsQuiet(t *testing.T) {
	db := openTestDB(t)
	seedThreadRow(t, db)
	engine, _ := newScriptedEngine(t, db, &scriptedIterator{
		messages: []claudesdk.Message{text("partial ")},
		terminal: context.Canceled,
	})

	dst := &memSSEWriter{}
	err := engine.Chat(context.Background(), chatReq(), dst)
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("err = %v, want context.Canceled through", err)
	}
	_, perrs := dst.snapshot()
	if len(perrs) != 0 {
		t.Errorf("cancellation must not surface a proxy_error, got %+v", perrs)
	}
}

// No CLI is a configuration state, not a crash: a typed, actionable error.
func TestChat_MissingCLIIsATypedError(t *testing.T) {
	db := openTestDB(t)
	engine := NewEngine(stubProfile{baseURL: "http://127.0.0.1:1", modelID: "m"}, db, nil, nil, "", t.TempDir())
	dst := &memSSEWriter{}
	if err := engine.Chat(context.Background(), chatReq(), dst); err == nil {
		t.Fatal("missing CLI must fail the turn")
	}
	_, perrs := dst.snapshot()
	if len(perrs) != 1 || perrs[0].Kind != cloudproxy.KindServiceUnavailable ||
		!strings.Contains(perrs[0].Message, "CLI") {
		t.Fatalf("proxy errors = %+v", perrs)
	}
}

// The two knowledge hooks fire exactly as on L1: retrieval announces before
// the first token, and a completed turn is indexed.
func TestChat_KnowledgeHooksMirrorL1(t *testing.T) {
	db := openTestDB(t)
	seedThreadRow(t, db)
	hooks := &recordingHooks{retrieve: []localinference.RetrievedSource{
		{Kind: "file", Label: "notes.md", Text: "KNOWN-FACT", Score: 0.9},
	}}
	engine, prompt := newScriptedEngine(t, db, &scriptedIterator{messages: []claudesdk.Message{
		text("answer"), result(false, "answer"),
	}})
	engine.hooks = hooks

	dst := &memSSEWriter{}
	if err := engine.Chat(context.Background(), chatReq(), dst); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	frames, _ := dst.snapshot()
	if frames[0].Type != "retrieval" {
		t.Errorf("first frame = %q, want the retrieval announcement", frames[0].Type)
	}
	if !strings.Contains(*prompt, "KNOWN-FACT") {
		t.Error("retrieved context must reach the prompt")
	}
	hooks.waitForIndex(t)
}

type recordingHooks struct {
	mu       sync.Mutex
	retrieve []localinference.RetrievedSource
	indexed  int
}

func (r *recordingHooks) IndexTurn(context.Context, uint64, string, string, string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.indexed++
	return nil
}

func (r *recordingHooks) Retrieve(context.Context, uint64, string, int) ([]localinference.RetrievedSource, error) {
	return r.retrieve, nil
}

func (r *recordingHooks) waitForIndex(t *testing.T) {
	t.Helper()
	for i := 0; i < 500; i++ {
		r.mu.Lock()
		n := r.indexed
		r.mu.Unlock()
		if n == 1 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("IndexTurn was not called")
}

// The other half of the vocabulary pin (pi_agent has the first): every tool the
// claude CLI may call must have a home in the shared approval policy too. The
// two engines share one ApprovalConfig, and a name in neither surface is denied
// by Consult with no card and no appeal.
func TestEveryClaudeToolHasAHomeInTheVocabulary(t *testing.T) {
	for _, tool := range allowedTools {
		if !agentruntime.ApprovalSurfaceHas(tool) {
			t.Fatalf("the claude surface enables %q, which is in neither "+
				"ApprovalReadSurface nor ApprovalWriteSurface", tool)
		}
	}
	// The CLI's own pre-allowed list is a SUBSET of the shared read surface,
	// never the other way round: WithAllowedTools may name only tools this CLI
	// actually has, while the policy has to cover pi's find/ls as well.
	for _, tool := range readOnlyTools {
		if !contains(agentruntime.ApprovalReadSurface, tool) {
			t.Fatalf("claude pre-allows %q, which the shared read surface does not carry", tool)
		}
	}
	for _, tool := range askTools {
		if !contains(agentruntime.ApprovalWriteSurface, tool) {
			t.Fatalf("claude asks for %q, which the shared write surface does not carry", tool)
		}
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
