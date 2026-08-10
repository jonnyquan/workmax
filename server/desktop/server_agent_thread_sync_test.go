//go:build desktop

package desktop

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	cloudproxy "server/desktop/cloud_proxy"
)

// The paused state has been honored by the sync writer and the history reader
// since it was introduced, and nothing in production ever wrote it. These
// tests are the write end.

const threadCloudSyncToken = "cloud-sync-token"

func newThreadCloudSyncFixture(t *testing.T) (base string, db *gorm.DB) {
	t.Helper()
	db = openMigratedTestDB(t)
	srv, err := NewServer(ServerConfig{
		SidecarVersion: "cloud-sync-test",
		LocalToken:     threadCloudSyncToken,
		DB:             db,
		TokenStore:     cloudproxy.NewTokenStore(newMemKeychain()),
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return "http://" + srv.listener.Addr().String(), db
}

func seedCloudSyncThread(t *testing.T, db *gorm.DB, uid uint64, uuid, state string) {
	t.Helper()
	if err := db.Exec(
		`INSERT INTO w_workagent_thread (uid, uuid, name, agent_type, cloud_sync_state)
		 VALUES (?, ?, 'A conversation', 'general_agent', ?)`,
		uid, uuid, state,
	).Error; err != nil {
		t.Fatalf("seed thread: %v", err)
	}
}

func putCloudSync(t *testing.T, base, uuid, body string) (int, string) {
	t.Helper()
	request, err := http.NewRequest(
		http.MethodPut, base+"/agent/threads/"+uuid+"/cloud-sync", strings.NewReader(body),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Local-Token", threadCloudSyncToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("PUT cloud-sync: %v", err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	return response.StatusCode, strings.TrimSpace(string(raw))
}

func readCloudSyncState(t *testing.T, db *gorm.DB, uuid string) string {
	t.Helper()
	var state string
	if err := db.Raw(
		`SELECT COALESCE(cloud_sync_state,'synced') FROM w_workagent_thread WHERE uuid = ?`, uuid,
	).Row().Scan(&state); err != nil {
		t.Fatalf("read state: %v", err)
	}
	return state
}

const cloudSyncThreadUUID = "de305d54-75b4-431b-adb2-eb6b9e546100"

func TestThreadCloudSyncPausesAndResumes(t *testing.T) {
	base, db := newThreadCloudSyncFixture(t)
	seedCloudSyncThread(t, db, localSingleUserUID, cloudSyncThreadUUID, "synced")

	status, body := putCloudSync(t, base, cloudSyncThreadUUID, `{"state":"paused"}`)
	if status != http.StatusOK || !strings.Contains(body, `"cloud_sync_state":"paused"`) {
		t.Fatalf("pause = %d %s", status, body)
	}
	if got := readCloudSyncState(t, db, cloudSyncThreadUUID); got != "paused" {
		t.Fatalf("stored state = %q", got)
	}

	// Idempotent: pausing what is paused is paused.
	if status, _ := putCloudSync(t, base, cloudSyncThreadUUID, `{"state":"paused"}`); status != http.StatusOK {
		t.Fatalf("repeat pause = %d", status)
	}

	status, _ = putCloudSync(t, base, cloudSyncThreadUUID, `{"state":"synced"}`)
	if status != http.StatusOK {
		t.Fatalf("resume = %d", status)
	}
	if got := readCloudSyncState(t, db, cloudSyncThreadUUID); got != "synced" {
		t.Fatalf("stored state after resume = %q", got)
	}
}

// A thread that never left this machine has no sync to pause. Answering 200
// would tell the user an action protected their data when it did nothing.
func TestThreadCloudSyncRefusesALocalOnlyThread(t *testing.T) {
	base, db := newThreadCloudSyncFixture(t)
	seedCloudSyncThread(t, db, localSingleUserUID, cloudSyncThreadUUID, "local")

	status, body := putCloudSync(t, base, cloudSyncThreadUUID, `{"state":"paused"}`)
	if status != http.StatusConflict || !strings.Contains(body, "thread_not_synced") {
		t.Fatalf("local thread pause = %d %s, want 409 thread_not_synced", status, body)
	}
	if got := readCloudSyncState(t, db, cloudSyncThreadUUID); got != "local" {
		t.Fatalf("refused request still wrote %q", got)
	}
}

// Ownership, as everywhere else: another identity's thread is not found rather
// than forbidden, so the endpoint cannot be used to probe for uuids.
func TestThreadCloudSyncCannotReachAnotherIdentitysThread(t *testing.T) {
	base, db := newThreadCloudSyncFixture(t)
	seedCloudSyncThread(t, db, localSingleUserUID+7, cloudSyncThreadUUID, "synced")

	status, body := putCloudSync(t, base, cloudSyncThreadUUID, `{"state":"paused"}`)
	if status != http.StatusNotFound || !strings.Contains(body, "thread_not_found") {
		t.Fatalf("foreign thread = %d %s, want 404 thread_not_found", status, body)
	}
	if got := readCloudSyncState(t, db, cloudSyncThreadUUID); got != "synced" {
		t.Fatalf("foreign thread was modified: %q", got)
	}
}

func TestThreadCloudSyncRejectsUnknownStates(t *testing.T) {
	base, db := newThreadCloudSyncFixture(t)
	seedCloudSyncThread(t, db, localSingleUserUID, cloudSyncThreadUUID, "synced")

	for _, body := range []string{
		`{"state":"local"}`,        // not a user-settable state
		`{"state":"Paused"}`,       // no case folding
		`{"state":""}`,             // no empty meaning "default"
		`{"state":"paused","x":1}`, // no unknown fields
		`{}`,
		`{"state":"paused"}{"state":"synced"}`,
	} {
		status, response := putCloudSync(t, base, cloudSyncThreadUUID, body)
		if status != http.StatusBadRequest {
			t.Fatalf("body %s = %d %s, want 400", body, status, response)
		}
	}
	if got := readCloudSyncState(t, db, cloudSyncThreadUUID); got != "synced" {
		t.Fatalf("a rejected body still changed state: %q", got)
	}
}

// The list route must keep showing a paused thread — it is the renderer's only
// view of the conversation, and the switch is not a delete.
func TestPausedThreadsRemainListableForTheirOwner(t *testing.T) {
	_, db := newThreadCloudSyncFixture(t)
	seedCloudSyncThread(t, db, localSingleUserUID, cloudSyncThreadUUID, "paused")

	withPaused, err := ListLocalThreads(db, localSingleUserUID, 50, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(withPaused) != 1 || withPaused[0].CloudSync != "paused" {
		t.Fatalf("include_paused=true rows = %+v", withPaused)
	}
	withoutPaused, err := ListLocalThreads(db, localSingleUserUID, 50, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(withoutPaused) != 0 {
		t.Fatalf("include_paused=false must still hide them: %+v", withoutPaused)
	}
}
