//go:build desktop

package cloud_proxy

import (
	"encoding/json"
	"testing"
	"time"

	svc "server/service/desktop/sync"
)

// TestWireContract_ThreadDelta pins that the JSON the cloud emits
// from svc.ThreadDeltaRow + the listThreadsItem wrapper can be
// decoded losslessly by the sidecar's ThreadDeltaItem.
//
// Without this test, schema drift looks like: cloud adds/renames
// a field → sidecar silently parses it as zero → ops sees no error
// → users see missing data in the desktop UI. The P1.B.6 prereq
// (proxy.go URL pointed at /api/workagent instead of /api/work-agent)
// was the same flavor of drift, caught only because the composer
// stopped working end-to-end.
//
// Coverage strategy: construct a row with EVERY field populated
// to a distinct non-zero value, round-trip through JSON, and assert
// the sidecar's parsed struct echoes them back. A new field on
// either side that breaks this contract trips the test loudly.
func TestWireContract_ThreadDelta(t *testing.T) {
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	cloud := struct {
		Action string `json:"action"`
		svc.ThreadDeltaRow
	}{
		Action: "upsert",
		ThreadDeltaRow: svc.ThreadDeltaRow{
			CloudThreadID: "42",
			UUID:          "thr-uuid-abc",
			Name:          "Test Thread",
			AgentMode:     "ppt",
			AgentType:     "general_agent",
			Model:         "work-pro",
			MessageCount:  7,
			MsgPreview:    "hello world",
			FileCount:     3,
			IsPublic:      true,
			UpdatedAt:     now,
			CreatedAt:     now.Add(-time.Hour),
		},
	}

	wire, err := json.Marshal(cloud)
	if err != nil {
		t.Fatal(err)
	}

	var got ThreadDeltaItem
	if err := json.Unmarshal(wire, &got); err != nil {
		t.Fatalf("sidecar decode failed: %v\nwire: %s", err, wire)
	}

	// Action + identifiers
	if got.Action != "upsert" {
		t.Errorf("Action: got %q, want %q", got.Action, "upsert")
	}
	if got.CloudThreadID != "42" {
		t.Errorf("CloudThreadID: %q", got.CloudThreadID)
	}
	if got.UUID != "thr-uuid-abc" {
		t.Errorf("UUID: %q", got.UUID)
	}

	// Display + meta
	if got.Name != "Test Thread" || got.AgentMode != "ppt" || got.AgentType != "general_agent" || got.Model != "work-pro" {
		t.Errorf("display+meta drift: %+v", got)
	}

	// Counters + flag
	if got.MessageCount != 7 || got.MsgPreview != "hello world" || got.FileCount != 3 || !got.IsPublic {
		t.Errorf("counters+flag drift: %+v", got)
	}

	// Timestamps — sidecar keeps them as strings (RFC3339 from cloud).
	// We don't assert exact format, only non-empty + RFC3339-parseable.
	if got.UpdatedAt == "" || got.CreatedAt == "" {
		t.Errorf("timestamps empty: updated=%q created=%q", got.UpdatedAt, got.CreatedAt)
	}
	if _, err := time.Parse(time.RFC3339, got.UpdatedAt); err != nil {
		t.Errorf("UpdatedAt not RFC3339: %q (%v)", got.UpdatedAt, err)
	}
	if _, err := time.Parse(time.RFC3339, got.CreatedAt); err != nil {
		t.Errorf("CreatedAt not RFC3339: %q (%v)", got.CreatedAt, err)
	}
}

// TestWireContract_MessageDelta — same shape pin for the
// messages delta endpoint.
func TestWireContract_MessageDelta(t *testing.T) {
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	cloud := struct {
		Action string `json:"action"`
		svc.MessageDeltaRow
	}{
		Action: "upsert",
		MessageDeltaRow: svc.MessageDeltaRow{
			CloudMessageID:    "msg-99",
			UUID:              "msg-uuid-z",
			ThreadUUID:        "thr-uuid-abc",
			UserText:          "hello",
			AIText:            "hi back",
			ChatMode:          "chat",
			ContentType:       "text",
			StructuredContent: `{"blocks":[]}`,
			Actions:           `[{"id":"a1"}]`,
			Metadata:          `{"k":"v"}`,
			UseImages:         `["url1"]`,
			UseFiles:          `["fid1"]`,
			UserRating:        5,
			UserFeedback:      "great",
			UpdatedAt:         now,
			CreatedAt:         now.Add(-2 * time.Hour),
		},
	}

	wire, err := json.Marshal(cloud)
	if err != nil {
		t.Fatal(err)
	}

	var got MessageDeltaItem
	if err := json.Unmarshal(wire, &got); err != nil {
		t.Fatalf("sidecar decode failed: %v\nwire: %s", err, wire)
	}

	if got.Action != "upsert" {
		t.Errorf("Action: %q", got.Action)
	}
	if got.CloudMessageID != "msg-99" || got.UUID != "msg-uuid-z" || got.ThreadUUID != "thr-uuid-abc" {
		t.Errorf("identifiers drift: %+v", got)
	}
	if got.UserText != "hello" || got.AIText != "hi back" || got.ChatMode != "chat" {
		t.Errorf("text drift: %+v", got)
	}
	if got.ContentType != "text" {
		t.Errorf("ContentType: %q", got.ContentType)
	}
	// JSON-blob fields are forwarded as raw strings; verify they survive
	// the encode → decode → re-encode loop without alteration.
	if got.StructuredContent != `{"blocks":[]}` {
		t.Errorf("StructuredContent: %q", got.StructuredContent)
	}
	if got.Actions != `[{"id":"a1"}]` {
		t.Errorf("Actions: %q", got.Actions)
	}
	if got.Metadata != `{"k":"v"}` {
		t.Errorf("Metadata: %q", got.Metadata)
	}
	if got.UseImages != `["url1"]` || got.UseFiles != `["fid1"]` {
		t.Errorf("file/image arrays drift: %+v", got)
	}
	if got.UserRating != 5 || got.UserFeedback != "great" {
		t.Errorf("rating drift: %+v", got)
	}
	if got.UpdatedAt == "" || got.CreatedAt == "" {
		t.Errorf("timestamps empty: %+v", got)
	}
}

// TestWireContract_DeltaPage_Envelope pins the wrapper shape
// (items / next_cursor / has_more / server_time). The cloud's
// listThreadsResponse + listMessagesResponse share this envelope;
// sidecar's ThreadsDeltaPage + MessagesDeltaPage decode against it.
func TestWireContract_DeltaPage_Envelope(t *testing.T) {
	// Use a minimal raw JSON to assert the field names + types.
	// If the cloud renames "next_cursor" → "cursor", this fails.
	raw := []byte(`{
		"items": [],
		"next_cursor": "abc",
		"has_more": true,
		"server_time": "2026-05-19T12:00:00Z"
	}`)

	var threads ThreadsDeltaPage
	if err := json.Unmarshal(raw, &threads); err != nil {
		t.Fatal(err)
	}
	if threads.NextCursor != "abc" || !threads.HasMore || threads.ServerTime == "" {
		t.Errorf("ThreadsDeltaPage envelope: %+v", threads)
	}

	var messages MessagesDeltaPage
	if err := json.Unmarshal(raw, &messages); err != nil {
		t.Fatal(err)
	}
	if messages.NextCursor != "abc" || !messages.HasMore || messages.ServerTime == "" {
		t.Errorf("MessagesDeltaPage envelope: %+v", messages)
	}
}
