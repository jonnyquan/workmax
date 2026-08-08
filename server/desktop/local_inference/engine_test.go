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
	return NewEngine(profile, db, nil), db, &memSSEWriter{}
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
