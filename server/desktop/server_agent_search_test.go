//go:build desktop

package desktop

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestSearchMessagesFindsBothHalves(t *testing.T) {
	base, db, _, _ := newDeleteFixture(t)
	threadUUID := "de305d54-75b4-431b-adb2-eb6b9e546501"
	threadID := seedLocalThreadForDelete(t, db, localSingleUserUID, threadUUID)
	if err := db.Exec(
		`UPDATE w_workagent_thread SET name = 'Q3 复盘' WHERE id = ?`, threadID,
	).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(
		`INSERT INTO w_workagent_message (uid, uuid, thread_id, user_text, ai_text)
		 VALUES (?, 'm-s1', ?, '请分析 churn 数字', 'Churn 环比持平于 2.1%，主要来自 self-serve 层')`,
		localSingleUserUID, threadID,
	).Error; err != nil {
		t.Fatal(err)
	}

	search := func(q string) (int, searchResponse) {
		t.Helper()
		resp, body := localAccountsRequest(
			t, http.MethodGet, base+"/agent/search?q="+url.QueryEscape(q), "tok", "")
		var parsed searchResponse
		_ = json.Unmarshal(body, &parsed)
		return resp.StatusCode, parsed
	}

	// Case-insensitive, and a row matching in both halves yields one result
	// per half — who said it changes what the snippet means.
	status, result := search("CHURN")
	if status != http.StatusOK || result.Count != 2 {
		t.Fatalf("churn: status %d result %+v", status, result)
	}
	roles := map[string]bool{}
	for _, item := range result.Items {
		roles[item.Role] = true
		if item.ThreadUUID != threadUUID || item.ThreadName != "Q3 复盘" {
			t.Fatalf("item misattributed: %+v", item)
		}
		if !strings.Contains(strings.ToLower(item.Snippet), "churn") {
			t.Fatalf("snippet lacks the match: %q", item.Snippet)
		}
	}
	if !roles["you"] || !roles["assistant"] {
		t.Fatalf("expected one match per half, got %+v", result.Items)
	}

	// CJK matches as a plain substring.
	status, result = search("环比持平")
	if status != http.StatusOK || result.Count != 1 || result.Items[0].Role != "assistant" {
		t.Fatalf("cjk: status %d result %+v", status, result)
	}

	// A literal, never a pattern: under LIKE semantics "_" matches ANY
	// character (so it would hit every row); under substring semantics it
	// matches only a literal underscore, which the seeded text lacks. And a
	// literal "%" must find the actual percent sign in "2.1%".
	status, result = search("_")
	if status != http.StatusOK || result.Count != 0 {
		t.Fatalf("wildcard must be literal: status %d result %+v", status, result)
	}
	status, result = search("2.1%")
	if status != http.StatusOK || result.Count != 1 {
		t.Fatalf("literal percent must match itself: status %d result %+v", status, result)
	}
}

func TestSearchMessagesScopeAndValidation(t *testing.T) {
	base, db, _, _ := newDeleteFixture(t)
	mine := seedLocalThreadForDelete(t, db, localSingleUserUID, "de305d54-75b4-431b-adb2-eb6b9e546502")
	if err := db.Exec(
		`INSERT INTO w_workagent_message (uid, uuid, thread_id, user_text, ai_text)
		 VALUES (?, 'm-s2', ?, 'my secret plan', 'ok')`,
		localSingleUserUID, mine,
	).Error; err != nil {
		t.Fatal(err)
	}
	// Same words under a DIFFERENT identity: must never surface.
	foreignUID := localSingleUserUID + 7
	if err := db.Exec(
		`INSERT INTO w_workagent_thread (uid, uuid, name, cloud_sync_state) VALUES (?, 'de305d54-75b4-431b-adb2-eb6b9e546503', 'Foreign', 'local')`,
		foreignUID,
	).Error; err != nil {
		t.Fatal(err)
	}
	var foreignThreadID uint64
	if err := db.Raw(`SELECT id FROM w_workagent_thread WHERE uid = ?`, foreignUID).Row().Scan(&foreignThreadID); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(
		`INSERT INTO w_workagent_message (uid, uuid, thread_id, user_text, ai_text)
		 VALUES (?, 'm-s3', ?, 'my secret plan too', 'ok')`,
		foreignUID, foreignThreadID,
	).Error; err != nil {
		t.Fatal(err)
	}

	resp, body := localAccountsRequest(
		t, http.MethodGet, base+"/agent/search?q="+url.QueryEscape("secret plan"), "tok", "")
	var parsed searchResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp.StatusCode != http.StatusOK || parsed.Count != 1 {
		t.Fatalf("scope: status %d result %+v", resp.StatusCode, parsed)
	}
	if parsed.Items[0].ThreadName != "Doomed" {
		t.Fatalf("foreign identity's message surfaced: %+v", parsed.Items[0])
	}

	// Validation: empty and oversized queries are 400.
	for _, bad := range []string{"", "   ", strings.Repeat("长", maxSearchQueryChars+1)} {
		resp, _ := localAccountsRequest(
			t, http.MethodGet, base+"/agent/search?q="+url.QueryEscape(bad), "tok", "")
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("query %q: status %d, want 400", bad, resp.StatusCode)
		}
	}
}

func TestSearchSnippetWindows(t *testing.T) {
	long := strings.Repeat("前", 100) + "目标词" + strings.Repeat("后", 100)
	snippet, ok := searchSnippet(long, "目标词")
	if !ok {
		t.Fatal("no match")
	}
	if !strings.Contains(snippet, "目标词") {
		t.Fatalf("snippet lost the match: %q", snippet)
	}
	if !strings.HasPrefix(snippet, "…") || !strings.HasSuffix(snippet, "…") {
		t.Fatalf("long text must elide both ends: %q", snippet)
	}
	if n := len([]rune(snippet)); n > 2*searchSnippetRuneWing+10 {
		t.Fatalf("snippet too wide: %d runes", n)
	}
	if _, ok := searchSnippet("nothing here", "missing"); ok {
		t.Fatal("must not fabricate a snippet")
	}
}
