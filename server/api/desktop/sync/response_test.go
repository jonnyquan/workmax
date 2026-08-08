package sync

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"server/service/desktop/sync"
)

func TestNewEnvelope_FieldOrderStable(t *testing.T) {
	// Contract test: the JSON field order matters because clients
	// might assume it for log scraping / debugging. Pin the order
	// so a refactor that switches Envelope to a map[string]any
	// (which would jumble order) gets caught.
	env := NewEnvelope([]any{
		map[string]any{"action": "upsert", "id": 1},
	}, "next-cursor-here", true)

	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	// Expect the canonical order: items, next_cursor, has_more, server_time
	wantOrder := []string{`"items":`, `"next_cursor":`, `"has_more":`, `"server_time":`}
	pos := 0
	for _, key := range wantOrder {
		idx := strings.Index(got[pos:], key)
		if idx < 0 {
			t.Errorf("missing field %q in: %s", key, got)
			continue
		}
		pos += idx + len(key)
	}
}

func TestNewEnvelope_ServerTimeIsRFC3339NanoUTC(t *testing.T) {
	env := NewEnvelope(nil, "", false)
	parsed, err := time.Parse(time.RFC3339Nano, env.ServerTime)
	if err != nil {
		t.Fatalf("server_time should be RFC3339Nano-parseable, got %q (%v)", env.ServerTime, err)
	}
	if parsed.Location() != time.UTC {
		t.Errorf("server_time should be UTC, got %v", parsed.Location())
	}
	// Should be within the last second.
	if delta := time.Since(parsed).Abs(); delta > time.Second {
		t.Errorf("server_time should be near now, off by %v", delta)
	}
}

func TestNewEnvelope_NilItemsRoundTripsAsEmptyArray(t *testing.T) {
	// Defensive: a sync endpoint with zero results shouldn't emit
	// "items": null (which clients have to handle separately from
	// "items": []). The standard json.Marshal renders nil slice as
	// null — this test will fail unless we adjust.
	env := NewEnvelope(nil, "", false)
	raw, _ := json.Marshal(env)
	if strings.Contains(string(raw), `"items":null`) {
		t.Errorf("nil items should serialize as [], got: %s", raw)
		t.Log("HINT: change NewEnvelope to coerce nil → empty slice before assignment")
	}
}

func TestClampLimit(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{
		{0, DefaultLimit},
		{-5, DefaultLimit},
		{1, 1},
		{50, 50},
		{DefaultLimit, DefaultLimit},
		{MaxLimit, MaxLimit},
		{MaxLimit + 1, MaxLimit},
		{99_999, MaxLimit},
	}
	for _, tc := range cases {
		if got := ClampLimit(tc.in); got != tc.want {
			t.Errorf("ClampLimit(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestEncodeNextCursor_HappyPath(t *testing.T) {
	c := sync.Cursor{UpdatedAt: time.Now().UTC(), ID: 42}
	s := EncodeNextCursor(c)
	if s == "" {
		t.Fatal("expected encoded cursor, got empty")
	}
	// Round-trip via the source-of-truth decoder.
	back, err := sync.DecodeCursor(s)
	if err != nil {
		t.Fatalf("encoded cursor doesn't round-trip: %v", err)
	}
	if back.ID != c.ID {
		t.Errorf("id round-trip: got %d, want %d", back.ID, c.ID)
	}
}

func TestActionConstants(t *testing.T) {
	// Pin the wire values — these are part of the API contract;
	// renaming would silently break every client that does
	// `if action === "upsert"`.
	if ActionUpsert != "upsert" {
		t.Errorf("ActionUpsert: got %q, want upsert", ActionUpsert)
	}
	if ActionDelete != "delete" {
		t.Errorf("ActionDelete: got %q, want delete", ActionDelete)
	}
}
