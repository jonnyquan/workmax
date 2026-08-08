# WorkMax Desktop — P0 Spike Report

**Date**: 2026-05-18
**Author**: Jonny + Claude (pair)
**Scope**: 17-task spike (P-1.1 through P-1.7, P0.1 through P0.10)
**Recommendation**: ✅ **Go for P1** — every P0 task hit acceptance, no
blocker-grade issues found, dev workflow runs end-to-end on macOS.

> Historical snapshot: the former Web/Admin clients and detailed planning
> archive were removed when the open-source repository converged on
> Server + Desktop. Paths and future-work wording below describe the 2026-05
> baseline, not current source ownership. Use [README.md](./README.md) for
> current commands and boundaries.

---

## 1. Capabilities shipped

What works today, end-to-end, on a developer's macOS box (`./desktop/scripts/dev.sh`):

| Capability | Status | Pinned by |
|---|---|---|
| Sidecar binary boots, opens SQLite, applies 1 migration, seeds device_id | ✅ | P0.1 + P0.2 |
| Sidecar HTTP on `127.0.0.1:<dynamic>` + handshake on stdout | ✅ | P0.3 |
| X-Local-Token middleware: missing token → 403 | ✅ | P0.3 |
| Electron main spawns sidecar, parses handshake, opens BrowserWindow | ✅ | P0.4 |
| Renderer reads `window.workmaxLocal` via contextBridge preload | ✅ | P0.5 |
| OAuth 2.1 Authorization Code + PKCE + loopback redirect (RFC 8252) | ✅ | P-1.1 → P-1.7 |
| Refresh token rotation + replay-detection chain sweep | ✅ | P-1.3 |
| macOS Keychain persistence (shell-out to `security` CLI, no CGO) | ✅ | P0.6a |
| OAuth happy path: LoginPage → BrowserWindow → token in Keychain → LoginPage unmounts | ✅ | P0.6b/c/d |
| Cloud Proxy chat relay: dual-SSE pipe, 1 MiB frame cap, backpressure | ✅ | P0.7 |
| 401 auto-refresh with 5s-loop guard | ✅ | P0.7c |
| Per-event SQLite cache writer (batch-flush every 8 events / on boundaries) | ✅ | P0.7b |
| 9-kind closed error classifier (cloud-proxy.md §6.1) | ✅ | P0.7a |
| Skill allowlist filter: catalog + chat-mode gate (only `ppt` exposed) | ✅ | P0.8 |
| Network state SSE: 30s probe + 2-failure debounce | ✅ | P0.9a |
| Local SQLite read endpoints: `/agent/threads` + `/agent/threads/:uuid/messages` | ✅ | P0.9b |
| Renderer `useNetworkState` hook + `OfflineBanner` | ✅ | P0.9c |

**Test footprint**: 100 desktop-tagged Go tests, race-clean.
Production build (`go build ./...` without `-tags desktop`) stays clean —
desktop code never leaks into the cloud server.

---

## 2. Capabilities NOT shipped (deferred to P1+)

Honest list. Each was either out of scope, blocked, or fell out of
re-scoping conversations.

| Missing capability | Reason | Path |
|---|---|---|
| **Real chat UI in the renderer** | Out of scope — P0 focused on the *plumbing* (sidecar, OAuth, relay, hooks). | P1: build the native bundled UI under `desktop/renderer/` against the typed Desktop bridge |
| **Thread row creation on chat turn** | `cache_writer` writes messages but not the parent `w_workagent_thread` row; the list endpoint returns empty until cloud-sync backfills. | P1.x: cloud-sync read path inserts/updates thread rows from cloud |
| **Cloud → local thread/message sync** | P0 only writes locally; pull-only sync was designed but not implemented in this snapshot. | P1 priority 1 (gating real workflow) |
| **Input-disabled-when-offline UX** | The chat composer doesn't exist in the renderer yet, so there's no input to disable. `useNetworkState` is in place; composer just needs to read it. | P1, with the chat UI mount |
| **Logout calls backend revoke** | ✅ Landed post-spike: sidecar `/auth/logout` clears local credentials and best-effort revokes the refresh token; renderer AccountSettings now calls it. | Done |
| **SSE keepalive comments** | `/agent/chat` and `/system/network-state` don't emit `: keepalive\n\n`. Loopback connection won't time out, but a reverse proxy (future) might. | P1 (cheap) |
| **Notarization + signing** | `build-mac.sh` runs electron-builder unsigned. Gatekeeper will refuse opens on other machines. | P1 — Apple ID + app-specific password needed |
| **Windows / Linux builds** | `dev.sh` detects Linux but `build-mac.sh` is mac-only; no Windows path at all. | P2 (mac-first decision is product, not technical) |
| **User-custom skill loader** | Hardcoded allowlist `["ppt"]`. | P2 — needs Server-owned catalog and Desktop marketplace plumbing |
| **Per-event timer-driven batch flush** | `cache_writer` uses 8-event count threshold. A long-quiet stream gets persisted only on the next event. | P1 — replace with 200ms timer (cloud-proxy.md §5.4) |
| **electron-builder config** | ~~`build-mac.sh` exits with clear error if `electron-builder.yml` missing — we never wrote one.~~ Landed post-spike — config + entitlements wired; app icon + notarization still TODO. | ~~P1 packaging task~~ Done for the config; icon + notarization remain |

---

## 3. Known issues / quirks

### 3.1 Issues caught during the spike (FIXED, but worth remembering)

- **GORM logger polluting stdout** (P0.3) — stdout handshake JSON must
  be the only stdout write. Fixed by injecting silent gorm logger with
  stderr output + `IgnoreRecordNotFoundError: true`. Anyone touching
  the sidecar boot path needs to keep this in mind: log to stderr,
  reserve stdout for the handshake line.

- **Transaction rollback eating replay sweep** (P-1.3) — refresh chain
  replay-detection sweep was inside the same `Transaction(func)` that
  the rotation logic ran in. When the inner func errored, the sweep
  rolled back too, leaving sibling chains alive. Fixed by moving the
  sweep OUTSIDE the transaction. **Security-critical**; pinned by test.

- **Cache-Control on /token success** (P-1.5) — RFC 6749 §5.1 requires
  `no-store` on ALL token responses, not just errors. Fixed with a
  `writeTokenCacheHeaders` helper.

- **Same-second JWT tokens identical** (P-1.5) — two refreshes inside
  one second produced identical JWT bytes (clock-second precision +
  identical claims). Added `jti` (chain UUID) claim for per-token
  uniqueness.

- **Wire-shape drift** ([[feedback_wire_shape_drift_audit]]) — JSON
  marshal/Unmarshal tests caught a 4-kind enum mismatch between Go
  side and TS side. Not in this spike's path, but a reminder for P1
  to dump+diff serialized JSON when adding new wire types.

- **Data race on test-watcher bool** (P0.8) — `cloudCalled bool` set
  by httptest handler goroutine, read by test goroutine without
  synchronization. Switched to `atomic.Bool`. Pattern documented for
  future tests.

### 3.2 Open quirks (kept on purpose)

- **`cache_writer` lazy-INSERT** — turn dies before first SSE event →
  no row left in `w_workagent_message`. Renderer never sees a "phantom"
  empty turn. Comes at the cost of: a turn that errors on the very
  first event still leaves a row marked `partial` with empty text.
  Tested as `TestCacheWriter_NoEventsNoRow` + `TestProxy_Chat_Upstream500`.

- **OAuth `tryAuthRecover` skipped if just-rotated** — if a Chat turn
  hits 401 within 5s of the last token rotation, we don't retry; assume
  the 401 means something other than expired token (e.g. backend
  permission issue). Bounded behavior; if we see false-negative reports
  in P1, lift the 5s threshold.

- **Network state debounce: 2 failures → offline, 1 success → online**
  (asymmetric). The user experience reads as "offline takes a minute
  to notice, online recovery is instant". Intentional per cloud-proxy.md §7.2.

- **`useNetworkState` reconnect is unconditional 2s** — no exponential
  backoff. If the sidecar is dead for an hour, the renderer hammers
  it every 2s. Fine for loopback (no network cost); rip into a
  proper backoff if we ever proxy this over the network (we won't).

---

## 4. Performance data (developer machine, M2 Max)

Numbers from `./desktop/scripts/dev.sh` runs on macOS 25.5 / Go 1.24.1
/ Node 20.11 / Electron 40.

| Metric | Measurement | Notes |
|---|---|---|
| Sidecar cold start (binary launch → handshake on stdout) | **~30 ms** | Includes SQLite open + migration check + listener bind |
| First HTTP request handle latency (after handshake) | **<5 ms** | `/health` round-trip from curl |
| First SSE event latency (`/agent/chat` → first frame) | **bounded by cloud** — sidecar adds ~10ms | Whole-turn latency depends on workmax.app response time |
| Network-state SSE first event | **<150 ms after subscribe** | Subscribe immediately pushes Snapshot() |
| Probe interval | 30s | Hardcoded; can be made tunable in P1 |
| SQLite write per cache flush | **~1-2 ms** | Single UPDATE; batched every 8 events or boundary |
| Electron cold open → DesktopShell first paint | **~600 ms** | Includes sidecar spawn + handshake parse + Next dev server compile |
| Sidecar memory (RSS, idle) | **~12 MiB** | After 30 min idle |
| SQLite file size (fresh install) | **~80 KiB** | After 1 migration; grows ~1 KiB per ai_text written |
| Go test suite (`go test -tags desktop ./desktop/...`) | **~6 s** | 100 tests; race detector adds ~2 s |

**Not measured (P1 work)**:
- Mid-turn cancellation latency (renderer close → upstream close)
  observed cleanly in `TestProxy_Chat_RendererDisconnect` (<500 ms)
  but never measured in a real production session.
- Memory under load (long chat turn with 1000+ SSE events).
- Cold-from-disk SQLite open with 10k+ message rows.

---

## 5. Architectural decisions worth re-litigating in P1

These are choices we deliberately made for the spike but should
revisit before locking in for v1.

| Decision | Rationale (P0) | What to verify in P1 |
|---|---|---|
| Shell-out to `security` CLI for macOS Keychain (no CGO) | Faster spike; no cross-compile pain | Measure latency under load; if `security` fork-exec adds significant per-call cost, switch to a CGO wrapper. Current measurement: ~5-10ms per call which is fine. |
| `cache_writer` raw SQL instead of GORM model | Avoid dragging cloud handler deps into desktop build | If we add more local-write tables in P1, the SQL repeats; consider a tiny gorm.Model subset. |
| Sidecar `/agent/chat` is a transparent SSE relay (not transforms or routes per-skill) | Cloud handler keeps full control over billing / gating | If we ever want to inject local context (system prompts from `desktop/`-resident skills) we have to break this. Today: no — keep it transparent. |
| 9-kind error classifier collapses CodePilot's 24-kind set | No local CLI / no MCP / no BYOK on desktop | Don't expand without evidence — every kind costs FE conditional UI. |
| Network probe = HEAD on `BaseURL/` (any HTTP response counts as online) | Bandwidth-cheap, doesn't need auth | If workmax.app ever puts the root behind auth, switch to a probe-specific endpoint. |
| Single-skill allowlist (`["ppt"]`) | Spike scope discipline | When unlocking more skills, each needs a smoke-test through the desktop renderer first. |
| Branch policy: single `main` branch with PR-sized commits, no spike branch | After 2026-05 conversation that "应该是合并，不是替换" | If the spike had been on a side branch, certain refactors that touched both desktop + production (e.g. P0.7d's `agent_chat_helpers` audit) would've been duplicated. Keeping single-branch worked; recommend continuing. |
| Hardcoded `text/event-stream` headers in sidecar handlers | Avoid abstracting too early | If we add more SSE endpoints in P1 (sync events?), extract a helper. |

---

## 6. P1 priorities (recommended order)

Based on what's missing + what shipped, the highest-leverage P1
sequence:

1. **Cloud→local thread/message sync**
   — without this, the local read endpoints never have data on a
   fresh install. Pull-only is fine for v1; bidirectional sync is v2.

2. **Build the real WorkAgent UI in the bundled Desktop renderer**
   against the typed Desktop bridge. With #1 in place, the thread list and
   chat history populate without a separate browser client.

3. **Chat composer + input-disabled-when-offline** — should fall
   naturally out of #2; `useNetworkState` is already in place.

4. **`electron-builder.yml` + notarization** — required to distribute
   to anyone other than the dev who built locally. Apple ID + app-
   specific password needed.

5. **Backend revoke on logout** — P0 cleanup; refresh chain stays
   alive until natural expiry today.

6. **Timer-driven batch flush in `cache_writer`** — replaces the
   8-event count threshold; quiet streams flush within 200ms.

7. **SSE keepalive comments** — defensive, cheap, no behavior change today.

8. **Windows + Linux build paths** — only after macOS dist is real.

---

## 7. Go/No-Go for P1

**Recommendation: Go.**

- All 17 spike tasks hit acceptance criteria.
- No blocker-grade issues found.
- Test coverage is meaningful (100 desktop-tagged Go tests, all paths
  exercised including security-critical refresh chain replay + 401
  auto-refresh + renderer disconnect cancellation).
- Production build path is unaffected (`go build ./...` clean, no
  desktop dependencies leaked).
- Dev workflow (`./desktop/scripts/dev.sh`) runs end-to-end on macOS
  in <5 minutes from clean clone (incl. `npm install`).
- Architecture decisions are documented + reversible where it matters.

The path to a v1 desktop client is clear. The hard parts (OAuth +
SSE relay + race-free auth) are done; what remains is mostly product
integration (mount the chat UI, sync data in, package + distribute).

---

## Appendix: Spike commit list (chronological)

| # | Commit | Slice |
|---|---|---|
| 1 | `b8d3a02f` | P-1.1 — desktop OAuth migration (3 tables) |
| 2 | `2f81f30a` | P-1.2 — OAuth models + PKCE + client registry |
| 3 | `7cf1ba9c` | P-1.3 — refresh chain rotation + replay sweep |
| 4 | `c97f8bee` | P-1.4 — OAuth /authorize + /consent handlers |
| 5 | `8cfee97a` | P-1.5 — /token endpoint (both grants) |
| 6 | `16f99de3` | P-1.6 — /userinfo |
| 7 | `ff8b4f19` | P-1.7 — E2E OAuth integration test + dev curl script |
| 8 | `b37845cb` + `5d701e9d` | rename + companion migration (table prefix) |
| 9 | `1f1d5dbe` | P0.6a — macOS Keychain + TokenStore |
| 10 | `c86151cd` | P0.6b — PKCE generator + loopback callback |
| 11 | `77a7cc61` | P0.6c — OAuth orchestrator + sidecar /auth endpoints |
| 12 | `81847c1f` + `9c59bf23` | P0.6d — Electron BrowserWindow + LoginPage (re-staged FE) |
| 13 | `24361c21` | P0.7a — proxy error classifier |
| 14 | `e1b1ed3f` | P0.7b — local SQLite cache_writer |
| 15 | `8fcd0666` | P0.7c — Chat orchestrator + SSE relay |
| 16 | `015684ca` | P0.7d — sidecar /agent/chat endpoint |
| 17 | `7009f633` | P0.8 — skill allowlist + filtered catalog + chat-mode gate |
| 18 | `e2acc8e7` | P0.9a — network state watcher + SSE endpoint |
| 19 | `26eb2653` | P0.9b — local thread/message read endpoints |
| 20 | `5308b295` | P0.9c — useNetworkState hook + OfflineBanner |
| 21 | *(this commit)* | P0.10 — SPIKE_REPORT + build-mac.sh + README polish |

(Earlier commits established the design docs + cloud-side OAuth
prereqs; those landed on main before the spike started in earnest.
The 21 above are the concrete code commits that shipped P0.)
