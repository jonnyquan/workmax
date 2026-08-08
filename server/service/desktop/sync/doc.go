// Package sync is intentionally bi-consumed by the cloud handlers
// AND the desktop sidecar. The same Cursor encoding, the same
// tombstone schema, and the same delta-merge semantics live here so
// the two sides can't drift.
//
// CONSUMERS
//
//   - Cloud handlers (server/api/desktop/sync/*) — always compiled.
//     Read/write w_workagent_tombstone in MySQL; emit cursors that
//     the sidecar then echoes back on the next request.
//
//   - Desktop sidecar (server/desktop/sync/*) — only compiled
//     under `//go:build desktop`. Decodes the same cursor, persists
//     to local SQLite via the //go:build-tagged readers.
//
// BUILD-TAG SAFETY
//
// This file deliberately has no build constraint — everything in
// this package must compile in BOTH the prod-cloud build and the
// `-tags desktop` sidecar build. Adding a cloud-only or sidecar-only
// import here splits the schema; resist the temptation. If you need
// per-side glue code, put it in the consuming package (server/api/...
// for cloud, server/desktop/sync/... for sidecar) and keep this
// package's surface narrow + symmetric.
//
// WIRE SHAPE
//
// Cursor encoding is the contract: any change here MUST be tested
// on both sides (cloud cursor_test.go + sidecar tests that round-
// trip a real delta). The cursor recursion (Cursor.Tombstone *Cursor)
// is load-bearing — see cursor.go for the format details and
// comparison rules.
//
// WHY THE SPLIT
//
// We didn't duplicate the cursor + tombstone repo into two packages
// because:
//   - The cursor is an opaque protocol — two implementations would
//     drift the moment one side adds a field.
//   - PruneTombstones runs ONLY on the cloud side (via the hourly
//     scheduler), so the cloud needs it; sidecar never deletes
//     tombstones — it only consumes them.
//   - Tombstone repo writes happen in cloud delete handlers'
//     transactions; reading them happens in both cloud (delta
//     queries) and sidecar (consuming the merged delta).
//
// The cost of bi-consumption is the discipline above. The benefit
// is that there's exactly ONE source of truth for the sync wire
// shape — which is the entire point of the package.
package sync
