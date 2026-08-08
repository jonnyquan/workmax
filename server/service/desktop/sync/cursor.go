// Cursor + envelope machinery for the desktop pull-sync endpoints.
// See doc.go for the package-level bi-consumption contract and
// ProjectDocs/writer-work-agent-plugin-platform-design-2026-08.md for
// the platform-level design rationale.
//
// P1.A.1 (this file) lands the shared cursor machinery; P1.A.2-.4
// use it for the four sync endpoints.
package sync

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Cursor is the opaque pagination + incremental-sync handle the
// sync endpoints emit + consume. Encoded as base64url(JSON) on the
// wire so the client treats it as opaque; structure is documented
// here for backend implementers.
//
// Wire JSON (before base64url-encoding):
//
//	{"updated_at": "2026-05-17T22:25:00.123Z", "id": 12345}
//	{"updated_at": ..., "id": ..., "tombstone": {"updated_at": ..., "id": ...}}
//
// `updated_at` is the row's RFC3339Nano timestamp; `id` is the
// row's PK. Both are required — the ORDER BY for every sync
// endpoint is `(updated_at ASC, id ASC)`, so the cursor must
// disambiguate when multiple rows share a millisecond.
//
// P1.A.5b: Tombstone sub-cursor (optional) lets a single endpoint
// merge two independent streams (upserts from the entity table +
// deletes from w_workagent_tombstone) using one cursor on the wire.
// Each stream has its own (updated_at, id) pair; the outer cursor
// is the upsert stream + the nested Tombstone is the delete stream.
// Nil Tombstone = no tombstone tracking (early upsert-only sidecar
// builds, or endpoints that don't have a delete story).
//
// Design decision: keep the wire JSON minimal. The Tombstone field
// is omitempty so legacy cursors round-trip unchanged.
type Cursor struct {
	UpdatedAt time.Time `json:"updated_at"`
	ID        int64     `json:"id"`
	Tombstone *Cursor   `json:"tombstone,omitempty"`
}

// EncodeCursor returns the base64url-encoded JSON form of c. Empty
// input (zero time + zero ID) is valid and round-trips, but the
// endpoint layer should treat it the same as "no cursor at all"
// (= full sync from beginning). See DecodeCursor for the inverse.
//
// Why base64url (vs raw base64): cursor strings appear in URL query
// params; the '+' / '/' chars in standard base64 would need
// percent-encoding which most clients screw up. base64url uses '-'
// and '_' instead, URL-safe by construction.
func EncodeCursor(c Cursor) (string, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("sync cursor: marshal: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// DecodeCursor parses an encoded cursor back into a Cursor. Empty
// string returns the zero value + nil error — endpoints treat zero
// cursor as "full sync from beginning", same as no cursor passed.
//
// Validation:
//   - Must be valid base64url (any padding rejected per
//     base64.RawURLEncoding semantics — standard base64 with '='
//     padding will fail decode)
//   - Must JSON-decode to {updated_at, id}
//   - updated_at must not be > 1 minute in the future (clients
//     shouldn't be inventing cursors with future timestamps; this
//     catches clock-skew bugs cheaply without rejecting legitimate
//     near-real-time cursors)
//   - id may be 0 (covers the case "first row at this timestamp")
//
// Returns one of the documented sentinel errors so callers can
// distinguish "client sent garbage" from "this endpoint hit an
// internal bug".
func DecodeCursor(encoded string) (Cursor, error) {
	if encoded == "" {
		return Cursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return Cursor{}, fmt.Errorf("%w: %v", ErrCursorBadEncoding, err)
	}
	var c Cursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return Cursor{}, fmt.Errorf("%w: %v", ErrCursorBadJSON, err)
	}
	if c.UpdatedAt.IsZero() {
		return Cursor{}, fmt.Errorf("%w: updated_at is zero", ErrCursorMissingField)
	}
	// Time can be in any zone — we normalize to UTC here so callers
	// can `WHERE updated_at >= ?` without worrying about TZ mismatch
	// with the database's stored UTC values.
	c.UpdatedAt = c.UpdatedAt.UTC()
	if c.UpdatedAt.After(time.Now().UTC().Add(time.Minute)) {
		return Cursor{}, fmt.Errorf("%w: updated_at is > 1 min in the future", ErrCursorFutureTime)
	}
	return c, nil
}

// IsZero reports whether c is the zero-cursor (no incremental
// resume point). Endpoint code uses this to branch "full sync"
// vs "delta sync".
func (c Cursor) IsZero() bool {
	return c.UpdatedAt.IsZero() && c.ID == 0
}

// Sentinel errors for cursor parsing. Wrap-able with %w so callers
// can errors.Is-check them.
var (
	ErrCursorBadEncoding  = errors.New("sync cursor: bad base64url encoding")
	ErrCursorBadJSON      = errors.New("sync cursor: bad JSON")
	ErrCursorMissingField = errors.New("sync cursor: missing required field")
	ErrCursorFutureTime   = errors.New("sync cursor: timestamp is in the future")
)
