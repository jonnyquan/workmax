//go:build desktop

package desktop

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const exportTestThreadUUID = "de305d54-75b4-431b-adb2-eb6b9e546301"

func TestExportThreadWritesMarkdownIntoWorkspace(t *testing.T) {
	base, db, filesDir, _ := newDeleteFixture(t)
	dataDir := filepath.Dir(filesDir)
	threadID := seedLocalThreadForDelete(t, db, localSingleUserUID, exportTestThreadUUID)
	if err := db.Exec(
		`UPDATE w_workagent_thread SET name = 'Q3 复盘' WHERE id = ?`, threadID,
	).Error; err != nil {
		t.Fatal(err)
	}
	for i, pair := range [][2]string{
		{"把 Q3 数字整理成复盘", "好的，先看营收：**up 12%**"},
		{"补充 churn 部分", "Churn 2.1%，环比持平"},
	} {
		if err := db.Exec(
			`INSERT INTO w_workagent_message (uid, uuid, thread_id, user_text, ai_text, streaming_state)
			 VALUES (?, ?, ?, ?, ?, 'complete')`,
			localSingleUserUID, "m-exp-"+string(rune('a'+i)), threadID, pair[0], pair[1],
		).Error; err != nil {
			t.Fatal(err)
		}
	}

	resp, body := localAccountsRequest(
		t, http.MethodPost, base+"/agent/threads/"+exportTestThreadUUID+"/export", "tok", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("export: %d %s", resp.StatusCode, body)
	}
	var parsed exportThreadResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !parsed.Exported || parsed.Messages != 2 || !strings.HasPrefix(parsed.Path, "exports/") {
		t.Fatalf("response = %+v", parsed)
	}

	full := filepath.Join(dataDir, "agent_workspace", "thread_"+exportTestThreadUUID, filepath.FromSlash(parsed.Path))
	content, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("exported file missing: %v", err)
	}
	text := string(content)
	for _, want := range []string{
		"# Q3 复盘",
		"把 Q3 数字整理成复盘",
		"**up 12%**",
		"Churn 2.1%",
		"**You**",
		"**WorkMax**",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("export lacks %q:\n%s", want, text)
		}
	}

	// The export is a deliverable: the workspace listing must show it, which
	// is what puts it in the Deliverables panel with zero new machinery.
	resp, body = localAccountsRequest(
		t, http.MethodGet, base+"/agent/threads/"+exportTestThreadUUID+"/workspace", "tok", "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), parsed.Path) {
		t.Fatalf("workspace listing must include the export: %d %s", resp.StatusCode, body)
	}
}

func TestExportThreadRefusals(t *testing.T) {
	base, db, _, _ := newDeleteFixture(t)
	// Empty thread → 409: an export that says nothing is not a deliverable.
	threadID := seedLocalThreadForDelete(t, db, localSingleUserUID, exportTestThreadUUID)
	_ = threadID
	resp, body := localAccountsRequest(
		t, http.MethodPost, base+"/agent/threads/"+exportTestThreadUUID+"/export", "tok", "")
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "thread_empty") {
		t.Fatalf("empty thread: %d %s", resp.StatusCode, body)
	}

	// A foreign identity's thread is not found, not forbidden.
	foreign := "de305d54-75b4-431b-adb2-eb6b9e546302"
	if err := db.Exec(
		`INSERT INTO w_workagent_thread (uid, uuid, name, cloud_sync_state) VALUES (?, ?, 'Not yours', 'local')`,
		localSingleUserUID+7, foreign,
	).Error; err != nil {
		t.Fatal(err)
	}
	resp, _ = localAccountsRequest(
		t, http.MethodPost, base+"/agent/threads/"+foreign+"/export", "tok", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("foreign thread: %d", resp.StatusCode)
	}

	// Garbage uuid → 400 before anything is touched.
	resp, _ = localAccountsRequest(
		t, http.MethodPost, base+"/agent/threads/not-a-uuid/export", "tok", "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad uuid: %d", resp.StatusCode)
	}
}
