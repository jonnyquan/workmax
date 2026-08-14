//go:build desktop

package desktop

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	migrationsdesktop "server/desktop/migrations_desktop"
)

const testProjectThreadUUID = "00000000-0000-4000-8000-00000000a001"

func openProjectTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:project-"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := migrationsdesktop.Apply(db); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	// Seed a thread the project operations will target.
	seedThread(t, db, 0, testProjectThreadUUID, "Project thread", "general", 0)
	return db
}

func TestThreadProject_SetAndClear(t *testing.T) {
	db := openProjectTestDB(t)
	token := "project-test-token-minimum-32-chars!!"
	srv, err := NewServer(ServerConfig{
		SidecarVersion: "test",
		LocalToken:     token,
		DB:             db,
		DeviceID:       "device-test",
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ts := httptest.NewServer(srv.httpServer.Handler)
	t.Cleanup(ts.Close)

	put := func(body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPut, ts.URL+"/agent/threads/"+testProjectThreadUUID+"/project", strings.NewReader(body))
		req.Header.Set("X-Local-Token", token)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("PUT: %v", err)
		}
		defer res.Body.Close()
		raw := make([]byte, 1024)
		n, _ := res.Body.Read(raw)
		return res.StatusCode, string(raw[:n])
	}
	del := func() (int, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/agent/threads/"+testProjectThreadUUID+"/project", nil)
		req.Header.Set("X-Local-Token", token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("DELETE: %v", err)
		}
		defer res.Body.Close()
		raw := make([]byte, 1024)
		n, _ := res.Body.Read(raw)
		return res.StatusCode, string(raw[:n])
	}

	// Set a project
	status, body := put(`{"project_key":"Q3 Report"}`)
	if status != http.StatusOK {
		t.Fatalf("PUT project = %d %s", status, body)
	}
	var resp map[string]string
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp["project_key"] != "Q3 Report" {
		t.Fatalf("project_key = %q", resp["project_key"])
	}

	// The thread list now carries the project
	listReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/agent/threads?include_paused=true", nil)
	listReq.Header.Set("X-Local-Token", token)
	listRes, err := http.DefaultClient.Do(listReq)
	if err != nil {
		t.Fatalf("GET threads: %v", err)
	}
	var list struct {
		Items []LocalThreadRow `json:"items"`
	}
	if err := json.NewDecoder(listRes.Body).Decode(&list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	listRes.Body.Close()
	if len(list.Items) == 0 || list.Items[0].Project != "Q3 Report" {
		t.Fatalf("thread list project = %q, want 'Q3 Report'", list.Items[0].Project)
	}

	// Invalid: empty key
	if status, _ := put(`{"project_key":""}`); status != http.StatusBadRequest {
		t.Fatalf("empty key = %d, want 400", status)
	}
	// Invalid: unknown field
	if status, _ := put(`{"project_key":"x","extra":1}`); status != http.StatusBadRequest {
		t.Fatalf("unknown field = %d, want 400", status)
	}

	// Clear
	status, body = del()
	if status != http.StatusOK {
		t.Fatalf("DELETE project = %d %s", status, body)
	}

	// Thread list no longer carries the project
	listReq2, _ := http.NewRequest(http.MethodGet, ts.URL+"/agent/threads?include_paused=true", nil)
	listReq2.Header.Set("X-Local-Token", token)
	listRes2, err := http.DefaultClient.Do(listReq2)
	if err != nil {
		t.Fatalf("GET threads after clear: %v", err)
	}
	var list2 struct {
		Items []LocalThreadRow `json:"items"`
	}
	if err := json.NewDecoder(listRes2.Body).Decode(&list2); err != nil {
		t.Fatalf("decode list2: %v", err)
	}
	listRes2.Body.Close()
	if len(list2.Items) == 0 || list2.Items[0].Project != "" {
		t.Fatalf("after clear, project = %q, want empty", list2.Items[0].Project)
	}
}

func TestThreadProject_OwnershipCheck(t *testing.T) {
	db := openProjectTestDB(t)
	token := "project-owner-token-minimum-32-chars"
	srv, err := NewServer(ServerConfig{
		SidecarVersion: "test",
		LocalToken:     token,
		DB:             db,
		DeviceID:       "device-test",
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ts := httptest.NewServer(srv.httpServer.Handler)
	t.Cleanup(ts.Close)

	// A made-up UUID that does not belong to this identity
	req, _ := http.NewRequest(http.MethodPut,
		ts.URL+"/agent/threads/00000000-0000-4000-8000-00000000dead/project",
		strings.NewReader(`{"project_key":"hack"}`))
	req.Header.Set("X-Local-Token", token)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("foreign thread = %d, want 404", res.StatusCode)
	}
}

