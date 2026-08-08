//go:build desktop

package cloud_proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
)

// memSSEWriter is a test SSEWriter that records every frame
// in-memory so assertions can inspect what the renderer would see.
type memSSEWriter struct {
	mu         sync.Mutex
	frames     []SSEEvent
	errors     []ProxyError
	keepalives int
	writeErr   error // injected: WriteEvent returns this if non-nil
}

type notifyingSSEWriter struct {
	SSEWriter
	once         sync.Once
	eventWritten chan struct{}
}

type replacingSessionOnDoneWriter struct {
	SSEWriter
	replace func() error
	once    sync.Once
	err     error
}

func (writer *replacingSessionOnDoneWriter) WriteEvent(event SSEEvent) error {
	if err := writer.SSEWriter.WriteEvent(event); err != nil {
		return err
	}
	if logicalSSEEventType(event) == "done" {
		writer.once.Do(func() { writer.err = writer.replace() })
	}
	return nil
}

func (w *notifyingSSEWriter) WriteEvent(ev SSEEvent) error {
	if err := w.SSEWriter.WriteEvent(ev); err != nil {
		return err
	}
	w.once.Do(func() { close(w.eventWritten) })
	return nil
}

func (m *memSSEWriter) WriteEvent(ev SSEEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.writeErr != nil {
		return m.writeErr
	}
	m.frames = append(m.frames, ev)
	return nil
}
func (m *memSSEWriter) WriteProxyError(pe ProxyError) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.errors = append(m.errors, pe)
	return nil
}
func (m *memSSEWriter) WriteKeepalive() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keepalives++
	return nil
}
func (m *memSSEWriter) snapshot() ([]SSEEvent, []ProxyError) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f := append([]SSEEvent(nil), m.frames...)
	e := append([]ProxyError(nil), m.errors...)
	return f, e
}

// newProxyTestFixture builds a Proxy + DB + token store seeded with a
// fresh non-expired token + a stub upstream that the caller
// configures.
func newProxyTestFixture(t *testing.T, upstreamHandler http.HandlerFunc) (*Proxy, *memSSEWriter, *gorm.DB, *httptest.Server) {
	t.Helper()
	upstream := httptest.NewServer(upstreamHandler)
	t.Cleanup(upstream.Close)

	db := openCacheTestDB(t)

	kc := newMemKeychain()
	store := NewTokenStore(kc)
	// Pre-seed a valid token pair. AccessExpiresAt = now+1h so
	// acquireToken doesn't bother refreshing.
	if err := store.Save(TokenPair{
		AccessToken:      proxyTestAccessToken(proxyTestUID, "fixture"),
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "stub-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}); err != nil {
		t.Fatalf("seed token: %v", err)
	}

	client := NewClient(upstream.URL)
	client.HTTPClient = upstream.Client()
	proxy := NewProxy(client, store, db)
	proxy.HTTPClient = upstream.Client()

	return proxy, &memSSEWriter{}, db, upstream
}

// memKeychain is a process-local Keychain stand-in for tests.
type memKeychain struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemKeychain() *memKeychain            { return &memKeychain{data: map[string][]byte{}} }
func (m *memKeychain) key(s, a string) string { return s + "\x00" + a }
func (m *memKeychain) Read(s, a string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v, ok := m.data[m.key(s, a)]; ok {
		return append([]byte(nil), v...), nil
	}
	return nil, ErrKeychainNoEntry
}
func (m *memKeychain) Write(s, a string, v []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[m.key(s, a)] = append([]byte(nil), v...)
	return nil
}
func (m *memKeychain) Delete(s, a string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, m.key(s, a))
	return nil
}

func sseLine(w http.ResponseWriter, line string) {
	io.WriteString(w, line+"\n")
}

const (
	proxyTestUID      = uint64(42)
	proxyTestTurnUUID = "de305d54-75b4-431b-adb2-eb6b9e546014"
)

func proxyTestAccessToken(uid uint64, marker string) string {
	return mintTestJWT([]byte(fmt.Sprintf(`{"Id":%d,"marker":%q}`, uid, marker)))
}

// TestProxy_Chat_HappyPath: upstream streams two text events + done.
// Renderer should see all three frames + the cache row should land
// state=complete with the concatenated text.
func TestProxy_Chat_HappyPath(t *testing.T) {
	proxy, dst, db, _ := newProxyTestFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+proxyTestAccessToken(proxyTestUID, "fixture") {
			t.Errorf("upstream auth header: got %q", got)
		}
		if r.Header.Get("X-WorkMax-Client") != "desktop" {
			t.Errorf("missing X-WorkMax-Client header")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		// Two text events + one done.
		sseLine(w, "event: text")
		sseLine(w, `data: {"text":"Hello "}`)
		sseLine(w, "")
		flusher.Flush()
		sseLine(w, "event: text")
		sseLine(w, `data: {"text":"world"}`)
		sseLine(w, "")
		flusher.Flush()
		sseLine(w, "event: done")
		sseLine(w, `data: {}`)
		sseLine(w, "")
		flusher.Flush()
	})

	err := proxy.Chat(context.Background(), ChatRequest{
		ThreadID:   1,
		ThreadUUID: "thr_1",
		TurnUUID:   proxyTestTurnUUID,
		UID:        proxyTestUID,
		UserText:   "say hi",
		ChatMode:   "ppt",
		Body:       []byte(`{"text":"say hi"}`),
	}, dst)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	frames, errs := dst.snapshot()
	if len(errs) > 0 {
		t.Errorf("unexpected proxy errors: %+v", errs)
	}
	if len(frames) != 3 {
		t.Fatalf("frame count: got %d, want 3 (%+v)", len(frames), frames)
	}
	for i, want := range []string{"text", "text", "done"} {
		if frames[i].Type != want {
			t.Errorf("frame[%d].Type = %q, want %q", i, frames[i].Type, want)
		}
	}

	// Verify cache row state.
	var state, aiText, messageRequestID string
	row := db.Raw(`SELECT streaming_state, ai_text, message_idempotency_key FROM w_workagent_message LIMIT 1`).Row()
	if err := row.Scan(&state, &aiText, &messageRequestID); err != nil {
		t.Fatalf("scan row: %v", err)
	}
	if state != "complete" {
		t.Errorf("state: got %q", state)
	}
	if aiText != "Hello world" {
		t.Errorf("ai_text: got %q", aiText)
	}
	if messageRequestID != "desktop-turn:"+proxyTestTurnUUID {
		t.Errorf("message_idempotency_key: got %q", messageRequestID)
	}
}

func TestProxy_Chat_TerminalReplayReusesStableCacheRow(t *testing.T) {
	var calls int
	proxy, dst, db, _ := newProxyTestFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if calls == 1 {
			sseLine(w, "event: text")
			sseLine(w, `data: {"text":"original answer"}`)
			sseLine(w, "")
			sseLine(w, "event: done")
			sseLine(w, `data: {}`)
			sseLine(w, "")
		} else {
			sseLine(w, `data: {"type":"done","result":{"type":"result","subtype":"already_processed","is_error":false,"result":"original answer","replayed":true}}`)
			sseLine(w, "")
		}
		w.(http.Flusher).Flush()
	})
	request := ChatRequest{
		ThreadID: 1, ThreadUUID: "thr-replay", TurnUUID: proxyTestTurnUUID,
		UID: proxyTestUID, UserText: "frozen prompt", ChatMode: "ppt", Body: []byte(`{}`),
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := proxy.Chat(context.Background(), request, dst); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
	}
	var count int
	if err := db.Raw(`SELECT COUNT(*) FROM w_workagent_message WHERE message_idempotency_key = ?`, "desktop-turn:"+proxyTestTurnUUID).
		Row().Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("terminal replay cache rows=%d, want 1", count)
	}
	var aiText, state string
	if err := db.Raw(`SELECT ai_text, streaming_state FROM w_workagent_message WHERE message_idempotency_key = ?`, "desktop-turn:"+proxyTestTurnUUID).
		Row().Scan(&aiText, &state); err != nil {
		t.Fatal(err)
	}
	if aiText != "original answer" || state != streamingStateComplete {
		t.Fatalf("terminal replay cache=%q/%q", aiText, state)
	}
}

func TestProxy_Chat_ForwardedDoneWinsSessionEpochRace(t *testing.T) {
	proxy, inner, db, _ := newProxyTestFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		sseLine(w, "event: text")
		sseLine(w, `data: {"text":"committed before replacement"}`)
		sseLine(w, "")
		sseLine(w, "event: done")
		sseLine(w, `data: {}`)
		sseLine(w, "")
		w.(http.Flusher).Flush()
	})
	destination := &replacingSessionOnDoneWriter{
		SSEWriter: inner,
		replace: func() error {
			return proxy.tokenStore.Save(TokenPair{
				AccessToken:      proxyTestAccessToken(99, "replacement-after-done"),
				AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
				RefreshToken:     "replacement-refresh",
				RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
			})
		},
	}
	err := proxy.Chat(context.Background(), ChatRequest{
		ThreadID: 1, ThreadUUID: "thr-done-race", TurnUUID: proxyTestTurnUUID,
		UID: proxyTestUID, UserText: "frozen", ChatMode: "ppt", Body: []byte(`{}`),
	}, destination)
	if destination.err != nil {
		t.Fatalf("replace session: %v", destination.err)
	}
	if err != nil {
		t.Fatalf("already-forwarded done was reinterpreted: %v", err)
	}
	frames, proxyErrors := inner.snapshot()
	if len(frames) != 2 || frames[1].Type != "done" || len(proxyErrors) != 0 {
		t.Fatalf("downstream frames=%+v errors=%+v", frames, proxyErrors)
	}
	var state, aiText string
	if err := db.Raw(`SELECT streaming_state, ai_text FROM w_workagent_message WHERE message_idempotency_key = ?`, "desktop-turn:"+proxyTestTurnUUID).
		Row().Scan(&state, &aiText); err != nil {
		t.Fatal(err)
	}
	if state != streamingStateComplete || aiText != "committed before replacement" {
		t.Fatalf("done-race cache=%q/%q", state, aiText)
	}
}

func TestProxy_Chat_DataEnvelopeCachesBlockAndStopsAtDone(t *testing.T) {
	proxy, dst, db, _ := newProxyTestFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		sseLine(w, `data: {"type":"block","message_id":"msg-1","block_index":0,"block":{"type":"text","text":"Hello from block"}}`)
		sseLine(w, "")
		flusher.Flush()
		sseLine(w, `data: {"type":"done","message_id":"msg-1"}`)
		sseLine(w, "")
		flusher.Flush()
		// A buggy upstream frame after done must neither reach the Renderer
		// nor contaminate the cached completed answer.
		sseLine(w, `data: {"type":"block","block":{"type":"text","text":"must-not-pass"}}`)
		sseLine(w, "")
		flusher.Flush()
	})

	err := proxy.Chat(context.Background(), ChatRequest{
		ThreadID:   1,
		ThreadUUID: "thr_1",
		TurnUUID:   proxyTestTurnUUID,
		UID:        proxyTestUID,
		UserText:   "say hi",
		ChatMode:   "ppt",
		Body:       []byte(`{}`),
	}, dst)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	frames, proxyErrors := dst.snapshot()
	if len(proxyErrors) != 0 {
		t.Fatalf("unexpected proxy errors: %+v", proxyErrors)
	}
	if len(frames) != 2 {
		t.Fatalf("data-envelope frame count: got %d, want block + done only (%+v)", len(frames), frames)
	}
	for i, wantType := range []string{"block", "done"} {
		if frames[i].Type != "" {
			t.Fatalf("frame[%d] gained an event field instead of preserving data-only SSE: %+v", i, frames[i])
		}
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(frames[i].Data), &envelope); err != nil {
			t.Fatalf("decode frame[%d]: %v", i, err)
		}
		if envelope.Type != wantType {
			t.Fatalf("frame[%d] envelope type = %q, want %q", i, envelope.Type, wantType)
		}
	}

	var state, aiText string
	if err := db.Raw(`SELECT streaming_state, ai_text FROM w_workagent_message LIMIT 1`).Row().Scan(&state, &aiText); err != nil {
		t.Fatalf("scan data-envelope cache row: %v", err)
	}
	if state != streamingStateComplete || aiText != "Hello from block" {
		t.Fatalf("data-envelope cache: state=%q ai_text=%q", state, aiText)
	}
}

func TestProxy_Chat_DataEnvelopeEOFBeforeDoneMarksPartial(t *testing.T) {
	proxy, dst, db, _ := newProxyTestFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		sseLine(w, `data: {"type":"block","block":{"type":"text","text":"partial answer"}}`)
		sseLine(w, "")
		flusher.Flush()
		// Handler return closes the body cleanly, but there is no done event.
	})

	err := proxy.Chat(context.Background(), ChatRequest{
		ThreadID:   1,
		ThreadUUID: "thr_1",
		TurnUUID:   proxyTestTurnUUID,
		UID:        proxyTestUID,
		UserText:   "say hi",
		ChatMode:   "ppt",
		Body:       []byte(`{}`),
	}, dst)
	if err == nil {
		t.Fatal("clean EOF without done should be classified as a truncated stream")
	}
	frames, proxyErrors := dst.snapshot()
	if len(frames) != 1 || logicalSSEEventType(frames[0]) != "block" {
		t.Fatalf("partial data-envelope frames = %+v, want one block", frames)
	}
	if len(proxyErrors) != 1 || proxyErrors[0].Kind != KindServiceUnavailable || !proxyErrors[0].Retryable {
		t.Fatalf("truncated stream proxy errors = %+v", proxyErrors)
	}

	var state, aiText string
	if err := db.Raw(`SELECT streaming_state, ai_text FROM w_workagent_message LIMIT 1`).Row().Scan(&state, &aiText); err != nil {
		t.Fatalf("scan truncated cache row: %v", err)
	}
	if state != streamingStatePartial || aiText != "partial answer" {
		t.Fatalf("truncated cache: state=%q ai_text=%q", state, aiText)
	}
}

func TestPipeUpstream_AcceptsCRLFSSEFrames(t *testing.T) {
	dst := &memSSEWriter{}
	err := PipeUpstream(
		context.Background(),
		strings.NewReader("event: text\r\ndata: {\"text\":\"Hello\"}\r\n\r\nevent: done\r\ndata: {}\r\n\r\n"),
		dst,
		nil,
	)
	if err != nil {
		t.Fatalf("PipeUpstream: %v", err)
	}
	frames, errs := dst.snapshot()
	if len(errs) > 0 {
		t.Fatalf("unexpected proxy errors: %+v", errs)
	}
	if len(frames) != 2 {
		t.Fatalf("frame count: got %d, want 2 (%+v)", len(frames), frames)
	}
	if frames[0].Type != "text" || frames[0].Data != `{"text":"Hello"}` {
		t.Fatalf("first frame = %+v", frames[0])
	}
	if frames[1].Type != "done" || frames[1].Data != `{}` {
		t.Fatalf("second frame = %+v", frames[1])
	}
}

// TestProxy_Chat_UpstreamSpamRejected: upstream returns 500 → no
// frames forwarded, proxy_error emitted, cache row marked partial.
func TestProxy_Chat_Upstream500(t *testing.T) {
	proxy, dst, db, _ := newProxyTestFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":"upstream broke"}`)
	})
	err := proxy.Chat(context.Background(), ChatRequest{
		ThreadID:   1,
		ThreadUUID: "thr_1",
		TurnUUID:   proxyTestTurnUUID,
		UID:        proxyTestUID,
		UserText:   "say hi",
		Body:       []byte(`{}`),
	}, dst)
	if err == nil {
		t.Fatal("expected error")
	}
	frames, errs := dst.snapshot()
	if len(frames) != 0 {
		t.Errorf("upstream 5xx should send no SSE frames, got %d", len(frames))
	}
	if len(errs) != 1 || errs[0].Kind != KindServiceUnavailable {
		t.Errorf("want one service_unavailable, got %+v", errs)
	}
	// No row inserted because cache_writer lazy-INSERTs on first event.
	var count int64
	db.Raw(`SELECT count(*) FROM w_workagent_message`).Row().Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 rows (no events streamed), got %d", count)
	}
}

// TestProxy_Chat_Upstream401Recovery: first upstream call returns 401.
// We exercise the auto-refresh path: rotate, retry, succeed.
func TestProxy_Chat_Upstream401AutoRefresh(t *testing.T) {
	// Track call sequence: first call returns 401, second call returns
	// 200 SSE. Token-exchange endpoint returns the refreshed pair.
	var (
		chatCalls      int
		tokenCalls     int
		chatRequestIDs []string
	)
	staleAccess := proxyTestAccessToken(proxyTestUID, "stale")
	rotatedAccess := proxyTestAccessToken(proxyTestUID, "rotated")
	mux := http.NewServeMux()
	mux.HandleFunc("/api/desktop/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		tokenCalls++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tokenExchangeResponse{
			AccessToken:      rotatedAccess,
			TokenType:        "Bearer",
			ExpiresIn:        3600,
			RefreshToken:     "rotated-refresh",
			RefreshExpiresIn: 86400,
			Scope:            "workagent",
		})
	})
	mux.HandleFunc("/api/work-agent/chat/agent", func(w http.ResponseWriter, r *http.Request) {
		chatCalls++
		chatRequestIDs = append(chatRequestIDs, r.Header.Get("X-Agent-Request-Id"))
		if chatCalls == 1 {
			// First call: stale token → 401
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":"invalid_token"}`)
			return
		}
		// Second call: must use rotated token.
		if r.Header.Get("Authorization") != "Bearer "+rotatedAccess {
			t.Errorf("retry used wrong token: %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		sseLine(w, "event: text")
		sseLine(w, `data: {"text":"Recovered"}`)
		sseLine(w, "")
		flusher.Flush()
		sseLine(w, "event: done")
		sseLine(w, `data: {}`)
		sseLine(w, "")
		flusher.Flush()
	})
	upstream := httptest.NewServer(mux)
	t.Cleanup(upstream.Close)

	db := openCacheTestDB(t)
	store := NewTokenStore(newMemKeychain())
	// Stale token saved 10s ago (>5s threshold so tryAuthRecover
	// proceeds).
	if err := store.Save(TokenPair{
		AccessToken:      staleAccess,
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "stale-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
		SavedAt:          time.Now().UTC().Add(-10 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}

	client := NewClient(upstream.URL)
	client.HTTPClient = upstream.Client()
	proxy := NewProxy(client, store, db)
	proxy.HTTPClient = upstream.Client()

	dst := &memSSEWriter{}
	err := proxy.Chat(context.Background(), ChatRequest{
		ThreadID: 1, TurnUUID: proxyTestTurnUUID, UID: proxyTestUID, UserText: "x", Body: []byte(`{}`),
	}, dst)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if chatCalls != 2 {
		t.Errorf("chat calls: got %d, want 2", chatCalls)
	}
	if len(chatRequestIDs) != 2 || chatRequestIDs[0] != "desktop-turn:"+proxyTestTurnUUID || chatRequestIDs[1] != chatRequestIDs[0] {
		t.Errorf("401 retry changed X-Agent-Request-Id: %q", chatRequestIDs)
	}
	if tokenCalls != 1 {
		t.Errorf("token calls: got %d, want 1", tokenCalls)
	}
	frames, errs := dst.snapshot()
	if len(errs) != 0 {
		t.Errorf("unexpected proxy_error during recovery: %+v", errs)
	}
	if len(frames) != 2 || frames[0].Type != "text" || frames[1].Type != "done" {
		t.Errorf("expected recovered text + done frames, got %+v", frames)
	}
	// Token store should now hold the rotated tokens.
	pair, _ := store.Get()
	if pair.AccessToken != rotatedAccess {
		t.Errorf("token not rotated: %q", pair.AccessToken)
	}
	var state, aiText string
	row := db.Raw(`SELECT streaming_state, ai_text FROM w_workagent_message LIMIT 1`).Row()
	if err := row.Scan(&state, &aiText); err != nil {
		t.Fatalf("scan recovered cache row: %v", err)
	}
	if state != streamingStateComplete {
		t.Errorf("recovered cache state: got %q, want %q", state, streamingStateComplete)
	}
	if aiText != "Recovered" {
		t.Errorf("recovered cache ai_text: got %q", aiText)
	}
}

func TestProxy_Chat_RejectsDifferentInitialSessionSubject(t *testing.T) {
	var chatCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chatCalls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(upstream.Close)

	db := openCacheTestDB(t)
	store := NewTokenStore(newMemKeychain())
	if err := store.Save(TokenPair{
		AccessToken:      proxyTestAccessToken(proxyTestUID+1, "different-account"),
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "different-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}); err != nil {
		t.Fatalf("seed different session: %v", err)
	}
	client := NewClient(upstream.URL)
	client.HTTPClient = upstream.Client()
	proxy := NewProxy(client, store, db)
	proxy.HTTPClient = upstream.Client()
	dst := &memSSEWriter{}

	err := proxy.Chat(context.Background(), ChatRequest{
		ThreadID: 1,
		TurnUUID: proxyTestTurnUUID,
		UID:      proxyTestUID,
		UserText: "must not cross accounts",
		Body:     []byte(`{}`),
	}, dst)
	if !errors.Is(err, ErrChatSessionChanged) {
		t.Fatalf("Chat error = %v, want ErrChatSessionChanged", err)
	}
	if chatCalls != 0 {
		t.Fatalf("cross-account initial request reached cloud %d time(s)", chatCalls)
	}
	frames, proxyErrors := dst.snapshot()
	if len(frames) != 0 || len(proxyErrors) != 1 || proxyErrors[0].Kind != KindSessionChanged {
		t.Fatalf("cross-account result: frames=%+v errors=%+v", frames, proxyErrors)
	}
	var cached int64
	if err := db.Raw(`SELECT COUNT(*) FROM w_workagent_message`).Row().Scan(&cached); err != nil {
		t.Fatalf("count cache rows: %v", err)
	}
	if cached != 0 {
		t.Fatalf("cross-account initial request wrote %d cache row(s)", cached)
	}
}

func TestProxy_Chat_ProactiveRefreshSessionTransitionIsSessionChanged(t *testing.T) {
	tests := []struct {
		name       string
		transition func(*TokenStore) error
	}{
		{
			name: "same UID login replacement",
			transition: func(store *TokenStore) error {
				return store.Save(TokenPair{
					AccessToken:      proxyTestAccessToken(proxyTestUID, "same-user-new-login"),
					AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
					RefreshToken:     "same-user-new-refresh",
					RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
					Scope:            "workagent",
				})
			},
		},
		{name: "logout", transition: func(store *TokenStore) error { return store.Clear() }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			refreshStarted := make(chan struct{})
			refreshCanceled := make(chan struct{})
			releaseRefresh := make(chan struct{})
			var refreshStartedOnce sync.Once
			var refreshCanceledOnce sync.Once
			var releaseRefreshOnce sync.Once
			release := func() { releaseRefreshOnce.Do(func() { close(releaseRefresh) }) }
			var tokenCalls, chatCalls int

			mux := http.NewServeMux()
			mux.HandleFunc(CloudRouteOAuthToken, func(w http.ResponseWriter, r *http.Request) {
				tokenCalls++
				if err := r.ParseForm(); err != nil {
					t.Errorf("parse refresh form: %v", err)
				}
				if got := r.Form.Get("refresh_token"); got != "old-refresh" {
					t.Errorf("refresh_token = %q, want old-refresh", got)
				}
				refreshStartedOnce.Do(func() { close(refreshStarted) })
				select {
				case <-r.Context().Done():
					refreshCanceledOnce.Do(func() { close(refreshCanceled) })
				case <-releaseRefresh:
				}
			})
			mux.HandleFunc(CloudRouteChatAgent, func(w http.ResponseWriter, r *http.Request) {
				chatCalls++
				w.WriteHeader(http.StatusInternalServerError)
			})
			upstream := httptest.NewServer(mux)
			t.Cleanup(upstream.Close)
			t.Cleanup(release)

			db := openCacheTestDB(t)
			store := NewTokenStore(newMemKeychain())
			if err := store.Save(TokenPair{
				AccessToken:      proxyTestAccessToken(proxyTestUID, "expiring-old"),
				AccessExpiresAt:  time.Now().UTC().Add(5 * time.Second),
				RefreshToken:     "old-refresh",
				RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
				Scope:            "workagent",
			}); err != nil {
				t.Fatalf("seed old session: %v", err)
			}
			client := NewClient(upstream.URL)
			client.HTTPClient = upstream.Client()
			proxy := NewProxy(client, store, db)
			proxy.HTTPClient = upstream.Client()
			dst := &memSSEWriter{}

			chatDone := make(chan error, 1)
			go func() {
				chatDone <- proxy.Chat(context.Background(), ChatRequest{
					ThreadID: 1,
					TurnUUID: proxyTestTurnUUID,
					UID:      proxyTestUID,
					UserText: "proactive refresh race",
					Body:     []byte(`{}`),
				}, dst)
			}()
			select {
			case <-refreshStarted:
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for proactive refresh")
			}

			if err := test.transition(store); err != nil {
				t.Fatalf("session transition: %v", err)
			}
			select {
			case err := <-chatDone:
				if !errors.Is(err, ErrChatSessionChanged) {
					t.Fatalf("Chat error = %v, want ErrChatSessionChanged", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for session-fenced Chat")
			}
			select {
			case <-refreshCanceled:
			case <-time.After(2 * time.Second):
				t.Fatal("session transition did not cancel proactive refresh HTTP")
			}

			frames, proxyErrors := dst.snapshot()
			if len(frames) != 0 || len(proxyErrors) != 1 || proxyErrors[0].Kind != KindSessionChanged {
				t.Fatalf("session transition result: frames=%+v errors=%+v", frames, proxyErrors)
			}
			if tokenCalls != 1 || chatCalls != 0 {
				t.Fatalf("cloud calls: token=%d chat=%d, want 1/0", tokenCalls, chatCalls)
			}
			var cached int64
			if err := db.Raw(`SELECT COUNT(*) FROM w_workagent_message`).Row().Scan(&cached); err != nil {
				t.Fatalf("count cache rows: %v", err)
			}
			if cached != 0 {
				t.Fatalf("retired proactive-refresh request wrote %d cache row(s)", cached)
			}
		})
	}
}

func TestProxy_Chat_NonOKResponseSessionTransitionIsSessionChanged(t *testing.T) {
	tests := []struct {
		name       string
		transition func(*TokenStore) error
	}{
		{
			name: "same UID login replacement",
			transition: func(store *TokenStore) error {
				return store.Save(TokenPair{
					AccessToken:      proxyTestAccessToken(proxyTestUID, "same-user-new-login"),
					AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
					RefreshToken:     "same-user-new-refresh",
					RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
					Scope:            "workagent",
				})
			},
		},
		{name: "logout", transition: func(store *TokenStore) error { return store.Clear() }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			responseStarted := make(chan struct{})
			upstreamCanceled := make(chan struct{})
			releaseUpstream := make(chan struct{})
			var responseStartedOnce sync.Once
			var upstreamCanceledOnce sync.Once
			var releaseUpstreamOnce sync.Once
			release := func() { releaseUpstreamOnce.Do(func() { close(releaseUpstream) }) }

			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				responseStartedOnce.Do(func() { close(responseStarted) })
				select {
				case <-r.Context().Done():
					upstreamCanceledOnce.Do(func() { close(upstreamCanceled) })
				case <-releaseUpstream:
				}
			}))
			t.Cleanup(upstream.Close)
			t.Cleanup(release)

			db := openCacheTestDB(t)
			store := NewTokenStore(newMemKeychain())
			if err := store.Save(TokenPair{
				AccessToken:      proxyTestAccessToken(proxyTestUID, "current"),
				AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
				RefreshToken:     "current-refresh",
				RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
				Scope:            "workagent",
			}); err != nil {
				t.Fatalf("seed session: %v", err)
			}
			client := NewClient(upstream.URL)
			client.HTTPClient = upstream.Client()
			proxy := NewProxy(client, store, db)
			proxy.HTTPClient = upstream.Client()
			dst := &memSSEWriter{}

			chatDone := make(chan error, 1)
			go func() {
				chatDone <- proxy.Chat(context.Background(), ChatRequest{
					ThreadID: 1,
					TurnUUID: proxyTestTurnUUID,
					UID:      proxyTestUID,
					UserText: "non-OK response race",
					Body:     []byte(`{}`),
				}, dst)
			}()
			select {
			case <-responseStarted:
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for non-OK response")
			}

			if err := test.transition(store); err != nil {
				t.Fatalf("session transition: %v", err)
			}
			select {
			case err := <-chatDone:
				if !errors.Is(err, ErrChatSessionChanged) {
					t.Fatalf("Chat error = %v, want ErrChatSessionChanged", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for session-fenced Chat")
			}
			select {
			case <-upstreamCanceled:
			case <-time.After(2 * time.Second):
				t.Fatal("session transition did not cancel non-OK response body")
			}

			frames, proxyErrors := dst.snapshot()
			if len(frames) != 0 || len(proxyErrors) != 1 || proxyErrors[0].Kind != KindSessionChanged {
				t.Fatalf("session transition result: frames=%+v errors=%+v", frames, proxyErrors)
			}
			var cached int64
			if err := db.Raw(`SELECT COUNT(*) FROM w_workagent_message`).Row().Scan(&cached); err != nil {
				t.Fatalf("count cache rows: %v", err)
			}
			if cached != 0 {
				t.Fatalf("retired non-OK request wrote %d cache row(s)", cached)
			}
		})
	}
}

func TestProxy_Chat_401ConcurrentLoginRetiresOldSessionEpoch(t *testing.T) {
	tests := []struct {
		name   string
		newUID uint64
	}{
		{name: "same account re-login retires old request", newUID: proxyTestUID},
		{name: "different account login retires old request", newUID: proxyTestUID + 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var (
				chatCalls          int
				tokenCalls         int
				retryAuthorization string
			)
			refreshStarted := make(chan struct{})
			releaseRefresh := make(chan struct{})
			var refreshStartedOnce sync.Once
			var releaseRefreshOnce sync.Once
			release := func() { releaseRefreshOnce.Do(func() { close(releaseRefresh) }) }
			t.Cleanup(release)

			staleRotatedAccess := proxyTestAccessToken(proxyTestUID, "stale-rotated")
			mux := http.NewServeMux()
			mux.HandleFunc(CloudRouteOAuthToken, func(w http.ResponseWriter, r *http.Request) {
				tokenCalls++
				refreshStartedOnce.Do(func() { close(refreshStarted) })
				select {
				case <-releaseRefresh:
				case <-r.Context().Done():
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(tokenExchangeResponse{
					AccessToken:      staleRotatedAccess,
					TokenType:        "Bearer",
					ExpiresIn:        3600,
					RefreshToken:     "stale-rotated-refresh",
					RefreshExpiresIn: 86400,
					Scope:            "workagent",
				})
			})
			mux.HandleFunc(CloudRouteChatAgent, func(w http.ResponseWriter, r *http.Request) {
				chatCalls++
				if chatCalls == 1 {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = io.WriteString(w, `{"error":"invalid_token"}`)
					return
				}
				retryAuthorization = r.Header.Get("Authorization")
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "event: done\ndata: {}\n\n")
			})
			upstream := httptest.NewServer(mux)
			t.Cleanup(upstream.Close)

			db := openCacheTestDB(t)
			store := NewTokenStore(newMemKeychain())
			if err := store.Save(TokenPair{
				AccessToken:      proxyTestAccessToken(proxyTestUID, "rejected-old"),
				AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
				RefreshToken:     "old-refresh",
				RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
				Scope:            "workagent",
				SavedAt:          time.Now().UTC().Add(-10 * time.Second),
			}); err != nil {
				t.Fatalf("seed old session: %v", err)
			}
			client := NewClient(upstream.URL)
			client.HTTPClient = upstream.Client()
			proxy := NewProxy(client, store, db)
			proxy.HTTPClient = upstream.Client()

			dst := &memSSEWriter{}
			chatDone := make(chan error, 1)
			go func() {
				chatDone <- proxy.Chat(context.Background(), ChatRequest{
					ThreadID: 1,
					TurnUUID: proxyTestTurnUUID,
					UID:      proxyTestUID,
					UserText: "race",
					Body:     []byte(`{}`),
				}, dst)
			}()
			select {
			case <-refreshStarted:
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for forced refresh")
			}

			newLogin := TokenPair{
				AccessToken:      proxyTestAccessToken(test.newUID, "new-login"),
				AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
				RefreshToken:     "new-login-refresh",
				RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
				Scope:            "workagent",
			}
			if err := store.Save(newLogin); err != nil {
				t.Fatalf("new login Save: %v", err)
			}
			release()

			var chatErr error
			select {
			case chatErr = <-chatDone:
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for Chat")
			}
			if !errors.Is(chatErr, ErrChatSessionChanged) {
				t.Fatalf("Chat error = %v, want ErrChatSessionChanged", chatErr)
			}
			if chatCalls != 1 || retryAuthorization != "" {
				t.Fatalf("retired request retried: calls=%d authorization=%q", chatCalls, retryAuthorization)
			}
			frames, proxyErrors := dst.snapshot()
			if len(frames) != 0 || len(proxyErrors) != 1 || proxyErrors[0].Kind != KindSessionChanged {
				t.Fatalf("session replacement result: frames=%+v errors=%+v", frames, proxyErrors)
			}
			var cached int64
			if err := db.Raw(`SELECT COUNT(*) FROM w_workagent_message`).Row().Scan(&cached); err != nil {
				t.Fatalf("count cache rows: %v", err)
			}
			if cached != 0 {
				t.Fatalf("retired request wrote %d cache row(s)", cached)
			}
			if tokenCalls != 1 {
				t.Fatalf("token calls = %d, want 1", tokenCalls)
			}
			saved, err := store.Get()
			if err != nil {
				t.Fatalf("store.Get: %v", err)
			}
			if saved.AccessToken != newLogin.AccessToken || saved.RefreshToken != newLogin.RefreshToken {
				t.Fatalf("stale 401 refresh overwrote new login: %+v", saved)
			}
		})
	}
}

func TestProxy_Chat_SameAccountReloginCancelsInflightSSE(t *testing.T) {
	upstreamCanceled := make(chan struct{})
	proxy, inner, db, _ := newProxyTestFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		sseLine(w, "event: text")
		sseLine(w, `data: {"text":"before replacement"}`)
		sseLine(w, "")
		flusher.Flush()
		<-r.Context().Done()
		close(upstreamCanceled)
	})

	eventWritten := make(chan struct{})
	dst := &notifyingSSEWriter{SSEWriter: inner, eventWritten: eventWritten}
	chatDone := make(chan error, 1)
	go func() {
		chatDone <- proxy.Chat(context.Background(), ChatRequest{
			ThreadID: 1,
			TurnUUID: proxyTestTurnUUID,
			UID:      proxyTestUID,
			UserText: "replace the session while streaming",
			Body:     []byte(`{}`),
		}, dst)
	}()

	select {
	case <-eventWritten:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first SSE event")
	}

	// An unconditional Save represents a new login even when its subject is the
	// same. It must retire the old request instead of migrating that request to
	// the new authorization chain.
	if err := proxy.tokenStore.Save(TokenPair{
		AccessToken:      proxyTestAccessToken(proxyTestUID, "same-user-new-login"),
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "same-user-new-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}); err != nil {
		t.Fatalf("replace session: %v", err)
	}

	select {
	case err := <-chatDone:
		if !errors.Is(err, ErrChatSessionChanged) {
			t.Fatalf("Chat error = %v, want ErrChatSessionChanged", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for session-fenced Chat")
	}
	select {
	case <-upstreamCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("session replacement did not cancel upstream SSE request")
	}

	frames, proxyErrors := inner.snapshot()
	if len(frames) != 1 || frames[0].Type != "text" {
		t.Fatalf("frames after replacement = %+v, want only first text event", frames)
	}
	if len(proxyErrors) != 1 || proxyErrors[0].Kind != KindSessionChanged {
		t.Fatalf("proxy errors after replacement = %+v", proxyErrors)
	}
	var state, aiText string
	if err := db.Raw(`SELECT streaming_state, ai_text FROM w_workagent_message LIMIT 1`).Row().Scan(&state, &aiText); err != nil {
		t.Fatalf("scan partial cache row: %v", err)
	}
	if state != streamingStatePartial || aiText != "before replacement" {
		t.Fatalf("cache after replacement: state=%q ai_text=%q", state, aiText)
	}
}

// TestProxy_Chat_NoSessionReturnsAuthRequired: TokenStore empty →
// auth_required proxy_error, no upstream call.
func TestProxy_Chat_NoSession(t *testing.T) {
	db := openCacheTestDB(t)
	store := NewTokenStore(newMemKeychain())
	client := NewClient("http://does-not-exist")
	proxy := NewProxy(client, store, db)

	dst := &memSSEWriter{}
	err := proxy.Chat(context.Background(), ChatRequest{
		ThreadID: 1, TurnUUID: proxyTestTurnUUID, UID: proxyTestUID, UserText: "x", Body: []byte(`{}`),
	}, dst)
	if err == nil {
		t.Fatal("expected error")
	}
	_, errs := dst.snapshot()
	if len(errs) != 1 || errs[0].Kind != KindAuthRequired {
		t.Errorf("want one auth_required, got %+v", errs)
	}
}

// TestProxy_Chat_RendererDisconnect: downstream Write returns an
// error → relay cancels upstream + returns the error without
// emitting a proxy_error (renderer is already gone).
func TestProxy_Chat_RendererDisconnect(t *testing.T) {
	upstreamClosed := make(chan struct{})
	proxy, _, _, _ := newProxyTestFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		// Send one event then block until client disconnects.
		sseLine(w, "event: text")
		sseLine(w, `data: {"text":"x"}`)
		sseLine(w, "")
		flusher.Flush()
		<-r.Context().Done()
		close(upstreamClosed)
	})

	// Writer that fails on the second WriteEvent — simulates renderer
	// hanging up after the first frame.
	writeCalls := 0
	dst := &memSSEWriter{}
	wrapper := writerThatFailsAfter(dst, &writeCalls, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		// Force-cancel upstream after a short delay so the test doesn't
		// hang if PipeUpstream doesn't notice the downstream failure
		// fast enough (it should).
		time.Sleep(500 * time.Millisecond)
		cancel()
	}()
	_ = proxy.Chat(ctx, ChatRequest{ThreadID: 1, TurnUUID: proxyTestTurnUUID, UID: proxyTestUID, UserText: "x", Body: []byte(`{}`)}, wrapper)

	select {
	case <-upstreamClosed:
		// good — upstream saw context cancellation
	case <-time.After(2 * time.Second):
		t.Error("upstream not cancelled within 2s after downstream failure")
	}
}

// writerThatFailsAfter returns an SSEWriter that delegates to `inner`
// for the first `successCount` calls, then returns a disconnect error.
func writerThatFailsAfter(inner SSEWriter, calls *int, successCount int) SSEWriter {
	return failAfterWriter{inner: inner, calls: calls, success: successCount}
}

type failAfterWriter struct {
	inner   SSEWriter
	calls   *int
	success int
}

func (f failAfterWriter) WriteEvent(ev SSEEvent) error {
	*f.calls++
	if *f.calls > f.success {
		return fmt.Errorf("renderer disconnected")
	}
	return f.inner.WriteEvent(ev)
}
func (f failAfterWriter) WriteProxyError(pe ProxyError) error {
	return f.inner.WriteProxyError(pe)
}
func (f failAfterWriter) WriteKeepalive() error {
	return f.inner.WriteKeepalive()
}

func TestProxy_emitErrorAndClose_ShapesErrorBody(t *testing.T) {
	dst := &memSSEWriter{}
	p := &Proxy{}
	err := p.emitErrorAndClose(dst, ProxyError{
		Kind:    KindRateLimited,
		Message: "slow Authorization: Bearer access-secret Basic bare-basic-secret https://user:pass@example.com/path?refresh_token=refresh-secret&api-key=api-secret",
		Details: map[string]any{
			"upstream_body_prefix": "access_token=body-secret X-Local-Token=local-secret",
			"password":             "password-secret",
			"nested": map[string]any{
				"client_secret": "client-secret",
				"reason":        "secret=generic-secret",
			},
		},
	}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "rate_limited") {
		t.Errorf("error string should mention kind: %q", err.Error())
	}
	for _, secret := range []string{"access-secret", "bare-basic-secret", "user:pass", "refresh-secret", "api-secret", "body-secret", "local-secret", "password-secret", "client-secret", "generic-secret"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("returned error leaked %q: %s", secret, err.Error())
		}
	}
	_, errs := dst.snapshot()
	if len(errs) != 1 || errs[0].Kind != KindRateLimited {
		t.Errorf("dst should have one rate_limited error, got %+v", errs)
	}
	serialized := proxyErrorStringForTest(errs[0])
	for _, secret := range []string{"access-secret", "bare-basic-secret", "user:pass", "refresh-secret", "api-secret", "body-secret", "local-secret", "password-secret", "client-secret", "generic-secret"} {
		if strings.Contains(serialized, secret) {
			t.Fatalf("SSE proxy_error leaked %q: %s", secret, serialized)
		}
	}
}

// TestPipeUpstream_RoundTripsFrames is a focused test on the SSE
// parser independent of HTTP / Token / Chat.
func TestPipeUpstream_RoundTripsFrames(t *testing.T) {
	raw := strings.Join([]string{
		"event: text",
		`data: {"text":"hello"}`,
		"",
		": keepalive",
		"",
		"event: tool_use",
		`data: {"name":"plan"}`,
		"",
		"event: done",
		`data: {}`,
		"",
	}, "\n")
	dst := &memSSEWriter{}
	err := PipeUpstream(context.Background(), strings.NewReader(raw), dst, nil)
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	frames, _ := dst.snapshot()
	if len(frames) != 3 {
		t.Fatalf("want 3 frames (keepalive dropped), got %d (%+v)", len(frames), frames)
	}
	for i, want := range []string{"text", "tool_use", "done"} {
		if frames[i].Type != want {
			t.Errorf("frame[%d].Type = %q, want %q", i, frames[i].Type, want)
		}
	}
}

func TestPipeUpstream_RejectsAggregateOversizedFrame(t *testing.T) {
	var raw strings.Builder
	line := "data: " + strings.Repeat("x", 1024) + "\n"
	for raw.Len()+len(line) <= maxSSEFrameBytes {
		raw.WriteString(line)
	}
	raw.WriteString(line)
	raw.WriteByte('\n')

	dst := &memSSEWriter{}
	err := PipeUpstream(context.Background(), strings.NewReader(raw.String()), dst, nil)
	if !errors.Is(err, ErrSSEFrameTooLarge) {
		t.Fatalf("PipeUpstream error = %v, want ErrSSEFrameTooLarge", err)
	}
	frames, _ := dst.snapshot()
	if len(frames) != 0 {
		t.Fatalf("oversized frame reached renderer: %+v", frames)
	}
}

func TestPipeUpstream_DoneStopsBeforeTrailingFrames(t *testing.T) {
	raw := strings.Join([]string{
		"event: done",
		`data: {}`,
		"",
		"event: text",
		`data: {"text":"must-not-pass"}`,
		"",
	}, "\n")
	dst := &memSSEWriter{}
	if err := PipeUpstream(context.Background(), strings.NewReader(raw), dst, nil); err != nil {
		t.Fatalf("PipeUpstream: %v", err)
	}
	frames, _ := dst.snapshot()
	if len(frames) != 1 || frames[0].Type != "done" {
		t.Fatalf("frames after terminal done = %+v", frames)
	}
}

func TestPipeUpstream_CleanEOFWithoutDoneIsTruncated(t *testing.T) {
	raw := "data: {\"type\":\"block\",\"block\":{\"type\":\"text\",\"text\":\"partial\"}}\n\n"
	dst := &memSSEWriter{}
	err := PipeUpstream(context.Background(), strings.NewReader(raw), dst, nil)
	if !errors.Is(err, ErrSSEStreamTruncated) {
		t.Fatalf("PipeUpstream error = %v, want ErrSSEStreamTruncated", err)
	}
	frames, _ := dst.snapshot()
	if len(frames) != 1 || logicalSSEEventType(frames[0]) != "block" {
		t.Fatalf("frames before truncated EOF = %+v", frames)
	}
}

// TestPipeUpstream_ContextCancellation: cancelling ctx during a read
// returns ctx.Err() within reasonable time.
func TestPipeUpstream_ContextCancellation(t *testing.T) {
	// Use a reader that blocks forever to simulate a stalled upstream.
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close(); _ = pr.Close() })
	// Write one full frame then hang.
	go func() {
		io.WriteString(pw, "event: text\ndata: {\"text\":\"x\"}\n\n")
	}()

	ctx, cancel := context.WithCancel(context.Background())
	dst := &memSSEWriter{}
	done := make(chan error, 1)
	go func() { done <- PipeUpstream(ctx, pr, dst, nil) }()

	// Give the first frame time to land then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()
	_ = pw.Close() // unblock scanner.Scan
	select {
	case err := <-done:
		if err == nil {
			// Pipe may have happened to drain before cancel; ok.
			return
		}
	case <-time.After(time.Second):
		t.Fatal("pipe did not return within 1s of cancel")
	}
}

func TestExtractTextFragment(t *testing.T) {
	cases := []struct {
		ev   SSEEvent
		want string
	}{
		{SSEEvent{Type: "text", Data: `{"text":"hi"}`}, "hi"},
		{SSEEvent{Type: "text_delta", Data: `{"delta":"hi"}`}, "hi"},
		{SSEEvent{Type: "content_block_delta", Data: `{"delta":{"text":"hi"}}`}, "hi"},
		{SSEEvent{Type: "text", Data: `not json`}, "not json"},
		{SSEEvent{Data: `{"type":"block","block":{"type":"text","text":"from envelope"}}`}, "from envelope"},
		{SSEEvent{Type: "done", Data: `{}`}, ""},
		{SSEEvent{Type: "tool_use", Data: `{"name":"x"}`}, ""},
	}
	for i, tc := range cases {
		if got := extractTextFragment(tc.ev); got != tc.want {
			t.Errorf("case %d: got %q, want %q", i, got, tc.want)
		}
	}
}

func TestGinSSEWriter_WriteEvent(t *testing.T) {
	var buf bytes.Buffer
	w := NewGinSSEWriter(&buf, nopFlusher{})
	if err := w.WriteEvent(SSEEvent{Type: "text", Data: `{"text":"hi"}`}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "event: text\n") {
		t.Errorf("missing event line: %q", out)
	}
	if !strings.Contains(out, `data: {"text":"hi"}`+"\n") {
		t.Errorf("missing data line: %q", out)
	}
	if !strings.HasSuffix(out, "\n\n") {
		t.Errorf("missing trailing blank line: %q", out)
	}
}

func TestGinSSEWriter_WriteKeepalive(t *testing.T) {
	var buf bytes.Buffer
	w := NewGinSSEWriter(&buf, nopFlusher{})
	if err := w.WriteKeepalive(); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if got != ": keepalive\n\n" {
		t.Errorf("keepalive shape: got %q, want %q", got, ": keepalive\n\n")
	}
}

func TestGinSSEWriter_KeepaliveInterleavesWithEvents(t *testing.T) {
	// Concurrent WriteEvent + WriteKeepalive must not corrupt frames.
	// GinSSEWriter's mutex must serialize them; the result should be
	// well-formed SSE (every frame ends with the blank-line delimiter).
	var buf bytes.Buffer
	w := NewGinSSEWriter(&buf, nopFlusher{})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			_ = w.WriteEvent(SSEEvent{Type: "text", Data: `{"text":"x"}`})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			_ = w.WriteKeepalive()
		}
	}()
	wg.Wait()

	// Validate: every non-empty line is either "event: ", "data: ", or ": ".
	// No event-data pair should be split by a keepalive (no "event:" line
	// directly followed by ":" comment with no "data:" in between).
	frames := strings.Split(buf.String(), "\n\n")
	for _, frame := range frames {
		if frame == "" {
			continue
		}
		lines := strings.Split(frame, "\n")
		hasEvent := false
		hasData := false
		isComment := false
		for _, l := range lines {
			switch {
			case strings.HasPrefix(l, "event: "):
				hasEvent = true
			case strings.HasPrefix(l, "data: "):
				hasData = true
			case strings.HasPrefix(l, ": "):
				isComment = true
			default:
				if l != "" {
					t.Errorf("unexpected line: %q", l)
				}
			}
		}
		if isComment && (hasEvent || hasData) {
			t.Errorf("keepalive interleaved into an event frame: %q", frame)
		}
		if hasEvent && !hasData {
			t.Errorf("event without data: %q", frame)
		}
	}
}

func TestGinSSEWriter_WriteProxyError(t *testing.T) {
	var buf bytes.Buffer
	w := NewGinSSEWriter(&buf, nopFlusher{})
	if err := w.WriteProxyError(ProxyError{Kind: KindNetworkUnavailable, Message: "down"}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "event: proxy_error\n") {
		t.Errorf("missing event: proxy_error: %q", out)
	}
	// Decode the data line and confirm the JSON shape.
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "data: ") {
			var pe ProxyError
			if err := json.Unmarshal([]byte(line[len("data: "):]), &pe); err != nil {
				t.Fatalf("data line not JSON: %v (%q)", err, line)
			}
			if pe.Kind != KindNetworkUnavailable {
				t.Errorf("decoded kind: got %q", pe.Kind)
			}
		}
	}
}

type nopFlusher struct{}

func (nopFlusher) Flush() {}
