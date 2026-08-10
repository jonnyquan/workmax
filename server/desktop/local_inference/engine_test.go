//go:build desktop

package local_inference

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	cloudproxy "server/desktop/cloud_proxy"
	migrationsdesktop "server/desktop/migrations_desktop"
)

// stubProfile 实现 ProfileReader，注入固定配置。
type stubProfile struct {
	protocol, baseURL, modelID, apiKey string
	err                                error
}

func (s stubProfile) LocalInferenceProfile() (string, string, string, string, error) {
	return s.protocol, s.baseURL, s.modelID, s.apiKey, s.err
}

// memSSEWriter 记录 engine 产出的 SSE 事件，供断言（local_inference 包内
// 自定义，因为 cloud_proxy 的同名 helper 在 _test.go 里不可跨包访问）。
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

func openLocalTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	// 每个测试唯一 DSN：共享内存缓存会跨测试泄漏行。
	dsn := "file:local-inference-" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := migrationsdesktop.Apply(db); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	return db
}

func sseLine(w io.Writer, line string) {
	if _, err := io.WriteString(w, line+"\n"); err != nil {
		// 测试上游写失败只能忽略；httptest 会记录。
		_ = err
	}
}

const (
	engineTestUID      = uint64(42)
	engineTestTurnUUID = "de305d54-75b4-431b-adb2-eb6b9e546014" // canonical RFC4122 v4
	engineTestModel    = "qwen2.5:0.5b"
)

func newTestEngine(t *testing.T, profile ProfileReader) (*Engine, *gorm.DB, *memSSEWriter) {
	t.Helper()
	db := openLocalTestDB(t)
	return NewEngine(profile, db, nil, nil), db, &memSSEWriter{}
}

// recordingIndexer is a fake KnowledgeHooks capturing IndexTurn calls so the
// L3c-4 hook can be asserted, and returning canned Retrieve results for the
// L3c-5 injection test. No cgo knowledge package needed.
type recordingIndexer struct {
	mu        sync.Mutex
	calls     []indexTurnCall
	retrieve  []RetrievedSource
	gotUID    uint64
	retrieved bool
}

type indexTurnCall struct {
	turnUUID, userText, assistantText string
}

func (r *recordingIndexer) IndexTurn(_ context.Context, turnUUID, userText, assistantText string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, indexTurnCall{turnUUID, userText, assistantText})
	return nil
}

func (r *recordingIndexer) Retrieve(_ context.Context, uid uint64, _ string, _ int) ([]RetrievedSource, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gotUID = uid
	r.retrieved = true
	out := make([]RetrievedSource, len(r.retrieve))
	copy(out, r.retrieve)
	return out, nil
}

func (r *recordingIndexer) snapshot() []indexTurnCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]indexTurnCall, len(r.calls))
	copy(out, r.calls)
	return out
}

func (r *recordingIndexer) waitForCall(t *testing.T, timeout time.Duration) []indexTurnCall {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if got := r.snapshot(); len(got) > 0 {
			return got
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("IndexTurn was not called within %v", timeout)
	return nil
}

// TestEngine_IndexesTurnOnSuccess verifies the L3c-4 hook fires after a clean
// turn with the turn uuid, user text, and accumulated assistant text.
func TestEngine_IndexesTurnOnSuccess(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f := w.(http.Flusher)
		sseLine(w, `data: {"choices":[{"delta":{"content":"Hello"}}]}`)
		sseLine(w, "")
		f.Flush()
		sseLine(w, "data: [DONE]")
		sseLine(w, "")
		f.Flush()
	}))
	t.Cleanup(upstream.Close)

	db := openLocalTestDB(t)
	idx := &recordingIndexer{}
	engine := NewEngine(
		stubProfile{protocol: protocolOpenAI, baseURL: upstream.URL, modelID: engineTestModel},
		db, nil, idx,
	)
	dst := &memSSEWriter{}
	if err := engine.Chat(context.Background(), cloudproxy.ChatRequest{
		ThreadID: 1, ThreadUUID: "thr_1", TurnUUID: engineTestTurnUUID,
		UID: engineTestUID, UserText: "hi", ChatMode: "general",
	}, dst); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	calls := idx.waitForCall(t, 2*time.Second)
	if len(calls) != 1 {
		t.Fatalf("want 1 IndexTurn call, got %d", len(calls))
	}
	c := calls[0]
	if c.turnUUID != engineTestTurnUUID || c.userText != "hi" || c.assistantText != "Hello" {
		t.Errorf("IndexTurn args = %+v, want {%s hi Hello}", c, engineTestTurnUUID)
	}
}

// TestEngine_DoesNotIndexOnFailure verifies the hook is skipped when a turn
// fails (upstream 500), so partial/failed turns are not recorded as memory.
func TestEngine_DoesNotIndexOnFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(upstream.Close)

	db := openLocalTestDB(t)
	idx := &recordingIndexer{}
	engine := NewEngine(
		stubProfile{protocol: protocolOpenAI, baseURL: upstream.URL, modelID: engineTestModel},
		db, nil, idx,
	)
	dst := &memSSEWriter{}
	_ = engine.Chat(context.Background(), cloudproxy.ChatRequest{
		ThreadID: 1, TurnUUID: engineTestTurnUUID, UID: engineTestUID, UserText: "hi",
	}, dst)

	if got := idx.snapshot(); len(got) != 0 {
		t.Errorf("IndexTurn must not be called on failure, got %+v", got)
	}
}

// TestEngine_InjectsRetrievedContext verifies L3c-5: retrieved knowledge chunks
// are prepended to the user text sent to the upstream model.
func TestEngine_InjectsRetrievedContext(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f := w.(http.Flusher)
		sseLine(w, "data: [DONE]")
		sseLine(w, "")
		f.Flush()
	}))
	t.Cleanup(upstream.Close)

	db := openLocalTestDB(t)
	idx := &recordingIndexer{retrieve: []RetrievedSource{
		{Kind: "file", Label: "alpha.md", Text: "ALPHA-FACT", Score: 0.91},
		{Kind: "conversation", Label: "Earlier conversation", Text: "BETA-FACT", Score: 0.62},
	}}
	engine := NewEngine(
		stubProfile{protocol: protocolOpenAI, baseURL: upstream.URL, modelID: engineTestModel},
		db, nil, idx,
	)
	dst := &memSSEWriter{}
	if err := engine.Chat(context.Background(), cloudproxy.ChatRequest{
		ThreadID: 1, ThreadUUID: "thr_1", TurnUUID: engineTestTurnUUID,
		UID: engineTestUID, UserText: "what is alpha?", ChatMode: "general",
	}, dst); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if !strings.Contains(gotBody, "ALPHA-FACT") || !strings.Contains(gotBody, "BETA-FACT") {
		t.Errorf("retrieved context not injected into upstream body: %s", gotBody)
	}
	if !strings.Contains(gotBody, "what is alpha?") {
		t.Errorf("original user text missing from body: %s", gotBody)
	}
	// The uid has to reach the store, or file names resolve against the wrong
	// owner and every source is labelled generically.
	if idx.gotUID != engineTestUID {
		t.Errorf("Retrieve got uid %d, want %d", idx.gotUID, engineTestUID)
	}
}

// The renderer cannot show what grounded an answer unless the stream says so.
// This pins the announcement: emitted before any text, naming both sources,
// and carrying what the model was actually given.
func TestEngine_AnnouncesRetrievedSources(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f := w.(http.Flusher)
		sseLine(w, `data: {"choices":[{"delta":{"content":"hello"}}]}`)
		sseLine(w, "")
		sseLine(w, "data: [DONE]")
		sseLine(w, "")
		f.Flush()
	}))
	t.Cleanup(upstream.Close)

	db := openLocalTestDB(t)
	idx := &recordingIndexer{retrieve: []RetrievedSource{
		{Kind: "file", Label: "q3-plan.md", Text: "Revenue grew 12%.", Score: 0.88},
		{Kind: "conversation", Label: "Earlier conversation", Text: "We agreed on the Q3 target.", Score: 0.55},
	}}
	engine := NewEngine(
		stubProfile{protocol: protocolOpenAI, baseURL: upstream.URL, modelID: engineTestModel},
		db, nil, idx,
	)
	dst := &memSSEWriter{}
	if err := engine.Chat(context.Background(), cloudproxy.ChatRequest{
		ThreadID: 1, ThreadUUID: "thr_1", TurnUUID: engineTestTurnUUID,
		UID: engineTestUID, UserText: "how did Q3 go?", ChatMode: "general",
	}, dst); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	events, _ := dst.snapshot()
	retrievalAt, firstTextAt := -1, -1
	for i, ev := range events {
		if ev.Type == "retrieval" && retrievalAt < 0 {
			retrievalAt = i
		}
		if ev.Type == "text_delta" && firstTextAt < 0 {
			firstTextAt = i
		}
	}
	if retrievalAt < 0 {
		t.Fatalf("no retrieval event on the stream; events=%+v", events)
	}
	if firstTextAt >= 0 && retrievalAt > firstTextAt {
		t.Errorf("retrieval announced at %d, after the first text at %d; the panel would fill in after the answer had started", retrievalAt, firstTextAt)
	}

	var payload struct {
		Sources []struct {
			Kind    string  `json:"kind"`
			Label   string  `json:"label"`
			Snippet string  `json:"snippet"`
			Score   float64 `json:"score"`
		} `json:"sources"`
	}
	if err := json.Unmarshal([]byte(events[retrievalAt].Data), &payload); err != nil {
		t.Fatalf("retrieval payload is not JSON: %v (%s)", err, events[retrievalAt].Data)
	}
	if len(payload.Sources) != 2 {
		t.Fatalf("announced %d sources, want 2: %+v", len(payload.Sources), payload.Sources)
	}
	if payload.Sources[0].Label != "q3-plan.md" || payload.Sources[0].Kind != "file" {
		t.Errorf("first source = %+v, want the file that ranked best", payload.Sources[0])
	}
	if payload.Sources[0].Snippet != "Revenue grew 12%." {
		t.Errorf("snippet = %q, want the chunk text the model was given", payload.Sources[0].Snippet)
	}
	if payload.Sources[1].Kind != "conversation" {
		t.Errorf("second source kind = %q, want conversation", payload.Sources[1].Kind)
	}
}

// A source dropped by the char budget must not be announced: the panel would
// credit a document the model never saw.
//
// (Assertion updated with the rune-budget fix: the accounting now charges each
// entry its real written size — text runes + 3 for "- " and "\n" — so the
// largest chunk that fits exactly is budget-3 runes, not budget runes.)
func TestPrependKnowledgeContextReportsOnlyWhatFitted(t *testing.T) {
	big := strings.Repeat("x", maxRetrievalContextChars-3)
	text, used := PrependKnowledgeContext("question", []RetrievedSource{
		{Kind: "file", Label: "kept.md", Text: big},
		{Kind: "file", Label: "dropped.md", Text: "this one is over budget"},
	})
	if len(used) != 1 || used[0].Label != "kept.md" {
		t.Fatalf("reported sources = %+v, want only kept.md", used)
	}
	if strings.Contains(text, "this one is over budget") {
		t.Error("the dropped chunk reached the model after all")
	}
}

// Every candidate blank or over budget means no context was added. Returning
// the preamble with an empty list would tell the model it had sources.
func TestPrependKnowledgeContextLeavesPromptAloneWhenNothingFits(t *testing.T) {
	text, used := PrependKnowledgeContext("question", []RetrievedSource{
		{Kind: "file", Label: "blank.md", Text: "   "},
	})
	if used != nil {
		t.Errorf("reported %+v, want nothing", used)
	}
	if text != "question" {
		t.Errorf("prompt = %q, want it untouched", text)
	}
}

func TestTruncateRunesCutsOnRuneBoundary(t *testing.T) {
	// Four-byte runes: a byte-wise cut here produces invalid UTF-8, which the
	// renderer's fatal TextDecoder turns into a failed turn.
	got := truncateRunes(strings.Repeat("𝄞", 300), 240)
	if !utf8.ValidString(got) {
		t.Fatalf("truncation produced invalid UTF-8: %q", got)
	}
	if utf8.RuneCountInString(got) != 241 { // 240 runes + the ellipsis
		t.Errorf("kept %d runes, want 240 plus an ellipsis", utf8.RuneCountInString(got))
	}
}

// cacheRow 读取本次 turn 写入的 message 行（按幂等 key）。
func cacheRow(t *testing.T, db *gorm.DB) (state, aiText string) {
	t.Helper()
	row := db.Raw(
		`SELECT streaming_state, COALESCE(ai_text,'') FROM w_workagent_message
		  WHERE message_idempotency_key = ? LIMIT 1`,
		"desktop-turn:"+engineTestTurnUUID,
	).Row()
	if err := row.Scan(&state, &aiText); err != nil {
		t.Fatalf("scan cache row: %v", err)
	}
	return state, aiText
}

// assertCleanDone 验证最后一帧是 done 且 Data 不会触发 classifyDoneForCache 标 partial。
func assertCleanDone(t *testing.T, frames []cloudproxy.SSEEvent) {
	t.Helper()
	if len(frames) == 0 || frames[len(frames)-1].Type != "done" {
		t.Fatalf("expected trailing done event, got frames: %+v", frames)
	}
	var check struct {
		IsError bool   `json:"is_error"`
		Code    string `json:"code"`
	}
	_ = json.Unmarshal([]byte(frames[len(frames)-1].Data), &check)
	if check.IsError || (check.Code != "" && check.Code != "OK") {
		t.Fatalf("done event would be classified partial: %s", frames[len(frames)-1].Data)
	}
}

func extractDeltaText(data string) string {
	var d struct {
		Delta string `json:"delta"`
	}
	_ = json.Unmarshal([]byte(data), &d)
	return d.Delta
}

func TestEngine_OpenAI_HappyPath(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("Accept header = %q", r.Header.Get("Accept"))
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f := w.(http.Flusher)
		sseLine(w, `data: {"choices":[{"delta":{"content":"Hello"}}]}`)
		sseLine(w, "")
		f.Flush()
		sseLine(w, `data: {"choices":[{"delta":{"content":" world"}}]}`)
		sseLine(w, "")
		f.Flush()
		sseLine(w, "data: [DONE]")
		sseLine(w, "")
		f.Flush()
	}))
	t.Cleanup(upstream.Close)

	engine, db, dst := newTestEngine(t, stubProfile{
		protocol: protocolOpenAI, baseURL: upstream.URL, modelID: engineTestModel, apiKey: "test-key",
	})
	err := engine.Chat(context.Background(), cloudproxy.ChatRequest{
		ThreadID: 1, ThreadUUID: "thr_1", TurnUUID: engineTestTurnUUID,
		UID: engineTestUID, UserText: "hi", ChatMode: "general",
	}, dst)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	frames, errs := dst.snapshot()
	if len(errs) != 0 {
		t.Fatalf("unexpected proxy errors: %+v", errs)
	}
	// 两个 text_delta + 一个 done。
	var got strings.Builder
	for _, f := range frames {
		if f.Type == "text_delta" {
			got.WriteString(extractDeltaText(f.Data))
		}
	}
	if got.String() != "Hello world" {
		t.Fatalf("delta text = %q, want %q", got.String(), "Hello world")
	}
	assertCleanDone(t, frames)

	state, aiText := cacheRow(t, db)
	if state != "complete" {
		t.Fatalf("cache streaming_state = %q, want complete", state)
	}
	if aiText != "Hello world" {
		t.Fatalf("cache ai_text = %q, want %q", aiText, "Hello world")
	}
}

func TestEngine_Anthropic_HappyPath(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-api-key"); got != "test-key" {
			t.Errorf("x-api-key = %q", got)
		}
		if r.Header.Get("anthropic-version") == "" {
			t.Errorf("missing anthropic-version header")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f := w.(http.Flusher)
		sseLine(w, "event: content_block_delta")
		sseLine(w, `data: {"delta":{"type":"text_delta","text":"Hola "}}`)
		sseLine(w, "")
		f.Flush()
		sseLine(w, "event: content_block_delta")
		sseLine(w, `data: {"delta":{"type":"text_delta","text":"mundo"}}`)
		sseLine(w, "")
		f.Flush()
		sseLine(w, "event: message_stop")
		sseLine(w, `data: {}`)
		sseLine(w, "")
		f.Flush()
	}))
	t.Cleanup(upstream.Close)

	engine, db, dst := newTestEngine(t, stubProfile{
		protocol: protocolAnthropic, baseURL: upstream.URL, modelID: "claude-test", apiKey: "test-key",
	})
	err := engine.Chat(context.Background(), cloudproxy.ChatRequest{
		ThreadID: 1, ThreadUUID: "thr_1", TurnUUID: engineTestTurnUUID,
		UID: engineTestUID, UserText: "hi", ChatMode: "general",
	}, dst)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	frames, errs := dst.snapshot()
	if len(errs) != 0 {
		t.Fatalf("unexpected proxy errors: %+v", errs)
	}
	var got strings.Builder
	for _, f := range frames {
		if f.Type == "text_delta" {
			got.WriteString(extractDeltaText(f.Data))
		}
	}
	if got.String() != "Hola mundo" {
		t.Fatalf("delta text = %q, want %q", got.String(), "Hola mundo")
	}
	assertCleanDone(t, frames)

	state, _ := cacheRow(t, db)
	if state != "complete" {
		t.Fatalf("cache streaming_state = %q, want complete", state)
	}
}

func TestEngine_HTTPError_500(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, "internal error")
	}))
	t.Cleanup(upstream.Close)

	engine, _, dst := newTestEngine(t, stubProfile{
		protocol: protocolOpenAI, baseURL: upstream.URL, modelID: engineTestModel, apiKey: "k",
	})
	err := engine.Chat(context.Background(), cloudproxy.ChatRequest{
		ThreadID: 1, TurnUUID: engineTestTurnUUID, UID: engineTestUID, UserText: "hi",
	}, dst)
	if err == nil {
		t.Fatal("expected error on HTTP 500")
	}
	_, errs := dst.snapshot()
	if len(errs) != 1 || errs[0].Kind != cloudproxy.KindServiceUnavailable {
		t.Fatalf("expected one KindServiceUnavailable proxy error, got %+v", errs)
	}
}

func TestEngine_HTTPError_401(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(upstream.Close)

	engine, _, dst := newTestEngine(t, stubProfile{
		protocol: protocolOpenAI, baseURL: upstream.URL, modelID: engineTestModel, apiKey: "bad-key",
	})
	err := engine.Chat(context.Background(), cloudproxy.ChatRequest{
		ThreadID: 1, TurnUUID: engineTestTurnUUID, UID: engineTestUID, UserText: "hi",
	}, dst)
	if err == nil {
		t.Fatal("expected error on HTTP 401")
	}
	_, errs := dst.snapshot()
	// 本地 API key 错误被映射成 KindAuthExpired（L2 可细化 KindLocalAuthError）。
	if len(errs) != 1 || errs[0].Kind != cloudproxy.KindAuthExpired {
		t.Fatalf("expected KindAuthExpired, got %+v", errs)
	}
}

func TestEngine_NetworkError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	upstream.Close() // 立即关闭，使连接失败

	engine, _, dst := newTestEngine(t, stubProfile{
		protocol: protocolOpenAI, baseURL: upstream.URL, modelID: engineTestModel, apiKey: "k",
	})
	err := engine.Chat(context.Background(), cloudproxy.ChatRequest{
		ThreadID: 1, TurnUUID: engineTestTurnUUID, UID: engineTestUID, UserText: "hi",
	}, dst)
	if err == nil {
		t.Fatal("expected network error")
	}
	_, errs := dst.snapshot()
	if len(errs) != 1 || errs[0].Kind != cloudproxy.KindNetworkUnavailable {
		t.Fatalf("expected KindNetworkUnavailable, got %+v", errs)
	}
}

func TestEngine_TruncatedStream(t *testing.T) {
	// 发一个 delta 后 EOF，无 [DONE] → 截断：renderer 收到 text_delta + proxy_error；
	// cache 行 streaming_state=partial。
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f := w.(http.Flusher)
		sseLine(w, `data: {"choices":[{"delta":{"content":"partial"}}]}`)
		sseLine(w, "")
		f.Flush()
		// 紧接 EOF，不发 [DONE]。
	}))
	t.Cleanup(upstream.Close)

	engine, db, dst := newTestEngine(t, stubProfile{
		protocol: protocolOpenAI, baseURL: upstream.URL, modelID: engineTestModel, apiKey: "k",
	})
	err := engine.Chat(context.Background(), cloudproxy.ChatRequest{
		ThreadID: 1, TurnUUID: engineTestTurnUUID, UID: engineTestUID, UserText: "hi",
	}, dst)
	if err == nil {
		t.Fatal("expected truncation error")
	}
	frames, errs := dst.snapshot()
	// 应有 text_delta，但无 done；应有一个 proxy_error。
	hasText := false
	for _, f := range frames {
		if f.Type == "text_delta" {
			hasText = true
		}
		if f.Type == "done" {
			t.Fatal("truncated stream must not emit done")
		}
	}
	if !hasText {
		t.Fatal("expected at least one text_delta before truncation")
	}
	if len(errs) != 1 || errs[0].Kind != cloudproxy.KindServiceUnavailable {
		t.Fatalf("expected KindServiceUnavailable proxy error, got %+v", errs)
	}
	state, _ := cacheRow(t, db)
	if state != "partial" {
		t.Fatalf("cache streaming_state = %q, want partial", state)
	}
}

func TestEngine_ContextCancelled(t *testing.T) {
	// 上游永不响应；ctx 取消后 Chat 返回 canceled，且不 emit proxy_error。
	block := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	// LIFO cleanup：先注册 upstream.Close、后注册 close(block)，使 close(block)
	// 先执行——否则 upstream.Close 会等永远阻塞的 handler，造成 cleanup 死锁。
	t.Cleanup(upstream.Close)
	t.Cleanup(func() { close(block) })

	engine, _, dst := newTestEngine(t, stubProfile{
		protocol: protocolOpenAI, baseURL: upstream.URL, modelID: engineTestModel, apiKey: "k",
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	err := engine.Chat(ctx, cloudproxy.ChatRequest{
		ThreadID: 1, TurnUUID: engineTestTurnUUID, UID: engineTestUID, UserText: "hi",
	}, dst)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	_, errs := dst.snapshot()
	if len(errs) != 0 {
		t.Fatalf("cancel must not emit proxy_error, got %+v", errs)
	}
}

func TestEngine_NotConfigured(t *testing.T) {
	// preferred_route=local 但 baseURL 空 → 按 OSS-4 不静默回退云端，显式报错。
	engine, _, dst := newTestEngine(t, stubProfile{
		protocol: protocolOpenAI, baseURL: "", modelID: "", apiKey: "",
	})
	err := engine.Chat(context.Background(), cloudproxy.ChatRequest{
		ThreadID: 1, TurnUUID: engineTestTurnUUID, UID: engineTestUID, UserText: "hi",
	}, dst)
	if err == nil {
		t.Fatal("expected not-configured error")
	}
	_, errs := dst.snapshot()
	if len(errs) != 1 || errs[0].Kind != cloudproxy.KindServiceUnavailable {
		t.Fatalf("expected KindServiceUnavailable, got %+v", errs)
	}
}

// fakeLoader is a test AttachmentLoader returning a fixed attachment slice,
// letting multimodal content tests inject image/text attachments without DB.
type fakeLoader struct{ atts []Attachment }

func (f fakeLoader) Load([]int64, uint64) ([]Attachment, error) { return f.atts, nil }

func TestEngine_OpenAI_WithImageAttachment(t *testing.T) {
	var capturedBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		capturedBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f := w.(http.Flusher)
		sseLine(w, "data: [DONE]")
		sseLine(w, "")
		f.Flush()
	}))
	t.Cleanup(upstream.Close)

	db := openLocalTestDB(t)
	engine := NewEngine(
		stubProfile{protocol: protocolOpenAI, baseURL: upstream.URL, modelID: "llava"},
		db,
		fakeLoader{atts: []Attachment{{Kind: "image", MimeType: "image/png", Base64: "BASE64PNG"}}},
		nil,
	)
	dst := &memSSEWriter{}
	err := engine.Chat(context.Background(), cloudproxy.ChatRequest{
		ThreadID: 1, TurnUUID: engineTestTurnUUID, UID: engineTestUID,
		UserText: "describe this", FileIDs: []int64{1},
	}, dst)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !strings.Contains(capturedBody, `"image_url"`) {
		t.Fatalf("request body missing image_url: %s", capturedBody)
	}
	if !strings.Contains(capturedBody, "data:image/png;base64,BASE64PNG") {
		t.Fatalf("request body missing base64 data url: %s", capturedBody)
	}
	if !strings.Contains(capturedBody, `"type":"text"`) {
		t.Fatalf("request body missing text part: %s", capturedBody)
	}
}

func TestEngine_Anthropic_WithAttachment(t *testing.T) {
	var capturedBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		capturedBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f := w.(http.Flusher)
		sseLine(w, "event: message_stop")
		sseLine(w, "{}")
		sseLine(w, "")
		f.Flush()
	}))
	t.Cleanup(upstream.Close)

	db := openLocalTestDB(t)
	engine := NewEngine(
		stubProfile{protocol: protocolAnthropic, baseURL: upstream.URL, modelID: "claude"},
		db,
		fakeLoader{atts: []Attachment{
			{Kind: "text", Text: "DOC CONTENT"},
			{Kind: "image", MimeType: "image/png", Base64: "BB"},
		}},
		nil,
	)
	dst := &memSSEWriter{}
	err := engine.Chat(context.Background(), cloudproxy.ChatRequest{
		ThreadID: 1, TurnUUID: engineTestTurnUUID, UID: engineTestUID,
		UserText: "summarize", FileIDs: []int64{1, 2},
	}, dst)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !strings.Contains(capturedBody, `"type":"image"`) {
		t.Fatalf("body missing image part: %s", capturedBody)
	}
	if !strings.Contains(capturedBody, "DOC CONTENT") {
		t.Fatalf("body missing extracted doc text: %s", capturedBody)
	}
	if !strings.Contains(capturedBody, "media_type") {
		t.Fatalf("body missing anthropic media_type: %s", capturedBody)
	}
}

func TestEngine_Keyless_Ollama(t *testing.T) {
	// apiKey 空（如 Ollama 无 key）→ 不设 Authorization，正常工作。
	var sawAuth bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			sawAuth = true
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f := w.(http.Flusher)
		sseLine(w, `data: {"choices":[{"delta":{"content":"ok"}}]}`)
		sseLine(w, "")
		f.Flush()
		sseLine(w, "data: [DONE]")
		sseLine(w, "")
		f.Flush()
	}))
	t.Cleanup(upstream.Close)

	engine, _, dst := newTestEngine(t, stubProfile{
		protocol: protocolOpenAI, baseURL: upstream.URL, modelID: engineTestModel, apiKey: "",
	})
	err := engine.Chat(context.Background(), cloudproxy.ChatRequest{
		ThreadID: 1, TurnUUID: engineTestTurnUUID, UID: engineTestUID, UserText: "hi",
	}, dst)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if sawAuth {
		t.Fatal("keyless profile must not send Authorization header")
	}
	frames, errs := dst.snapshot()
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %+v", errs)
	}
	assertCleanDone(t, frames)
}

// --- Conversation history --------------------------------------------------
// The local route used to send only the current prompt: every turn reached a
// model that had never seen the conversation it was allegedly continuing.

func seedHistoryRow(t *testing.T, db *gorm.DB, threadID uint64, userText, aiText, idemKey string) {
	t.Helper()
	if err := db.Exec(
		`INSERT INTO w_workagent_message (uid, uuid, thread_id, user_text, ai_text, message_idempotency_key)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		engineTestUID, "m-"+idemKey+userText[:min(8, len(userText))], threadID, userText, aiText, idemKey,
	).Error; err != nil {
		t.Fatalf("seed history: %v", err)
	}
}

// capturedMessages decodes the upstream request body's messages array.
func capturedMessages(t *testing.T, body string) []map[string]any {
	t.Helper()
	var payload struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("upstream body is not JSON: %v (%s)", err, body)
	}
	return payload.Messages
}

func TestEngine_SendsConversationHistoryInOrder(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		sseLine(w, "data: [DONE]")
		sseLine(w, "")
	}))
	t.Cleanup(upstream.Close)

	db := openLocalTestDB(t)
	seedHistoryRow(t, db, 1, "What were Q3 revenues?", "Revenue was 4.2M.", "key-1")
	seedHistoryRow(t, db, 1, "And the margin?", "Margin was 31%.", "key-2")
	// An interrupted exchange: answered nothing. Including it would put two
	// user messages in a row, which the Anthropic protocol rejects.
	seedHistoryRow(t, db, 1, "This one never got an answer", "", "key-3")
	// Another thread's conversation must not leak in.
	seedHistoryRow(t, db, 2, "Unrelated thread", "Unrelated answer", "key-4")

	engine := NewEngine(
		stubProfile{protocol: protocolOpenAI, baseURL: upstream.URL, modelID: engineTestModel},
		db, nil, nil,
	)
	if err := engine.Chat(context.Background(), cloudproxy.ChatRequest{
		ThreadID: 1, ThreadUUID: "thr_hist", TurnUUID: engineTestTurnUUID,
		UID: engineTestUID, UserText: "Expand on the margin.", ChatMode: "general",
	}, &memSSEWriter{}); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	messages := capturedMessages(t, gotBody)
	var texts []string
	for _, m := range messages {
		if s, ok := m["content"].(string); ok {
			texts = append(texts, m["role"].(string)+": "+s)
		}
	}
	want := []string{
		"user: What were Q3 revenues?",
		"assistant: Revenue was 4.2M.",
		"user: And the margin?",
		"assistant: Margin was 31%.",
		"user: Expand on the margin.",
	}
	if len(texts) != len(want) {
		t.Fatalf("messages = %v, want %v", texts, want)
	}
	for i := range want {
		if texts[i] != want[i] {
			t.Errorf("message[%d] = %q, want %q", i, texts[i], want[i])
		}
	}
	if strings.Contains(gotBody, "Unrelated") {
		t.Error("another thread's conversation leaked into this one")
	}
	if strings.Contains(gotBody, "never got an answer") {
		t.Error("an unanswered exchange must not ride as history")
	}
}

// A replay re-runs a turn whose row already exists under the same idempotency
// key. Sending that row back as history gives the model its own question
// twice.
func TestEngine_ReplayExcludesTheCurrentTurnFromHistory(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		sseLine(w, "data: [DONE]")
		sseLine(w, "")
	}))
	t.Cleanup(upstream.Close)

	db := openLocalTestDB(t)
	requestID, err := cloudproxy.DesktopTurnRequestID(engineTestTurnUUID)
	if err != nil {
		t.Fatal(err)
	}
	// The interrupted attempt's own row, plus one real prior exchange.
	seedHistoryRow(t, db, 1, "Tell me about Q3", "partial answer that got cut", requestID)
	seedHistoryRow(t, db, 1, "Earlier question", "Earlier answer", "key-prior")

	engine := NewEngine(
		stubProfile{protocol: protocolOpenAI, baseURL: upstream.URL, modelID: engineTestModel},
		db, nil, nil,
	)
	if err := engine.Chat(context.Background(), cloudproxy.ChatRequest{
		ThreadID: 1, ThreadUUID: "thr_replay", TurnUUID: engineTestTurnUUID,
		UID: engineTestUID, UserText: "Tell me about Q3", ChatMode: "general",
	}, &memSSEWriter{}); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if strings.Contains(gotBody, "partial answer that got cut") {
		t.Error("the replayed turn's own row rode along as history")
	}
	if !strings.Contains(gotBody, "Earlier answer") {
		t.Error("real prior history must still be present on a replay")
	}
}

// The budget drops the OLDEST exchanges. Dropping the newest would forget
// exactly the part of the conversation the user is continuing.
func TestLoadThreadHistory_BudgetDropsOldestFirst(t *testing.T) {
	db := openLocalTestDB(t)
	big := strings.Repeat("x", maxHistoryChars/2)
	seedHistoryRow(t, db, 1, "oldest question", big, "key-old")
	seedHistoryRow(t, db, 1, "middle question", big, "key-mid")
	seedHistoryRow(t, db, 1, "newest question", "newest answer", "key-new")

	history, err := LoadThreadHistory(db, engineTestUID, 1, "none")
	if err != nil {
		t.Fatal(err)
	}
	var joined strings.Builder
	for _, m := range history {
		joined.WriteString(m.Text)
		joined.WriteString("\n")
	}
	if !strings.Contains(joined.String(), "newest question") {
		t.Error("the newest exchange fell off; the budget must trim from the old end")
	}
	if strings.Contains(joined.String(), "oldest question") {
		t.Error("the oldest exchange survived a budget that cannot hold all three")
	}
	if len(history)%2 != 0 {
		t.Errorf("history must be whole pairs, got %d messages", len(history))
	}
	if len(history) > 0 && history[0].Role != "user" {
		t.Errorf("history must open with a user message, got %q", history[0].Role)
	}
}

// Phase 0.3a regression (local parser): the SSE spec allows `data:` with no
// space after the colon. The local parser already handled it — this pins the
// behavior so the two parsers cannot diverge again.
func TestEngine_OpenAI_AcceptsNoSpaceDataFrames(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f := w.(http.Flusher)
		sseLine(w, `data:{"choices":[{"delta":{"content":"你好"}}]}`)
		sseLine(w, "")
		f.Flush()
		sseLine(w, "data:[DONE]")
		sseLine(w, "")
		f.Flush()
	}))
	t.Cleanup(upstream.Close)

	engine, _, dst := newTestEngine(t, stubProfile{
		protocol: protocolOpenAI, baseURL: upstream.URL, modelID: engineTestModel,
	})
	if err := engine.Chat(context.Background(), cloudproxy.ChatRequest{
		ThreadID: 1, ThreadUUID: "thr_nospace", TurnUUID: engineTestTurnUUID,
		UID: engineTestUID, UserText: "hi", ChatMode: "general",
	}, dst); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	frames, errs := dst.snapshot()
	if len(errs) != 0 {
		t.Fatalf("unexpected proxy errors: %+v", errs)
	}
	var got strings.Builder
	for _, f := range frames {
		if f.Type == "text_delta" {
			got.WriteString(extractDeltaText(f.Data))
		}
	}
	if got.String() != "你好" {
		t.Fatalf("delta text = %q, want %q (no-space data frames must not be dropped)", got.String(), "你好")
	}
	assertCleanDone(t, frames)
}

// Phase 0.3b: a local upstream that goes silent mid-turn is force-closed by
// the idle watchdog instead of hanging the turn forever, and the failure is
// surfaced retryable with the cache row finalized partial.
func TestEngine_OpenAI_IdleUpstreamIsCutRetryable(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f := w.(http.Flusher)
		sseLine(w, `data: {"choices":[{"delta":{"content":"partial"}}]}`)
		sseLine(w, "")
		f.Flush()
		// Silence forever; the watchdog's body close cancels this context.
		<-r.Context().Done()
	}))
	t.Cleanup(upstream.Close)

	engine, db, dst := newTestEngine(t, stubProfile{
		protocol: protocolOpenAI, baseURL: upstream.URL, modelID: engineTestModel,
	})
	engine.idleTimeout = 100 * time.Millisecond

	done := make(chan error, 1)
	go func() {
		done <- engine.Chat(context.Background(), cloudproxy.ChatRequest{
			ThreadID: 1, ThreadUUID: "thr_idle", TurnUUID: engineTestTurnUUID,
			UID: engineTestUID, UserText: "hello?", ChatMode: "general",
		}, dst)
	}()

	var err error
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Chat still blocked long after the idle timeout — half-open upstream hangs the turn again")
	}
	if err == nil {
		t.Fatal("Chat returned nil for a stream with no terminal frame")
	}
	if !errors.Is(err, cloudproxy.ErrSSEUpstreamIdle) {
		t.Errorf("err = %v, want it to wrap cloudproxy.ErrSSEUpstreamIdle", err)
	}

	frames, proxyErrors := dst.snapshot()
	var got strings.Builder
	for _, f := range frames {
		if f.Type == "text_delta" {
			got.WriteString(extractDeltaText(f.Data))
		}
	}
	if got.String() != "partial" {
		t.Errorf("pre-stall delta = %q, want %q", got.String(), "partial")
	}
	if len(proxyErrors) != 1 || !proxyErrors[0].Retryable {
		t.Errorf("want exactly one retryable proxy_error, got %+v", proxyErrors)
	}
	if state, _ := cacheRow(t, db); state != "partial" {
		t.Errorf("cache streaming_state = %q, want partial", state)
	}
}

// Phase 0.5 rune budget: the retrieval budget counts what the model reads,
// not UTF-8 bytes — Chinese text used to get a third of the capacity. And an
// oversized chunk is skipped (continue), not a stopping point (break): it
// must not evict smaller candidates ranked behind it.
func TestPrependKnowledgeContext_RunesNotBytesAndSkipsOversized(t *testing.T) {
	chinese := strings.Repeat("知", 600)                        // 1800 bytes, 600 runes: fits ONLY under rune counting
	oversized := strings.Repeat("大", maxRetrievalContextChars) // over budget on its own
	tail := "结尾小块"

	text, used := PrependKnowledgeContext("问题", []RetrievedSource{
		{Kind: "file", Label: "cn.md", Text: chinese},
		{Kind: "file", Label: "huge.md", Text: oversized},
		{Kind: "file", Label: "tail.md", Text: tail},
	})
	if len(used) != 2 || used[0].Label != "cn.md" || used[1].Label != "tail.md" {
		t.Fatalf("injected = %+v, want cn.md (rune-fits) and tail.md (survives the oversized skip)", used)
	}
	if !strings.Contains(text, chinese) || !strings.Contains(text, tail) {
		t.Error("kept chunks missing from the assembled prompt")
	}
	if strings.Contains(text, oversized) {
		t.Error("the oversized chunk reached the model")
	}
}

// Phase 0.5 rune budget for history: two Chinese pairs of ~10k runes each
// (~30k bytes each) both fit a 24k-rune budget. Byte counting kept only one.
func TestLoadThreadHistory_CountsRunesNotBytes(t *testing.T) {
	db := openLocalTestDB(t)
	cn := strings.Repeat("史", 10_000)
	seedHistoryRow(t, db, 1, "第一问", cn, "key-1")
	seedHistoryRow(t, db, 1, "第二问", cn, "key-2")

	history, err := LoadThreadHistory(db, engineTestUID, 1, "none")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 4 {
		t.Fatalf("history length = %d, want 4 (both pairs fit a rune-counted budget)", len(history))
	}
}
