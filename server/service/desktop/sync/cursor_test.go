package sync

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCursor_RoundTrip(t *testing.T) {
	cases := []Cursor{
		{UpdatedAt: time.Date(2026, 5, 17, 22, 25, 0, 123_000_000, time.UTC), ID: 12345},
		{UpdatedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), ID: 1},
		// id=0 is legitimate ("first row at this timestamp")
		{UpdatedAt: time.Date(2026, 5, 17, 22, 25, 0, 0, time.UTC), ID: 0},
	}
	for _, want := range cases {
		t.Run(want.UpdatedAt.Format(time.RFC3339), func(t *testing.T) {
			encoded, err := EncodeCursor(want)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			got, err := DecodeCursor(encoded)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if !got.UpdatedAt.Equal(want.UpdatedAt) {
				t.Errorf("updated_at: got %v, want %v", got.UpdatedAt, want.UpdatedAt)
			}
			if got.ID != want.ID {
				t.Errorf("id: got %d, want %d", got.ID, want.ID)
			}
		})
	}
}

func TestDecodeCursor_EmptyStringIsZero(t *testing.T) {
	got, err := DecodeCursor("")
	if err != nil {
		t.Fatalf("empty cursor should not error: %v", err)
	}
	if !got.IsZero() {
		t.Errorf("empty cursor should be zero, got %+v", got)
	}
}

func TestDecodeCursor_NormalizesToUTC(t *testing.T) {
	loc, _ := time.LoadLocation("America/New_York")
	src := Cursor{UpdatedAt: time.Date(2026, 5, 17, 18, 25, 0, 0, loc), ID: 1}
	encoded, _ := EncodeCursor(src)
	got, _ := DecodeCursor(encoded)
	if got.UpdatedAt.Location() != time.UTC {
		t.Errorf("decoded cursor should be UTC, got %v", got.UpdatedAt.Location())
	}
	// Same instant, different zone — must still compare equal.
	if !got.UpdatedAt.Equal(src.UpdatedAt) {
		t.Errorf("instant mismatch: got %v, src %v", got.UpdatedAt, src.UpdatedAt)
	}
}

// === Negative cases (the 5 the execution plan calls out) ===

func TestDecodeCursor_BadBase64(t *testing.T) {
	cases := []string{
		"!!!not-base64!!!",
		"contains spaces in middle",
		"=padding-not-allowed", // RawURLEncoding rejects '='
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			_, err := DecodeCursor(in)
			if err == nil {
				t.Fatal("expected error for bad base64")
			}
			if !errors.Is(err, ErrCursorBadEncoding) {
				t.Errorf("expected ErrCursorBadEncoding, got %v", err)
			}
		})
	}
}

func TestDecodeCursor_BadJSON(t *testing.T) {
	// Valid base64url, but not valid JSON.
	bogus := base64.RawURLEncoding.EncodeToString([]byte("definitely not json"))
	_, err := DecodeCursor(bogus)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrCursorBadJSON) {
		t.Errorf("expected ErrCursorBadJSON, got %v", err)
	}
}

func TestDecodeCursor_MissingUpdatedAt(t *testing.T) {
	// JSON without updated_at field.
	bogus := base64.RawURLEncoding.EncodeToString([]byte(`{"id": 42}`))
	_, err := DecodeCursor(bogus)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrCursorMissingField) {
		t.Errorf("expected ErrCursorMissingField, got %v", err)
	}
}

func TestDecodeCursor_FutureTimestamp(t *testing.T) {
	// Cursor 10 minutes in the future — clearly bogus.
	src := Cursor{UpdatedAt: time.Now().UTC().Add(10 * time.Minute), ID: 1}
	encoded, _ := EncodeCursor(src)
	_, err := DecodeCursor(encoded)
	if err == nil {
		t.Fatal("expected error for future timestamp")
	}
	if !errors.Is(err, ErrCursorFutureTime) {
		t.Errorf("expected ErrCursorFutureTime, got %v", err)
	}
}

func TestDecodeCursor_NearRealtimeNotRejected(t *testing.T) {
	// Cursor 30s in the future — within the 60s grace window for
	// clock skew. Should NOT error.
	src := Cursor{UpdatedAt: time.Now().UTC().Add(30 * time.Second), ID: 1}
	encoded, _ := EncodeCursor(src)
	if _, err := DecodeCursor(encoded); err != nil {
		t.Errorf("30s-future cursor should be accepted (clock skew grace), got: %v", err)
	}
}

func TestCursor_IsZero(t *testing.T) {
	if !(Cursor{}).IsZero() {
		t.Error("default Cursor should be zero")
	}
	if (Cursor{ID: 1}).IsZero() {
		t.Error("Cursor with nonzero ID should NOT be zero")
	}
	if (Cursor{UpdatedAt: time.Now()}).IsZero() {
		t.Error("Cursor with nonzero UpdatedAt should NOT be zero")
	}
}

func TestEncodeCursor_IsURLSafe(t *testing.T) {
	// The encoded string must contain only [A-Za-z0-9_-]. RawURLEncoding
	// guarantees this; we pin the property so a future "switch to
	// StdEncoding" doesn't silently break URL-embedded cursors.
	src := Cursor{UpdatedAt: time.Now().UTC(), ID: 99999999}
	encoded, _ := EncodeCursor(src)
	for _, ch := range encoded {
		switch {
		case ch >= 'A' && ch <= 'Z':
		case ch >= 'a' && ch <= 'z':
		case ch >= '0' && ch <= '9':
		case ch == '-' || ch == '_':
		default:
			t.Errorf("encoded cursor contains non-URL-safe char %q in %q", ch, encoded)
		}
	}
	// Sanity: shouldn't be ridiculously long either.
	if len(encoded) > 100 {
		t.Errorf("encoded cursor unexpectedly long: %d chars", len(encoded))
	}
	if strings.Contains(encoded, "=") {
		t.Errorf("cursor must not contain '=' padding: %q", encoded)
	}
}
