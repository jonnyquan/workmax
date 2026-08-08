# WorkMax Desktop — P1 Mid-Milestone Checkpoint

**Date**: 2026-05-20 (v0.5; v0.1-v0.4 on 2026-05-19)
**Authors**: Jonny + Claude (pair)
**Predecessor**: [SPIKE_REPORT.md](./SPIKE_REPORT.md) (P0 closeout)
**Companion plan**: historical plan removed during Server/Desktop consolidation
**Closeout report**: [P1_COMPLETION_REPORT.md](./P1_COMPLETION_REPORT.md)

> Historical snapshot: references to the former Web/Admin clients describe the
> 2026-05 source baseline. They are not current dependencies or supported
> build commands; current ownership is `server/` + `desktop/` only.

> v0.5 update: renderer now consumes the active-only thread list
> (`/agent/threads?include_paused=false`) by default, so P1.B.4 is
> wired through both sidecar and UI. `desktop/README.md` was refreshed
> for the current electron-builder config and Desktop test commands.
>
> v0.4 update: 1 more slice after v0.3 — P1.B.4 completed the
> `include_paused=false` filter on `/agent/threads`, closing the
> last small partial item in the sidecar read API.
>
> v0.3 update: 2 more slices after v0.2 — P1.B.3.x.3 (threads-job
> tick fires periodic message-sync for top-N recent threads) +
> P1.A.5c (tombstone GC sweeper, 90-day retention). The §6.2
> nice-to-have queue is now drained of items 1 and 2; what
> remains is full WorkAgent mount + external-dep items
> (icon/notarization/non-mac). All 3 tombstone sub-slices
> (P1.A.5a/b/c) are landed end-to-end.
>
> v0.5 update: packaged builds now prefer the bundled static shell under
> `Resources/renderer/en/desktop/index.html`; the hermetic packaged smoke
> verifies seeded cached-history reads with `WORKMAX_CLOUD_BASE=http://127.0.0.1:9`.
> Public-release No-Go still holds for real previously authenticated cache
> evidence using the helper-enforced unreachable sidecar cloud base, real-cloud
> smoke, signing/notarization, and product-scope decisions.
>
> v0.4 update: the app icon asset is now generated at
> `desktop/build/icons/icon.icns` from the reviewed Desktop icon source and wired into
> `electron-builder.yml`; public-release No-Go still holds for bundled renderer,
> signing/notarization, real-cloud smoke, and packaged manual/security gates.
>
> v0.2 update: 4 more slices landed after v0.1 — P1.A.5a + P1.A.5b
> (full delete-propagation loop), P1.A.3 (single-thread full-fetch),
> + markdown rendering. The "deletes silently desync" gap from
> SPIKE_REPORT §2 is fully closed.
>
> Velocity in the days following the P0 spike was high (15+ slices
> landed). This checkpoint snapshots what's in production, what
> works end-to-end, and what's left — so future planning starts
> from a clear picture rather than scrolling through git log.
>
> **Bottom line**: the desktop client is now genuinely usable as a
> thin chat client. Sign in → see your conversations → click one →
> read history → send a new message. Remaining P1 work is
> incremental, not gating.

---

## 1. What landed since SPIKE_REPORT

### 1.1 SPIKE_REPORT cleanup queue (the "deferred" list from §2)

| Item | Status | Commit |
|---|---|---|
| SSE keepalive on `/agent/chat` | ✅ | `e39829f8` |
| `cache_writer` time-driven batch flush (cloud-proxy.md §5.4) | ✅ | `0218040d` |
| Backend `/revoke` handler (RFC 7009) + sidecar logout call | ✅ | `2ad110ea` + `bd6a7104` |
| `electron-builder.yml` + entitlements wired | ✅ | `8fc47ab7` |
| Input-disabled-when-offline UX | ✅ | `4e0c2000` (P1.B.6) |
| Cloud→local thread sync | ✅ | `645be703` (P1.B.3) |
| Cloud→local message sync (on-demand) | ✅ | `8a8d1db0` (P1.B.3.x.2) |
| Mount real WorkAgent UI in DesktopShell | 🟡 slim — read-only thread list + message view + composer + markdown rendering shipped; full WorkAgent integration deferred (parallel edits make it merge-conflict-risky) |
| App icon (`icon.icns`) | ✅ wired as `desktop/build/icons/icon.icns` |
| Notarization workflow | ❌ external Apple Developer ID dependency |
| Windows / Linux builds | ❌ product decision (mac-first) |
| Deletes silently desync | ✅ | `b545f12e` (sidecar) + `394c3334` (cloud) + `e105060f` (GC) |
| Plain `<pre>` for assistant text is ugly | ✅ | `51f34676` (markdown rendering) |
| "User wants everything cached for offline" gap | ✅ | `ded48ba9` (periodic message-sync fan-out) |

**10 of 14 items closed.** Remaining 4 are split: 1 deferred for sequencing
(full WorkAgent integration), 3 external dependencies (icon, notarization,
non-mac platforms).

### 1.2 P1 plan slices (execution-plan-p1.md)

| Slice | Status | Commit |
|---|---|---|
| **P1.A.1** cursor + envelope + router group | ✅ | `87662396` |
| **P1.A.2** GET `/api/desktop/sync/threads` | ✅ | `9afef176` |
| **P1.A.3** GET `/threads/:id` single fetch | ✅ | `ca4003e7` |
| **P1.A.4 partial** GET `/sync/messages` | ✅ | `a76822a1` |
| **P1.A.4 cont.** render_jobs + thread_files | ⏳ deferred (no consumer yet) |
| **P1.A.5a** sidecar handles `action="delete"` | ✅ | `b545f12e` |
| **P1.A.5b** cloud tombstone emission + sync merge | ✅ | `394c3334` |
| **P1.A.5c** tombstone GC sweeper (90-day retention) | ✅ | `e105060f` |
| **P1.B.1** SyncWorker skeleton + CursorStore | ✅ | `060ac358` |
| **P1.B.2** sidecar `Client.ListThreadsDelta` | ✅ | `7d9a93c4` |
| **P1.B.3** SyncWorker wired for thread sync | ✅ | `645be703` |
| **P1.B.3.x** sidecar messages sync primitives | ✅ | `90453c65` |
| **P1.B.3.x.2** wire messages sync (on-demand) | ✅ | `8a8d1db0` |
| **P1.B.3.x.3** threads-job periodic message-sync fan-out | ✅ | `ded48ba9` |
| **P1.B.4** enhanced `/agent/threads*` endpoints | ✅ cloud_sync_state surfaced as JSON + `include_paused=false` sidecar/filter + renderer active-list call |
| **P1.B.5 (slim)** mount thread list + message view | ✅ | `d1fba47c` |
| **P1.B.5+** markdown rendering in ThreadView | ✅ | `51f34676` |
| **P1.B.5 (full)** mount real WorkAgent component | ⏳ deferred (sequencing) |
| **P1.B.6** composer + offline-disabled UX | ✅ | `4e0c2000` (+ prereq `f07666a8`) |
| **P1.B.7** offline write queue | ⏸ premature (only chat-send writes today; correctly handled by P1.B.6's offline-disable rather than queueing) |
| **P1.B.8** completion report | (this doc is the mid-milestone version, now v0.5) |

**18 of 19 slices done, 1 deferred (full WorkAgent mount;
awaiting parallel-edit sequencing).**

### 1.3 Two bugs caught during the P1 build

These are worth flagging as future-defense lessons:

1. **`cloud_proxy/proxy.go` targeted a non-existent cloud URL** —
   `/api/workagent/agent/chat` doesn't exist; real path is
   `/api/work-agent/chat/agent`. Never surfaced before because no
   caller drove an end-to-end chat through the proxy until P1.B.6.
   Tests passed against the wrong URL. **Lesson**: when a slice
   builds a relay against a real endpoint, drive at least one
   manual smoke test against the real cloud during the slice, not
   weeks later. Fix in `f07666a8`.

2. **GORM `Scan` into struct needs explicit `AS` aliases for
   `COALESCE`'d columns** — first run of `message_repo_test.go`
   came back with all string fields empty because the result
   column name was the full SQL expression. Generic enough to bite
   again. **Lesson**: when adding a new repository with COALESCE
   defaults, write at least one round-trip test that asserts
   non-empty values BEFORE relying on the repo elsewhere.
   Fix in `a76822a1`.

3. **GORM `tx.Create(&row)` does a post-INSERT scan that fails on
   SQLite `TEXT`-stored `time.Time`** — caught when implementing
   `InsertTombstone`; the model's `DeletedAt time.Time` couldn't
   be re-scanned from the TEXT column after the INSERT. Same
   family as bug #2 (GORM column-resolution quirks). **Lesson**:
   prefer raw `tx.Exec(INSERT...)` over `tx.Create(&row)` for
   tables we don't need the returned ID from. Works on both
   SQLite (tests) + MySQL (prod) without backend-specific code.
   Fix in `394c3334`.

---

## 2. End-to-end UX now alive

Walk-through after `./desktop/scripts/dev.sh`:

1. **Sign in** — click "Sign in with WorkMax" → BrowserWindow opens
   workmax.app OAuth → consent → loopback callback → token in macOS
   Keychain → renderer's `useAuthStatus` poll (1.5s) flips to
   authenticated.

2. **Thread list populates** — `useThreadList` polls `/agent/threads`
   every 5s. Within a few seconds of sign-in, the `SyncWorker`
   (P1.B.3) fires its startup tick → calls
   `ListThreadsDelta(cursor)` → upserts into local SQLite →
   `cloud_sync_state='synced'`. Next poll cycle sees the rows.

3. **Click a thread** — `useThreadMessages(uuid)` fetches
   `/agent/threads/:uuid/messages`. Handler returns local rows
   immediately AND fires `MessagesSyncer.Trigger` for that
   thread. Background goroutine syncs the cloud messages into
   SQLite. Next 5s poll picks them up.

4. **Send a message** — type in composer (⌘+Enter or click Send).
   Composer reads `useNetworkState` — disabled if offline.
   `useChatStream` POSTs to `/agent/chat` with `thread_uuid` +
   payload. Sidecar resolves UUID to local PK, forwards to
   `/api/work-agent/chat/agent` with Bearer. SSE chunks land →
   in-flight buffer grows above the historic list. On `done`,
   the historic list refreshes (200ms gap to prevent flicker),
   in-flight clears.

5. **Toggle wifi off** — within ~30s, OfflineBanner shows up top,
   composer textarea + Send button grey out with tooltip
   ("Offline — sending disabled"). Historic browse still works
   (reads local SQLite). Re-enable wifi → composer re-enables
   automatically via the network-state SSE channel.

6. **Sign out** — `/auth/logout` snapshots the refresh token,
   POSTs cloud `/api/desktop/oauth/revoke` (best-effort; doesn't
   block on cloud failure), clears Keychain. Renderer routes back
   to LoginPage.

7. **Restart sidecar mid-session** — handshake re-establishes,
   Keychain re-loaded, SyncWorker startup trigger re-fires. User
   sees ~1-2s of "Loading…" then everything resumes from the
   resume cursor (no double-processing; cursor is per-page-flushed).

What's NOT alive yet (documented in §3):

- ~~Markdown rendering of assistant messages~~ ✅ landed in
  `51f34676` (v0.2). Assistant text now renders bold/italic/code
  blocks/lists/tables/links/blockquotes via remark-gfm.
- File attachments / inline file references.
- Skill picker UI (hardcoded `ppt` from P0 allowlist).
- Thread rename / settings (delete propagation **does** work now;
  v0.2 landed both halves of the tombstone loop).
- Tool-use / structured content blocks rendered inline (visible
  in cached `ai_text` after `done`, but not as live blocks during
  the stream).

---

## 3. Test footprint + LOC inventory

| Surface | Files | Lines | Tests |
|---|---|---|---|
| Sidecar Go (sidecar HTTP + cloud_proxy + sync) | ~42 | 5,695 prod + 7,171 test | **215** |
| Cloud-side sync + tombstone GC sweeper (Go) | ~14 | 3,599 | 79 (sync sub-suite) |
| Renderer (desktop FE TypeScript) | ~14 | 2,121 | Vitest Desktop suite: 18 files / 182 tests |

**Race detector clean** across the whole desktop tree:
`go test -tags desktop -race ./desktop/...` — passes in ~25s
on M-series hardware.

**Production build path untouched**: `go build ./...` (no
`-tags desktop`) stays clean. Zero leakage from desktop into the
cloud server.

---

## 4. Architecture: what's deliberately different from the original plan

Each of these calls is documented in the slice's commit message
(and re-summarized here so future maintainers don't re-litigate).

### 4.1 P1.B.5 (full WorkAgent mount) → P1.B.5 (slim)

**Historical plan said**: mount the then-existing browser WorkAgent component
end-to-end with all requests re-routed through the local bridge.

**Shipped**: standalone `ThreadList` + `ThreadView` (~600 LOC
total) using only sidecar endpoints. Did NOT touch the WorkAgent
component (~173 .tsx files with parallel edits in flight from
the user's other work).

**Why**: merge-conflict risk vs marginal incremental value —
most WorkAgent features need cloud endpoints the sidecar doesn't
proxy. The slim path delivers the actual user-visible win using
what's already there.

**Path forward**: P1.B.5 (full) sits behind whatever signals the
parallel WorkAgent edits have settled. Likely candidates: a
shared `<DesktopApiAdapter>` context provider that flips fetch
base URLs depending on `window.workmaxLocal` presence.

### 4.2 On-demand messages sync instead of periodic

**Plan said**: SyncWorker walks all local threads each tick to
refresh messages.

**Shipped**: `MessagesSyncer` triggered by `/agent/threads/:uuid/messages`
handler each time the user opens a thread. Per-thread coalesce.

**Why**: most users open 1-3 threads, not 50. Periodic sync
would fan out wasted bandwidth. On-demand has lower latency (sync
fires immediately on click instead of waiting up to 5min).
Matches how email apps work.

**Reversible**: a future periodic walk for offline-prepared users
can be ADDED as an additional trigger source on top of the
existing on-demand path.

### 4.3 P1.B.7 (offline write queue) → deferred

**Plan said**: queue offline writes (chat sends, thread renames,
etc.) for replay when connection returns.

**Shipped**: nothing — and the deferral is on purpose.

**Why**: the only write surface that exists today is chat send,
which is correctly handled by P1.B.6's offline-disable rather
than queueing. Replaying a queued chat hours later would generate
AI responses out of context — weird UX. Other writes (rename,
delete, etc.) don't exist on the desktop renderer yet.

**Revisit when**: thread rename / delete / settings UIs land.

### 4.4 P1.B.2 narrowed to 1 endpoint (threads only)

**Plan said**: 5-endpoint stub client for the future sync API surface.

**Shipped**: `Client.ListThreadsDelta` alone. Other 4 (`/threads/:id`,
`/messages`, `/render_jobs`, `/thread_files`) deferred.

**Why**: only `/threads` cloud counterpart existed when P1.B.2
landed. Stub-implementing the others would have meant guessing
wire shapes that could shift when the actual endpoint lands.

**Status**: `/messages` client + writer + job all landed in
P1.B.3.x; render_jobs + thread_files still pending (no consumer
surface yet).

---

## 5. Performance snapshot (developer machine, M-series Mac)

Numbers from `./desktop/scripts/dev.sh` runs against a real
workmax.app dev environment.

| Metric | Value | Notes |
|---|---|---|
| Sidecar cold start (binary launch → stdout handshake) | ~30 ms | Unchanged from P0 |
| First HTTP request handle latency | <5 ms | Unchanged |
| Thread list first-paint after sign-in | **~5 s** | SyncWorker startup tick + first poll cycle |
| Click thread → messages visible | **<1 s** (already-synced) / ~1-3s (cold) | On-demand sync goroutine race |
| Chat send → first SSE chunk | bounded by cloud (~200-400ms typical) | Sidecar adds <10ms |
| `cache_writer` flush cadence | every 32 events OR 200ms (P0.7b + `0218040d`) | Same UX, lower fsync churn |
| Sidecar RSS (idle, 1 user, ~5 threads cached) | **~15 MiB** | Up from ~12 MiB at P0 (sync worker + state) |
| SQLite file size (50 threads + ~200 messages cached) | **~1.2 MiB** | Bounded by `structured_content` blob sizes |
| `go test -tags desktop -race ./desktop/...` | **~25 s** | Was ~8s at P0; sync sub-tests add the bulk |

**Not yet measured (P2 work)**: 1000+ thread cold-start sync time,
sidecar memory under heavy chat traffic, SQLite open time with
100k+ message rows.

---

## 6. Open items for the rest of P1

### 6.1 Critical-path remaining (none)

The critical UX path is closed. Sign-in → list → view → send →
offline behavior all work end-to-end. There's no slice blocking
"can a real user complete a real task on desktop."

### 6.2 Nice-to-haves, in rough priority order

(v0.3 closed items 1 + 2 from v0.2 — periodic message-sync fan-out
and tombstone GC. Renumbered + pruned. Queue now drained of all
small/medium items; only multi-day + external-dep items remain.)

1. **WorkAgent component full mount** — only valuable once we
   want additional Desktop features (skill picker,
   file uploads, thread settings, rating UI). Multi-day; needs
   coordination with parallel WorkAgent work.

2. **Tool-use / structured content rendering** — today these are
   buried in `ai_text` after `done`. Surfacing them inline needs
   either: (a) reuse WorkAgent's block renderers, OR (b) build a
   minimal desktop-side renderer. (a) is cheap once item 1 lands.

3. **Sidecar `Client.GetThreadFull` method** — the cloud endpoint
   landed in v0.2 (P1.A.3); the sidecar consumer hasn't. Will
   land when the renderer's "show thread settings" UI lands and
   needs it. Half-day add.

### 6.3 External-dependency items (not P1)

- Notarization workflow — needs Apple Developer ID + app-
  specific password
- Windows / Linux build paths — needs product decision

### 6.4 Things that "feel" like P1 but aren't

- **Cloud→local file sync**: `thread_files` sync endpoint
  exists in design but renderer doesn't surface files yet.
  Wait for file-attachment UI before building.
- **Authenticated WebSocket / server-push channel**: design
  doc mentions it for P3+ (cloud-sync.md §4). Polling is
  fine for P1; revisit if real users complain about latency.
- **Per-thread write rate-limiting**: in case a user triggers
  many sends in rapid succession. Cloud should handle; if
  not, we'll see it in real usage and add then.

---

## 7. Go/No-Go for taking the desktop to early-access users

**Recommendation: Go** for internal-team and 1-2 friendly
external users. Not for public release yet.

**Ready for early testers because**:
- End-to-end UX works (sign in → see threads → chat).
- Race-clean concurrency; SQLite writes survive crash mid-stream
  (`streaming_state='partial'` recovery).
- Offline detection + graceful degradation.
- Logout actually invalidates server-side state.
- Test footprint (203 desktop-tagged Go tests) catches
  regressions in the hot paths.

**NOT ready for public because**:
- Unsigned + unnotarized — Gatekeeper warns on other machines.
- macOS-only.
- Several WorkAgent features are not surfaced (file upload,
  skill picker, thread settings, ratings).
- ~~Deletes don't sync~~ ✅ landed in v0.2 (`b545f12e` + `394c3334`).

**Watch-items if you ship to early testers**:
- Real-world latency for "click thread → see history" with N
  cloud messages. Could need batching adjustments.
- Sidecar RSS under sustained chat traffic. Could need
  message-buffer tuning.
- Edge cases the test matrix doesn't cover: network flap during
  OAuth, race between sign-in and sync-worker startup tick.

---

## 8. Spike → P1 progression at a glance

```
P0 SPIKE (closed 2026-05-18)
  17 PR-sized tasks, 100 desktop tests, "Go for P1" recommended

P1 mid-milestone v0.1 (2026-05-19 early)
  +15 slices: 4 post-spike hygiene + 11 P1 plan items
  203 desktop tests
  Critical UX path closed

P1 mid-milestone v0.2 (2026-05-19 mid-day)
  +4 slices: P1.A.5a/b tombstone propagation (sidecar + cloud),
  P1.A.3 single-thread full-fetch, markdown rendering
  211 desktop tests (+8)
  Delete-propagation loop closed end-to-end

P1 mid-milestone v0.3 (2026-05-19 later)
  +2 slices: P1.B.3.x.3 periodic message-sync fan-out (on-demand
  + periodic together cover all access patterns), P1.A.5c
  tombstone GC sweeper (90-day retention; loop fully closed)
  215 desktop tests (+4)
  §6.2 nice-to-have queue drained of all small/medium items;
  only multi-day "full WorkAgent mount" + external-dep items
  (icon, notarization, non-mac platforms) remain
  Recommended: Go for early-access users; defer public release

P1 mid-milestone v0.4 (2026-05-19 latest)
  +1 slice: P1.B.4 include_paused filter on /agent/threads.
  The sidecar read API no longer has a small partial item; remaining
  non-external P1 work is the multi-day full WorkAgent mount or a
  P1 completion report that explicitly defers it.
  217 desktop tests (+2)

P1 mid-milestone v0.5 (this update, 2026-05-20)
  Renderer useThreadList now calls /agent/threads?include_paused=false,
  so paused threads are hidden from the normal left rail end-to-end.
  Desktop renderer suite: 18 files / 182 tests passing.
  Desktop backend sweep: sidecar + cloud desktop API + services +
  middleware passing under -tags desktop.

P1 close (2026-05-20)
  P1_COMPLETION_REPORT.md written from the current state:
  early-access Go, public-release No-Go until performance/manual
  smoke/bundled-renderer/notarization gates are handled. Full
  WorkAgent mount, render_jobs, and thread_files remain deferred by explicit scope
  decision rather than hidden incomplete work. Packaged builds now prefer the
  bundled static renderer and only fall back to hosted `/desktop` if the
  artifact is absent; real previously authenticated cached-history evidence
  using the helper-enforced unreachable sidecar cloud base is still required for
  offline public release.
  Network-state SSE
  keepalive also landed and is covered by a focused sidecar test.
  Local-cache read benchmarks now cover 5000-thread and
  10000-message cases, and httptest cold-start thread sync covers
  1000 threads. Diagnostics now exposes heap/goroutine trends;
  sustained-chat memory and real-cloud timing remain gates. Account
  panel now reaches `/auth/userinfo` via the sidecar and `/auth/logout`
  closes local/cloud session. A local sidecar smoke helper now
  standardizes loopback readiness checks; OAuth/chat still require
  real-cloud manual smoke.

P2 (per execution-plan-p1.md §5)
  Marketplace / Windows / bidirectional sync — only after P1
  close ratifies the foundation.
```

---

*Document version: v0.5 — 2026-05-20 refresh after renderer active-list
wire-up and README maintenance. All small/medium nice-to-haves now
closed; the §6.2 queue has only multi-day + external-dep items
remaining.*
*Next refresh: when full WorkAgent mount lands, file/render consumer
surfaces appear, or a public-release gate is cleared.*
