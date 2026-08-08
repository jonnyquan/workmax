package workagent

import (
	"encoding/base64"
	"testing"
	"time"
)

// Cursor format is opaque to clients but the round-trip must be
// stable across the time-zone and monotonic-clock corners that bite
// Go's time.Time when you naively format it as a string. Encoding
// via UnixNano + decoding back via time.Unix(0, n) sidesteps all of
// those — pin the contract here so a future "let's prettify the
// cursor format" change can't quietly break in-flight cursors.
func TestConversationsCursor_RoundTrip(t *testing.T) {
	cases := []struct {
		name string
		ts   time.Time
		id   uint
	}{
		{"epoch", time.Unix(0, 0), 1},
		{"recent", time.Date(2026, 4, 29, 12, 30, 45, 123456789, time.UTC), 9999},
		{"location-east", time.Date(2026, 4, 29, 12, 30, 45, 123456789, time.FixedZone("CST", 8*3600)), 42},
		{"max-id", time.Now(), ^uint(0) >> 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cursor := encodeConversationsCursor(tc.ts, tc.id)
			gotTs, gotID, err := decodeConversationsCursor(cursor)
			if err != nil {
				t.Fatalf("decode error: %v", err)
			}
			// Compare via UnixNano because the decoded time is in UTC
			// (time.Unix(0, n)) — equivalent for ordering, just not
			// reflexively .Equal() on the wall-clock representation.
			if gotTs.UnixNano() != tc.ts.UnixNano() {
				t.Errorf("ts mismatch: got %d want %d", gotTs.UnixNano(), tc.ts.UnixNano())
			}
			if gotID != tc.id {
				t.Errorf("id mismatch: got %d want %d", gotID, tc.id)
			}
		})
	}
}

// Opaque cursors are sent back unmodified by clients, but a hostile
// or stale client may submit garbage. Each branch must fail with a
// non-nil error, not return zero-values that the query then runs
// against — a malformed cursor that decoded to (time.Time{}, 0)
// would silently degrade into "show me everything" instead of an
// auth-checkable failure.
func TestConversationsCursor_RejectsMalformed(t *testing.T) {
	cases := []struct {
		name   string
		cursor string
	}{
		{"not-base64", "this is not base64!"},
		{"base64-no-pipe", base64.RawURLEncoding.EncodeToString([]byte("1700000000000000000"))},
		{"base64-non-numeric-ts", base64.RawURLEncoding.EncodeToString([]byte("abc|123"))},
		{"base64-non-numeric-id", base64.RawURLEncoding.EncodeToString([]byte("1700000000000000000|nope"))},
		{"empty-after-decode", base64.RawURLEncoding.EncodeToString([]byte(""))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := decodeConversationsCursor(tc.cursor); err == nil {
				t.Errorf("expected error decoding %q", tc.cursor)
			}
		})
	}
}
