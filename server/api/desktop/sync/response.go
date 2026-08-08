// Package sync owns the desktop client's pull-sync HTTP endpoints.
// The platform-level wire contract is documented in
// ProjectDocs/writer-work-agent-plugin-platform-design-2026-08.md.
//
// P1.A.1 (this file) lands the shared envelope all 4 sync endpoints
// (P1.A.2-.4) emit; sync_router.go wires the empty group.
package sync

import (
	"time"

	"server/service/desktop/sync"
)

// Action is the closed enum of per-item operations the wire emits.
// Mirrors cloud-sync.md §5.1.
type Action string

const (
	ActionUpsert Action = "upsert"
	ActionDelete Action = "delete"
)

// Envelope is the shared response shape for every /api/desktop/sync/*
// endpoint. Items is endpoint-specific (each endpoint declares its
// own item type as `any` here, but the concrete type appears in the
// per-endpoint response struct that embeds Envelope).
//
// Per cloud-sync.md §5.1:
//   - items: row deltas (upsert or delete) since the request cursor
//   - next_cursor: cursor to pass on the next page's request. Empty
//     when has_more=false (the caller resumes from this same point
//     next time around).
//   - has_more: true if the server truncated to limit and there's
//     more data behind. False = caller has caught up.
//   - server_time: server's UTC wall-clock at response time. Lets
//     the client measure clock skew vs cursor freshness.
//
// We use `any` for Items at this layer; the per-endpoint package
// (P1.A.2 onwards) will define typed envelopes via embedding.
type Envelope struct {
	Items      []any  `json:"items"`
	NextCursor string `json:"next_cursor"`
	HasMore    bool   `json:"has_more"`
	ServerTime string `json:"server_time"` // RFC3339Nano UTC
}

// NewEnvelope constructs an Envelope with server_time stamped to now.
// The caller fills Items + NextCursor + HasMore based on the query
// result. Reduces the chance of forgetting server_time across N
// endpoints.
//
// Nil items is coerced to []any{} so the wire emits `"items": []`
// rather than `"items": null`. Clients have to handle "no results"
// as a normal-shape case anyway; emitting null forces them to add
// a `?? []` branch everywhere.
func NewEnvelope(items []any, nextCursor string, hasMore bool) Envelope {
	if items == nil {
		items = []any{}
	}
	return Envelope{
		Items:      items,
		NextCursor: nextCursor,
		HasMore:    hasMore,
		ServerTime: time.Now().UTC().Format(time.RFC3339Nano),
	}
}

// MaxLimit caps the limit query param all sync endpoints accept
// (cloud-sync.md §5.1: limit ≤ 500). Centralized here so a future
// product decision to lower/raise is a one-line change.
const MaxLimit = 500

// DefaultLimit is the limit used when the caller doesn't pass one.
// Tuned for the chat / thread surface: 100 is enough to render the
// renderer's first paint without scrolling; large enough that a
// fresh-install power user finishes thread list sync in ≤ 50 pages.
const DefaultLimit = 100

// ClampLimit normalizes the caller-supplied limit:
//   - <= 0 (incl. missing) → DefaultLimit
//   - > MaxLimit → MaxLimit
//   - else → unchanged
//
// Endpoint handlers call this on every request so the policy is
// consistent across all 4 sync endpoints (and the next one we add).
func ClampLimit(raw int) int {
	switch {
	case raw <= 0:
		return DefaultLimit
	case raw > MaxLimit:
		return MaxLimit
	default:
		return raw
	}
}

// EncodeNextCursor is a thin wrapper around sync.EncodeCursor that
// returns "" on error (logged at caller) so handler code can do
// `env.NextCursor = EncodeNextCursor(c)` without branching. A
// failure here is unrecoverable (we just marshaled the Cursor
// successfully in the SQL row → encode), so the empty string
// short-circuits the client into restarting at the cursor it
// already has, which is fine.
func EncodeNextCursor(c sync.Cursor) string {
	s, err := sync.EncodeCursor(c)
	if err != nil {
		return ""
	}
	return s
}
