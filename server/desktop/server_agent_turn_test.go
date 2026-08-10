//go:build desktop

package desktop

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
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	cloudproxy "server/desktop/cloud_proxy"
)

const (
	serverTestTurnUUID2 = "de305d54-75b4-431b-adb2-eb6b9e546015"
	serverTestTurnUUID3 = "de305d54-75b4-431b-adb2-eb6b9e546016"
)

func typedAgentChatBody(t *testing.T, turnUUID, threadUUID, text, mode string) []byte {
	t.Helper()
	body, err := json.Marshal(agentChatRequest{
		TurnUUID: turnUUID, ThreadUUID: threadUUID, UserText: text, ChatMode: mode,
		Payload: json.RawMessage(`{"stream":true}`),
	})
	if err != nil {
		t.Fatalf("marshal typed chat request: %v", err)
	}
	return body
}

func doSidecarRequest(t *testing.T, method, url, token string, body []byte) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Local-Token", token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func seedAgentTurnIntent(
	t *testing.T,
	db *gorm.DB,
	uid, threadID uint64,
	turnUUID, threadUUID, userText, mode, state, errorKind string,
) {
	t.Helper()
	digest, err := digestAgentTurnIntent(threadUUID, userText, mode)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := db.Exec(`
		INSERT INTO w_desktop_agent_turn_intent
			(uid, turn_uuid, thread_id, thread_uuid, user_text, chat_mode,
			 request_digest, state, last_error_kind, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		uid, turnUUID, threadID, threadUUID, userText, mode, digest, state, errorKind, now, now,
	).Error; err != nil {
		t.Fatalf("seed turn intent: %v", err)
	}
}

func TestValidateFrozenAgentTurnIntent(t *testing.T) {
	digest, _ := digestAgentTurnIntent("thr-valid", "frozen text", "ppt")
	valid := agentTurnIntent{
		UID: 42, TurnUUID: serverTestTurnUUID, ThreadID: 1, ThreadUUID: "thr-valid",
		UserText: "frozen text", ChatMode: "ppt", RequestDigest: digest,
		State: agentTurnIntentInterrupted, LastErrorKind: "sidecar_restarted",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if _, err := validateFrozenAgentTurnIntent(valid); err != nil {
		t.Fatalf("valid frozen intent: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*agentTurnIntent)
	}{
		{name: "turn uuid", mutate: func(intent *agentTurnIntent) { intent.TurnUUID = "not-a-uuid" }},
		{name: "blank text", mutate: func(intent *agentTurnIntent) { intent.UserText = "   " }},
		{name: "oversize text", mutate: func(intent *agentTurnIntent) { intent.UserText = strings.Repeat("x", maxAgentTurnUserTextBytes+1) }},
		{name: "NUL text", mutate: func(intent *agentTurnIntent) { intent.UserText = "bad\x00text" }},
		{name: "mode", mutate: func(intent *agentTurnIntent) { intent.ChatMode = "flashCard" }},
		{name: "digest", mutate: func(intent *agentTurnIntent) { intent.RequestDigest = strings.Repeat("0", 64) }},
		{name: "error kind control", mutate: func(intent *agentTurnIntent) { intent.LastErrorKind = "bad\nkind" }},
		{name: "error kind size", mutate: func(intent *agentTurnIntent) {
			intent.LastErrorKind = strings.Repeat("x", maxAgentTurnErrorKindBytes+1)
		}},
		{name: "updated at", mutate: func(intent *agentTurnIntent) { intent.UpdatedAt = "not-a-time" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			intent := valid
			test.mutate(&intent)
			// Model a malicious recomputation: semantic constraints must fail
			// independently of the unkeyed digest whenever request fields change.
			if test.name == "blank text" || test.name == "oversize text" || test.name == "NUL text" || test.name == "mode" {
				intent.RequestDigest, _ = digestAgentTurnIntent(intent.ThreadUUID, intent.UserText, intent.ChatMode)
			}
			if _, err := validateFrozenAgentTurnIntent(intent); err == nil {
				t.Fatalf("accepted corrupt frozen intent: %+v", intent)
			}
		})
	}
}

func TestAgentTurnRecoveryRoutes_ReplayListAndCancel(t *testing.T) {
	db := openServerTestDB(t)
	threadID := seedServerTestThread(t, db, 42, "thr-recovery")
	seedAgentTurnIntent(t, db, 42, threadID, serverTestTurnUUID, "thr-recovery", "recover me", "ppt", agentTurnIntentInterrupted, "sidecar_restarted")
	seedAgentTurnIntent(t, db, 42, threadID, serverTestTurnUUID2, "thr-recovery", "cancel me", "ppt", agentTurnIntentInterrupted, "request_canceled")
	seedAgentTurnIntent(t, db, 99, threadID, serverTestTurnUUID3, "thr-recovery", "private", "ppt", agentTurnIntentInterrupted, "private")

	var requestIDsMu sync.Mutex
	var requestIDs []string
	base, token := newServerFixtureWithDB(t, db, func(writer http.ResponseWriter, request *http.Request) {
		requestIDsMu.Lock()
		requestIDs = append(requestIDs, request.Header.Get("X-Agent-Request-Id"))
		requestIDsMu.Unlock()
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(writer, "event: done\ndata: {}\n\n")
		writer.(http.Flusher).Flush()
	})

	listResponse := doSidecarRequest(t, http.MethodGet, base+"/agent/turns/recoverable?limit=10", token, nil)
	listBody, _ := io.ReadAll(listResponse.Body)
	listResponse.Body.Close()
	if listResponse.StatusCode != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listResponse.StatusCode, listBody)
	}
	var listed map[string]any
	if err := json.Unmarshal(listBody, &listed); err != nil {
		t.Fatalf("decode recoverable response: %v", err)
	}
	items, ok := listed["items"].([]any)
	if !ok || len(items) != 2 || listed["count"] != float64(2) {
		t.Fatalf("recoverable response=%s", listBody)
	}
	for _, rawItem := range items {
		item := rawItem.(map[string]any)
		for _, forbidden := range []string{"uid", "thread_id", "cloud_thread_id", "request_digest"} {
			if _, exposed := item[forbidden]; exposed {
				t.Fatalf("recoverable item exposed %s: %s", forbidden, listBody)
			}
		}
	}

	replayResponse := doSidecarRequest(t, http.MethodPost, base+"/agent/turns/"+serverTestTurnUUID+"/replay", token, nil)
	replayBody, _ := io.ReadAll(replayResponse.Body)
	replayResponse.Body.Close()
	if replayResponse.StatusCode != http.StatusOK || !strings.Contains(string(replayBody), "event: done") {
		t.Fatalf("replay status=%d body=%s", replayResponse.StatusCode, replayBody)
	}
	var replayState string
	if err := db.Raw(`SELECT state FROM w_desktop_agent_turn_intent WHERE turn_uuid = ?`, serverTestTurnUUID).Row().Scan(&replayState); err != nil {
		t.Fatal(err)
	}
	if replayState != agentTurnIntentCompleted {
		t.Fatalf("replayed turn state=%q", replayState)
	}
	requestIDsMu.Lock()
	gotRequestIDs := append([]string(nil), requestIDs...)
	requestIDsMu.Unlock()
	if len(gotRequestIDs) != 1 || gotRequestIDs[0] != "desktop-turn:"+serverTestTurnUUID {
		t.Fatalf("replay request IDs=%q", gotRequestIDs)
	}

	cancelResponse := doSidecarRequest(t, http.MethodPost, base+"/agent/turns/"+serverTestTurnUUID2+"/cancel", token, nil)
	cancelBody, _ := io.ReadAll(cancelResponse.Body)
	cancelResponse.Body.Close()
	if cancelResponse.StatusCode != http.StatusOK || string(cancelBody) != `{"turn_uuid":"`+serverTestTurnUUID2+`","canceled":true}` {
		t.Fatalf("cancel status=%d body=%s", cancelResponse.StatusCode, cancelBody)
	}

	postList := doSidecarRequest(t, http.MethodGet, base+"/agent/turns/recoverable", token, nil)
	postListBody, _ := io.ReadAll(postList.Body)
	postList.Body.Close()
	if postList.StatusCode != http.StatusOK || !strings.Contains(string(postListBody), `"count":0`) {
		t.Fatalf("post-outcome recoverable response status=%d body=%s", postList.StatusCode, postListBody)
	}
}

func TestAgentTurnRecovery_FailsClosedOnCorruptedFrozenRequest(t *testing.T) {
	db := openServerTestDB(t)
	threadID := seedServerTestThread(t, db, 42, "thr-corrupt")
	seedAgentTurnIntent(t, db, 42, threadID, serverTestTurnUUID, "thr-corrupt", "mode corruption", "ppt", agentTurnIntentInterrupted, "corrupt")
	seedAgentTurnIntent(t, db, 42, threadID, serverTestTurnUUID2, "thr-corrupt", "text corruption", "ppt", agentTurnIntentInterrupted, "corrupt")
	modeDigest, _ := digestAgentTurnIntent("thr-corrupt", "mode corruption", "flashCard")
	if err := db.Exec(`UPDATE w_desktop_agent_turn_intent SET chat_mode = 'flashCard', request_digest = ? WHERE turn_uuid = ?`, modeDigest, serverTestTurnUUID).Error; err != nil {
		t.Fatal(err)
	}
	corruptText := "bad\x00text"
	textDigest, _ := digestAgentTurnIntent("thr-corrupt", corruptText, "ppt")
	if err := db.Exec(`UPDATE w_desktop_agent_turn_intent SET user_text = ?, request_digest = ? WHERE turn_uuid = ?`, corruptText, textDigest, serverTestTurnUUID2).Error; err != nil {
		t.Fatal(err)
	}
	var cloudCalls atomic.Int64
	base, token := newServerFixtureWithDB(t, db, func(http.ResponseWriter, *http.Request) {
		cloudCalls.Add(1)
	})

	list := doSidecarRequest(t, http.MethodGet, base+"/agent/turns/recoverable", token, nil)
	listBody, _ := io.ReadAll(list.Body)
	list.Body.Close()
	if list.StatusCode != http.StatusInternalServerError || !strings.Contains(string(listBody), "agent_turn_recovery_unavailable") {
		t.Fatalf("corrupt list status=%d body=%s", list.StatusCode, listBody)
	}
	for _, turnUUID := range []string{serverTestTurnUUID, serverTestTurnUUID2} {
		replay := doSidecarRequest(t, http.MethodPost, base+"/agent/turns/"+turnUUID+"/replay", token, nil)
		replayBody, _ := io.ReadAll(replay.Body)
		replay.Body.Close()
		if replay.StatusCode != http.StatusInternalServerError || !strings.Contains(string(replayBody), "agent_turn_intent_invalid") {
			t.Fatalf("corrupt replay %s status=%d body=%s", turnUUID, replay.StatusCode, replayBody)
		}
	}
	if cloudCalls.Load() != 0 {
		t.Fatalf("corrupted frozen intent reached upstream %d time(s)", cloudCalls.Load())
	}
}

func TestAgentChat_IdempotentReplayAndDigestConflict(t *testing.T) {
	db := openServerTestDB(t)
	seedServerTestThread(t, db, 42, "thr-idempotent")
	var calls atomic.Int64
	var requestIDsMu sync.Mutex
	var requestIDs []string
	base, token := newServerFixtureWithDB(t, db, func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		requestIDsMu.Lock()
		requestIDs = append(requestIDs, request.Header.Get("X-Agent-Request-Id"))
		requestIDsMu.Unlock()
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(writer, "event: done\ndata: {}\n\n")
		writer.(http.Flusher).Flush()
	})
	body := typedAgentChatBody(t, serverTestTurnUUID, "thr-idempotent", "same bytes", "ppt")
	for attempt := 0; attempt < 2; attempt++ {
		response := doSidecarRequest(t, http.MethodPost, base+"/agent/chat", token, body)
		responseBody, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("same-digest attempt %d status=%d body=%s", attempt, response.StatusCode, responseBody)
		}
	}
	conflict := doSidecarRequest(t, http.MethodPost, base+"/agent/chat", token,
		typedAgentChatBody(t, serverTestTurnUUID, "thr-idempotent", "different bytes", "ppt"))
	conflictBody, _ := io.ReadAll(conflict.Body)
	conflict.Body.Close()
	if conflict.StatusCode != http.StatusConflict || !strings.Contains(string(conflictBody), "idempotency_conflict") {
		t.Fatalf("digest conflict status=%d body=%s", conflict.StatusCode, conflictBody)
	}
	if calls.Load() != 2 {
		t.Fatalf("upstream calls=%d, want two same-digest attempts only", calls.Load())
	}
	requestIDsMu.Lock()
	defer requestIDsMu.Unlock()
	if len(requestIDs) != 2 || requestIDs[0] != requestIDs[1] || requestIDs[0] != "desktop-turn:"+serverTestTurnUUID {
		t.Fatalf("same intent request IDs=%q", requestIDs)
	}
}

func TestAgentChat_TurnUUIDCollisionIsOwnerFirst(t *testing.T) {
	db := openServerTestDB(t)
	threadID := seedServerTestThread(t, db, 42, "thr-current-owner")
	seedAgentTurnIntent(t, db, 99, threadID, serverTestTurnUUID, "other-account-secret", "secret prompt", "ppt", agentTurnIntentInterrupted, "private")
	base, token := newServerFixtureWithDB(t, db, func(http.ResponseWriter, *http.Request) {
		t.Error("cross-owner turn UUID collision reached upstream")
	})
	response := doSidecarRequest(t, http.MethodPost, base+"/agent/chat", token,
		typedAgentChatBody(t, serverTestTurnUUID, "thr-current-owner", "different request", "ppt"))
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusConflict || !strings.Contains(string(body), "turn_uuid_conflict") {
		t.Fatalf("owner collision status=%d body=%s", response.StatusCode, body)
	}
	if strings.Contains(string(body), "idempotency") || strings.Contains(string(body), "thread") {
		t.Fatalf("owner-first conflict leaked request comparison detail: %s", body)
	}
}

func TestAgentChat_ReplacementSessionCannotProbeForeignActiveLock(t *testing.T) {
	db := openServerTestDB(t)
	oldThreadID := seedServerTestThread(t, db, 42, "thr-old-session")
	seedServerTestThread(t, db, 99, "thr-new-session")
	seedAgentTurnIntent(t, db, 42, oldThreadID, serverTestTurnUUID, "thr-old-session", "old secret", "ppt", agentTurnIntentStreaming, "")
	store := cloudproxy.NewTokenStore(newMemKeychain())
	for _, uid := range []uint64{42, 99} {
		if err := store.Save(cloudproxy.TokenPair{
			AccessToken:      mintLocalHistoryJWT(uid),
			AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
			RefreshToken:     fmt.Sprintf("refresh-%d", uid),
			RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}
	client := cloudproxy.NewClient("http://127.0.0.1:1")
	server := &Server{cfg: ServerConfig{
		DB: db, TokenStore: store, Proxy: cloudproxy.NewProxy(client, store, db),
	}}
	foreignLock := server.agentTurnLock(serverTestTurnUUID)
	foreignLock.Lock()
	defer foreignLock.Unlock()

	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.POST("/agent/chat", server.handleAgentChat)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(
		http.MethodPost, "/agent/chat",
		bytes.NewReader(typedAgentChatBody(t, serverTestTurnUUID, "thr-new-session", "guess", "ppt")),
	))
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "turn_uuid_conflict") {
		t.Fatalf("replacement collision status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "turn_in_progress") {
		t.Fatalf("replacement session observed foreign lock liveness: %s", recorder.Body.String())
	}
}

func TestAgentChat_SameProcessTryLock(t *testing.T) {
	db := openServerTestDB(t)
	seedServerTestThread(t, db, 42, "thr-lock")
	arrived := make(chan struct{})
	release := make(chan struct{})
	base, token := newServerFixtureWithDB(t, db, func(writer http.ResponseWriter, request *http.Request) {
		close(arrived)
		<-release
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(writer, "event: done\ndata: {}\n\n")
		writer.(http.Flusher).Flush()
	})
	body := typedAgentChatBody(t, serverTestTurnUUID, "thr-lock", "one active request", "ppt")
	firstDone := make(chan error, 1)
	go func() {
		request, _ := http.NewRequest(http.MethodPost, base+"/agent/chat", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Local-Token", token)
		response, err := http.DefaultClient.Do(request)
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode != http.StatusOK {
				err = fmt.Errorf("first status=%d", response.StatusCode)
			}
		}
		firstDone <- err
	}()
	<-arrived
	second := doSidecarRequest(t, http.MethodPost, base+"/agent/chat", token, body)
	secondBody, _ := io.ReadAll(second.Body)
	second.Body.Close()
	if second.StatusCode != http.StatusConflict || !strings.Contains(string(secondBody), "turn_in_progress") {
		close(release)
		t.Fatalf("second status=%d body=%s", second.StatusCode, secondBody)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
}

func TestAgentTurn_CancelWinsBeforeUpstreamTransition(t *testing.T) {
	db := openServerTestDB(t)
	seedServerTestThread(t, db, 42, "thr-cancel-wins")
	store := cloudproxy.NewTokenStore(newMemKeychain())
	if err := store.Save(cloudproxy.TokenPair{
		AccessToken:      mintLocalHistoryJWT(42),
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	var cloudCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		cloudCalls.Add(1)
	}))
	t.Cleanup(upstream.Close)
	client := cloudproxy.NewClient(upstream.URL)
	client.HTTPClient = upstream.Client()
	proxy := cloudproxy.NewProxy(client, store, db)
	proxy.HTTPClient = upstream.Client()
	server := &Server{cfg: ServerConfig{DB: db, TokenStore: store, Proxy: proxy}}
	identity := server.resolveIdentity()
	if !identity.IsCloud() {
		t.Fatalf("identity = %+v, want the connected account", identity)
	}
	uid, lease := identity.UID, identity.Lease
	digest, _ := digestAgentTurnIntent("thr-cancel-wins", "frozen", "ppt")
	intent, thread, _, err := ensureAgentTurnIntent(
		db, lease, uid, serverTestTurnUUID, "thr-cancel-wins", "frozen", "ppt", digest,
	)
	if err != nil {
		t.Fatal(err)
	}
	if canceled, err := cancelAgentTurnIntent(db, lease, uid, serverTestTurnUUID); err != nil || !canceled {
		t.Fatalf("cancel=%v err=%v", canceled, err)
	}

	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.POST("/stream", func(context *gin.Context) {
		server.streamLegacyAgentTurn(context, legacyAgentTurnStreamInput{Intent: intent, Thread: thread, Lease: lease})
	})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/stream", nil))
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "turn_canceled") {
		t.Fatalf("canceled stream status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if cloudCalls.Load() != 0 {
		t.Fatalf("canceled turn reached upstream %d time(s)", cloudCalls.Load())
	}
}

func TestAgentTurn_TerminalOutcomeIsPinnedToFrozenOwnerAfterEpochChange(t *testing.T) {
	db := openServerTestDB(t)
	threadID := seedServerTestThread(t, db, 42, "thr-terminal-epoch")
	seedAgentTurnIntent(t, db, 42, threadID, serverTestTurnUUID, "thr-terminal-epoch", "frozen", "ppt", agentTurnIntentStreaming, "")
	store := cloudproxy.NewTokenStore(newMemKeychain())
	if err := store.Save(cloudproxy.TokenPair{
		AccessToken:      mintLocalHistoryJWT(42),
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "old-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	oldSnapshot, err := store.GetSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(cloudproxy.TokenPair{
		AccessToken:      mintLocalHistoryJWT(99),
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "replacement-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := oldSnapshot.Lease.Check(); !errors.Is(err, cloudproxy.ErrSessionChanged) {
		t.Fatalf("old lease check=%v, want session changed", err)
	}
	if err := finalizeAgentTurnIntentOutcome(
		db, 42, serverTestTurnUUID, agentTurnIntentCompleted, "",
	); err != nil {
		t.Fatalf("frozen owner terminal update: %v", err)
	}
	var state string
	if err := db.Raw(`SELECT state FROM w_desktop_agent_turn_intent WHERE turn_uuid = ?`, serverTestTurnUUID).Row().Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != agentTurnIntentCompleted {
		t.Fatalf("terminal state=%q, want completed", state)
	}

	if err := db.Exec(`UPDATE w_desktop_agent_turn_intent SET state = 'canceled' WHERE turn_uuid = ?`, serverTestTurnUUID).Error; err != nil {
		t.Fatal(err)
	}
	if err := finalizeAgentTurnIntentOutcome(
		db, 42, serverTestTurnUUID, agentTurnIntentCompleted, "",
	); err != nil {
		t.Fatalf("late terminal on canceled row: %v", err)
	}
	if err := db.Raw(`SELECT state FROM w_desktop_agent_turn_intent WHERE turn_uuid = ?`, serverTestTurnUUID).Row().Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != agentTurnIntentCanceled {
		t.Fatalf("late terminal overwrote canceled state with %q", state)
	}
	if err := finalizeAgentTurnIntentOutcome(
		db, 99, serverTestTurnUUID, agentTurnIntentCompleted, "",
	); err == nil {
		t.Fatal("replacement owner updated old account's terminal row")
	}
}

func TestAgentChat_ThreadBusyRemainsRecoverable(t *testing.T) {
	db := openServerTestDB(t)
	seedServerTestThread(t, db, 42, "thr-busy")
	base, token := newServerFixtureWithDB(t, db, func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(writer, `data: {"type":"done","result":{"code":"THREAD_BUSY"}}`+"\n\n")
		writer.(http.Flusher).Flush()
	})
	requestBody := typedAgentChatBody(t, serverTestTurnUUID, "thr-busy", "busy", "ppt")
	for attempt := 0; attempt < 3; attempt++ {
		response := doSidecarRequest(t, http.MethodPost, base+"/agent/chat", token, requestBody)
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "THREAD_BUSY") {
			t.Fatalf("busy relay attempt=%d status=%d body=%s", attempt, response.StatusCode, body)
		}
	}
	var state, errorKind string
	if err := db.Raw(`SELECT state, last_error_kind FROM w_desktop_agent_turn_intent WHERE turn_uuid = ?`, serverTestTurnUUID).
		Row().Scan(&state, &errorKind); err != nil {
		t.Fatal(err)
	}
	if state != agentTurnIntentInterrupted || errorKind != "turn_in_progress" {
		t.Fatalf("THREAD_BUSY state=%s/%s, want interrupted/turn_in_progress", state, errorKind)
	}
	var messageCount int
	if err := db.Raw(`SELECT COUNT(*) FROM w_workagent_message WHERE message_idempotency_key = ?`, "desktop-turn:"+serverTestTurnUUID).
		Row().Scan(&messageCount); err != nil {
		t.Fatal(err)
	}
	if messageCount != 0 {
		t.Fatalf("three busy retries created %d local message row(s), want 0", messageCount)
	}
}

func TestValidateLegacyAgentTurnPayload_Files(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{"stream only", `{"stream":true}`, false},
		{"stream plus files", `{"stream":true,"files":[1,2]}`, false},
		{"empty files", `{"stream":true,"files":[]}`, false},
		{"negative id rejected", `{"stream":true,"files":[-1]}`, true},
		{"unknown field rejected", `{"stream":true,"x":1}`, true},
		{"missing stream rejected", `{"files":[1]}`, true},
		{"stream false rejected", `{"stream":false}`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateLegacyAgentTurnPayload(json.RawMessage(c.raw))
			if c.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestLegacyAgentTurnDoneIsBusy(t *testing.T) {
	tests := []struct {
		name  string
		event legacyAgentTurnEventFixture
		want  bool
	}{
		{name: "explicit", event: legacyAgentTurnEventFixture{eventType: "done", data: `{"code":"THREAD_BUSY"}`}, want: true},
		{name: "data only nested", event: legacyAgentTurnEventFixture{data: `{"type":"done","result":{"subtype":"thread_busy"}}`}, want: true},
		{name: "other done", event: legacyAgentTurnEventFixture{eventType: "done", data: `{}`}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := legacyAgentTurnDoneIsBusy(test.event.sse()); got != test.want {
				t.Fatalf("busy=%v want=%v", got, test.want)
			}
		})
	}
}

type legacyAgentTurnEventFixture struct {
	eventType string
	data      string
}

func (fixture legacyAgentTurnEventFixture) sse() cloudproxy.SSEEvent {
	return cloudproxy.SSEEvent{Type: fixture.eventType, Data: fixture.data}
}

// fakeLocalRunner 是一个 TurnRunner stub：记录是否被调用并发出单个 done 事件。
// 用于验证 streamLegacyAgentTurn 在 local 路由下把 turn 交给 LocalInference。
type fakeLocalRunner struct {
	called atomic.Bool
}

func (f *fakeLocalRunner) Chat(_ context.Context, _ cloudproxy.ChatRequest, dst cloudproxy.SSEWriter) error {
	f.called.Store(true)
	return dst.WriteEvent(cloudproxy.SSEEvent{Type: "done", Data: `{"type":"done","result":"OK"}`})
}

// flushingRecorder 包装 httptest.ResponseRecorder 实现 http.Flusher，使
// streamLegacyAgentTurn 的 Flusher 存在性检查通过（recorder 本身不实现 Flush）。
type flushingRecorder struct {
	*httptest.ResponseRecorder
}

func (flushingRecorder) Flush() {}

// TestStreamLegacyAgentTurn_LocalRoute 验证 preferred_route=local 时：
//   - 调用 LocalInference.Chat（Proxy 为 nil 也不 panic，证明 local 路径不碰云端）；
//   - 不要求 cloud_thread_id（local 路径跳过云端线程解析，不会报 thread_not_ready）；
//   - 干净 done 后 finalize 把 turn intent 标记为 completed。
func TestStreamLegacyAgentTurn_LocalRoute(t *testing.T) {
	db := openServerTestDB(t)
	seedServerTestThread(t, db, 42, "thr-local-route")
	// openServerTestDB 用内联建表，不含 w_desktop_model_settings（0004 migration）；补建。
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS w_desktop_model_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    local_protocol TEXT NOT NULL DEFAULT '',
    local_base_url TEXT NOT NULL DEFAULT '',
    local_model_id TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
)`).Error; err != nil {
		t.Fatalf("create model settings table: %v", err)
	}
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS w_desktop_model_preference (
    uid INTEGER PRIMARY KEY,
    preferred_route TEXT NOT NULL DEFAULT 'official',
    official_model_id TEXT NOT NULL DEFAULT '',
    local_api_key_present INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
)`).Error; err != nil {
		t.Fatalf("create model preference table: %v", err)
	}
	store := cloudproxy.NewTokenStore(newMemKeychain())
	if err := store.Save(cloudproxy.TokenPair{
		AccessToken:      mintLocalHistoryJWT(42),
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	modelSettings := NewLocalModelSettingsStore(db, newMemKeychain())
	// uid 42 is the subject of the access token above: model settings are
	// per-identity now, so the route preference has to be stored under the
	// identity the server will resolve for this request.
	if _, err := modelSettings.Put(42, LocalModelSettingsPut{
		PreferredRoute: ModelRouteLocal,
		Local: &LocalModelProfilePut{
			Protocol: LocalProtocolOpenAICompatible,
			BaseURL:  "http://127.0.0.1:11434/v1",
			ModelID:  "llama3.2",
		},
	}); err != nil {
		t.Fatalf("put local settings: %v", err)
	}

	runner := &fakeLocalRunner{}
	server := &Server{cfg: ServerConfig{
		DB:             db,
		TokenStore:     store,
		ModelSettings:  modelSettings,
		LocalInference: runner,
		Proxy:          nil, // local 路径不应触碰云端 Proxy
	}}

	identity := server.resolveIdentity()
	if !identity.IsCloud() {
		t.Fatalf("identity = %+v, want the connected account", identity)
	}
	uid, lease := identity.UID, identity.Lease
	digest, _ := digestAgentTurnIntent("thr-local-route", "hi", "general")
	intent, thread, _, err := ensureAgentTurnIntent(
		db, lease, uid, serverTestTurnUUID, "thr-local-route", "hi", "general", digest,
	)
	if err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.POST("/stream", func(c *gin.Context) {
		server.streamLegacyAgentTurn(c, legacyAgentTurnStreamInput{Intent: intent, Thread: thread, Lease: lease})
	})
	rec := flushingRecorder{httptest.NewRecorder()}
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/stream", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s (local route must run without cloud_thread_id and Proxy)", rec.Code, rec.Body.String())
	}
	if !runner.called.Load() {
		t.Fatal("LocalInference.Chat was not called for local route")
	}
	if !strings.Contains(rec.Body.String(), "event: done") {
		t.Fatalf("expected a done event in SSE output: %s", rec.Body.String())
	}
	var state string
	if err := db.Raw(`SELECT state FROM w_desktop_agent_turn_intent WHERE turn_uuid = ?`, serverTestTurnUUID).Row().Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != agentTurnIntentCompleted {
		t.Fatalf("intent state=%q, want completed", state)
	}
}

// ensureLocalModelSettingsDB patches openServerTestDB (which lacks the 0004
// and 0009 migrations) with the model settings tables and turns the local
// route on. Reused by the L3d unauthenticated-local tests.
//
// The route preference is per-identity (migration 0009), and these fixtures
// boot servers that resolve to different identities: uid 0 for a trimmed boot
// with no TokenStore, the reserved single-user uid for a signed-out machine,
// and a cloud subject when a token is present. Seeding all of them keeps
// "the local route is on" the fixture's meaning rather than a statement about
// one particular caller. Extra uids are for tests that mint their own subject.
func ensureLocalModelSettingsDB(t *testing.T, db *gorm.DB, extraUIDs ...uint64) *LocalModelSettingsStore {
	t.Helper()
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS w_desktop_model_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    local_protocol TEXT NOT NULL DEFAULT '',
    local_base_url TEXT NOT NULL DEFAULT '',
    local_model_id TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
)`).Error; err != nil {
		t.Fatalf("create model settings table: %v", err)
	}
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS w_desktop_model_preference (
    uid INTEGER PRIMARY KEY,
    preferred_route TEXT NOT NULL DEFAULT 'official',
    official_model_id TEXT NOT NULL DEFAULT '',
    local_api_key_present INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
)`).Error; err != nil {
		t.Fatalf("create model preference table: %v", err)
	}
	store := NewLocalModelSettingsStore(db, newMemKeychain())
	for _, uid := range append([]uint64{0, localSingleUserUID}, extraUIDs...) {
		if _, err := store.Put(uid, LocalModelSettingsPut{
			PreferredRoute: ModelRouteLocal,
			Local: &LocalModelProfilePut{
				Protocol: LocalProtocolOpenAICompatible,
				BaseURL:  "http://127.0.0.1:11434/v1",
				ModelID:  "llama3.2",
			},
		}); err != nil {
			t.Fatalf("put local settings for uid %d: %v", uid, err)
		}
	}
	return store
}

// TestEnsureAgentTurnIntent_EmptyLeaseLocalUID: the L3d lease change lets the
// intent store run with an empty SessionLease (local route) under
// localSingleUserUID — it must not return ErrSessionChanged.
func TestEnsureAgentTurnIntent_EmptyLeaseLocalUID(t *testing.T) {
	db := openServerTestDB(t)
	seedServerTestThread(t, db, localSingleUserUID, "thr-local-uid")
	digest, _ := digestAgentTurnIntent("thr-local-uid", "hi", "general")
	intent, _, created, err := ensureAgentTurnIntent(
		db, cloudproxy.SessionLease{}, localSingleUserUID,
		serverTestTurnUUID, "thr-local-uid", "hi", "general", digest,
	)
	if err != nil {
		t.Fatalf("empty lease + local uid must work (L3d): %v", err)
	}
	if !created {
		t.Fatal("expected intent created")
	}
	if intent.UID != localSingleUserUID {
		t.Fatalf("intent uid = %d, want %d", intent.UID, localSingleUserUID)
	}
}

// L2 dispatch: the protocol the user chose in model settings picks the local
// engine. anthropic_compatible with a wired CLI goes to the tool loop;
// everything else — including anthropic_compatible with no CLI — stays on L1
// pure chat rather than failing.
func TestLocalTurnRunner_DispatchesByProtocol(t *testing.T) {
	db := openServerTestDB(t)
	l1 := &fakeLocalRunner{}
	l2 := &fakeLocalRunner{}

	// ensureLocalModelSettingsDB creates the table and seeds openai; overwrite
	// with the anthropic profile for the dispatch-to-L2 case.
	anthropic := ensureLocalModelSettingsDB(t, db)
	// No TokenStore on the server below, so it resolves the unscoped identity.
	if _, err := anthropic.Put(0, LocalModelSettingsPut{
		PreferredRoute: ModelRouteLocal,
		Local: &LocalModelProfilePut{
			Protocol: LocalProtocolAnthropicCompatible,
			BaseURL:  "http://127.0.0.1:1", ModelID: "m",
		},
	}); err != nil {
		t.Fatal(err)
	}

	srv := &Server{cfg: ServerConfig{ModelSettings: anthropic, LocalInference: l1, LocalAgent: l2}}
	if got := srv.localTurnRunner(); got != TurnRunner(l2) {
		t.Error("anthropic_compatible with a CLI must dispatch to the tool loop")
	}

	srv = &Server{cfg: ServerConfig{ModelSettings: anthropic, LocalInference: l1, LocalAgent: nil}}
	if got := srv.localTurnRunner(); got != TurnRunner(l1) {
		t.Error("no CLI wired: anthropic_compatible must fall back to L1, not fail")
	}

	openai := ensureLocalModelSettingsDB(t, openServerTestDB(t))
	srv = &Server{cfg: ServerConfig{ModelSettings: openai, LocalInference: l1, LocalAgent: l2}}
	if got := srv.localTurnRunner(); got != TurnRunner(l1) {
		t.Error("openai_compatible must stay on L1 even when a tool loop is wired")
	}
}
