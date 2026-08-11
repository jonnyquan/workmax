# WorkMax Desktop Smoke Checklist

Use this checklist when validating a Desktop build against the current code.
It separates local sidecar readiness from authenticated cloud behavior and
public-release packaging gates. The supported product boundary is Go Server
plus Desktop only; no check may rely on or recreate an independent Web/Admin
client.

## 1. Local Sidecar Readiness

Prerequisite: start the sidecar with a known token. There is one binary now and
`--serve-only` is the sidecar-without-a-window mode:

```bash
WORKMAX_LOCAL_TOKEN=<token> ./desktop/scripts/dev.sh --serve-only
```

It prints the loopback port it bound. The two cloud-dependent checks below
(`--with-server-version`, `--with-skills-catalog`) need a signed-in session and
fail offline; that is the environment, not a regression.

```bash
WORKMAX_LOCAL_TOKEN=<token> ./desktop/scripts/smoke-local.sh --port <port>
WORKMAX_LOCAL_TOKEN=<token> ./desktop/scripts/smoke-local.sh --port <port> --expect-version 0.1.0-p1-ea
WORKMAX_LOCAL_TOKEN=<token> ./desktop/scripts/smoke-local.sh --port <port> --check-token-rejection
WORKMAX_LOCAL_TOKEN=<token> ./desktop/scripts/smoke-local.sh --port <port> --check-pid-lock --sidecar-binary desktop/wails/bin/workmax-desktop
```

Expected:

- `/health`, `/auth/status`, `/system/diagnostics`, and
  `/agent/threads?include_paused=false` return 2xx.
- Diagnostics include a non-empty sidecar version; with
  `--expect-version <version>`, the helper asserts the exact version.
- With `--check-token-rejection`, missing, wrong, and duplicate
  `X-Local-Token` requests return 403. Duplicate-header rejection is checked on
  `/health`; missing/wrong rejection is checked across representative
  read/write surfaces: `/health`, `/auth/status`, `/system/diagnostics`,
  `/agent/threads`, `/system/log`, and `/system/trigger-sync`.
- With `--check-pid-lock --sidecar-binary <path>`, a second sidecar launched
  against the same diagnostics data directory exits with the expected
  "another sidecar instance" lock error before opening SQLite.
- The helper accepts only loopback origin `--base` values, rejecting
  credentials, paths, query strings, and fragments before any token-bearing curl.
  It also applies a bounded curl timeout to every request and bounds the
  second-sidecar PID-lock probe with `--pid-lock-timeout` so a lock regression
  fails instead of hanging smoke. When `--diagnostics-samples` captures more
  than one snapshot, every sample is validated for SQLite paths, integrity,
  migrations, and Go heap/goroutine counters.
- `desktop/scripts/smoke-local.test.sh` covers the helper's preflight
  validation paths without launching a sidecar, including unsafe `--base`
  values that must be rejected before any token-bearing curl can run. It also
  stubs curl to prove multi-sample diagnostics validation accepts two valid
  samples and rejects a malformed later sample.
- `desktop/scripts/build-mac.test.sh` covers build wrapper argument validation
  and the `--preflight-only` path against the current packaging inputs,
  including packaging drift for hardened runtime, the renderer allowlist
  allowlist, bundled renderer resources, and side-effect hooks.
- `desktop/scripts/check-bundled-renderer.test.sh` covers bundled-renderer
  source preflight failures before packaging: missing files, missing or unsafe
  CSP, extra remote CSP connect sources, non-relative CSS/JS references, and
  unexpected or token-like embedded files.

## 1b. L2 Tool Loop and Approvals

The tool loop is the one path whose behavior lives inside somebody else's
binary, so it gets its own smoke against the real claude CLI. Everything is
local: a scripted Anthropic endpoint stands in for the model, so the harness
picks which tool the loop calls and when.

```bash
WORKMAX_TEST_CLAUDE_CLI=$HOME/.local/share/claude/versions/<ver> \
  ./desktop/scripts/smoke-l2-approvals.sh
```

It owns its own sidecar (isolated data dir, both approval modes) and prints one
`ok`/`FAIL` line per claim. `--skip-timeout` drops the case that waits out the
120s approval timeout; `--keep` keeps the data dir and per-turn `.sse`
captures for inspection. Without a CLI it prints "skipping" and exits 0, the
same env gate as `TestIntegration_CLIEndpointInventory` — CI has no claude
binary.

Expected: 31 checks pass. What they cover:

- a write tool call raises an `approval_request` frame carrying id, name and
  target; each of the four decisions is answered through the same
  `POST /agent/turns/:uuid/approve` route the renderer's card uses;
- `allow_once` runs the tool and the turn still reaches `done`; `deny` leaves
  the file absent, narrates `tool_denied`, and the turn survives the refusal;
- `allow_session` silences the next call on the same thread; `allow_always`
  silences a new thread too and leaves exactly one
  `w_desktop_agent_permission_rule` row for the active local uid;
- read-only tools (`Read`/`Glob`/`Grep`) never ask;
- a tool outside the declared surface (`Bash`) is refused and does not execute
  — in BOTH modes, including pre-approved mode, which is where it once did;
- a write outside the workspace is refused outright, never offered as a card;
- an unanswered card denies on the sidecar's 120s timeout while the stream
  stays alive on keepalives;
- with `WORKMAX_L2_APPROVALS` unset the loop is back to pre-approved mode: no
  card, the tool runs, and the approve endpoint answers 503.

`desktop/scripts/smoke-l2-approvals.test.sh` covers the harness itself without
a CLI or a sidecar: the preflight, the skip gate, the approve payload's shell
quoting, and the scripted endpoint's directive rules.

Optional authenticated additions:

```bash
WORKMAX_LOCAL_TOKEN=<token> ./desktop/scripts/smoke-local.sh --port <port> --with-userinfo --with-server-version --with-skills-catalog --trigger-sync
```

Run those only after OAuth sign-in. `--with-skills-catalog` exercises the
cloud-backed `/agent/skills/catalog` path and asserts `allowed_modes` includes
`ppt`, `items` is an array, `count === items.length`, and no returned item falls
outside the Desktop allowlist.
`--trigger-sync` returns 401 by design when there is no valid Desktop session.

### Phase 1 Server candidate checks

The following checks exercise the implemented Desktop Login Transaction
kernel, persistence model, password adapter, HTTP-handler contract, OAuth code
binding, migration text, Sidecar typed cloud client/coordinator/local routes,
the Go `/login/` gate and bundled password UI without reading
`server/config.yaml` or connecting to an external database:

```bash
cd server
go test ./service/desktop/logintransaction ./service/identity ./service/secrets ./api/desktop/login ./service/desktop/oauth ./router/desktop ./initialize ./initialize/internal ./core ./migrations
go test -tags desktop ./desktop/... ./api/desktop/... ./service/desktop/... ./router/desktop/... ./middleware/...
cd ../desktop/wails && CGO_ENABLED=1 go test -tags desktop ./... && cd ../..
node desktop/scripts/check-desktop-boundaries.mjs
node desktop/scripts/check-bundled-renderer-behavior.mjs
```

These tests use fakes, in-memory repositories, SQLite fixtures, local
`httptest` servers and static SQL contract checks. They verify that `LoginApi`
is in `DesktopApiGroup`, all four paths are mounted by `DesktopLoginRouter` through
`mountDesktopResourceSurface`, RouteSpec assigns the dedicated bootstrap and
transaction credentials, and database-free catalog composition remains
fail-closed. They also cover the database-CAS five-failure budget, canonical
request/capability validation, route security headers, bounded process-local
per-IP limits, untrusted-forwarded-IP default, the 15-second/64-KiB public
header budget, secrets-key readiness, and the Sidecar client's no-cookie/
no-redirect, bounded-response and exact loopback/state/code behavior. The cloud
route inventory contains nine existing proxy routes plus four Login Transaction
routes, for 13 Sidecar-consumed contracts; the local Sidecar inventory contains
18 routes, including four Main-only Login Transaction routes. Coordinator tests
also cover one explicit `http.Client.Do` per stage with no application-level
password replay, cancellation, expiry cleanup, exact scope, Keychain commit
fencing and closed errors. A Main-owned 32-byte canonical Base64URL flow ID is
required on begin/password/cancel; stale A mutations cannot affect replacement
B, an in-flight Begin cannot be overtaken by its Cancel, and neither the ID nor
credentials enter the Renderer response contract. The shell and Renderer
checks cover fixed IPC, main-frame admission, compatibility-fetch blocking,
password clearing and ambiguous-local-response reconciliation without credential
replay. TokenStore tests cover revision/CAS, proactive/401 refresh singleflight,
refresh-vs-login/logout fencing, closed errors, and the SQLite non-secret
tombstone that blocks stale Keychain recovery across Sidecar/SQLite reopen.
Credential HTTP tests cover every token/revoke/userinfo/skills/sync/chat Bearer
path with HTTPS-or-exact-loopback, no Cookie Jar, no redirect, strict token JSON,
and one forced refresh after skills/sync 401. SSE tests cap the aggregate frame,
stop at `done`, and reject trailing events. Shutdown tests close auth admission,
cancel Sidecar auth lifetime synchronously, and honor the caller deadline.
Account-race tests bind Chat's initial token and 401 retry token to the exact
local-history UID: an initial mismatch makes zero Chat business-cloud/cache
effects, while a post-401 mismatch makes zero additional retry/cache effects.
MessagesSyncer tests freeze expected UID at Trigger time and validate it before
the initial messages-delta request and after 401 recovery; mismatch makes no
message upsert or cursor advance, and no initial or additional-retry delta call,
respectively. Threads sync tests require a distinct resume cursor per positive
JWT UID and prove that neither another account's scoped cursor nor the retained
legacy global cursor is consumed. Refresh waiters must honor their own context;
shutdown tests require a timed-out messages drain to keep admission closed and
prevent SQLite close while an owner may still be active. Diagnostics tests require
`auth.persistence_state=ok|degraded|unavailable` so persistence degradation is
not reported as ordinary logout.
Darwin-only fake-command tests verify Keychain secret-via-stdin, bounded command
deadlines and redacted errors without invoking the real macOS Keychain.
Encryption tests install isolated test keys; the
database-free route catalog does not require `WORKMAX_SECRETS_KEY`. These checks
used only fakes, SQLite and local `httptest` servers. They did not read the real
`server/config.yaml`, connect to an external database/MySQL or real cloud, or invoke the
real OS Keychain, and therefore are not packaged/fresh-profile E2E evidence.
They also do **not** prove that `20260672` has been
applied to MySQL, that InnoDB locking/CHECK/collation/timestamp behavior matches
the SQLite/static contract, that a deployed Server has the required
schema/secrets, or that a packaged Desktop can complete fresh-profile sign-in
against the real cloud and OS Keychain.

## 2. Manual Go Server Smoke

Run against the Go Server or API gateway selected by `WORKMAX_CLOUD_BASE`.
The password path is connected in source from the routed Server API through the
Sidecar coordinator and four local privileged routes to the Go `/login/` gate
and the bundled password form. The target MySQL migration and a deployed real
cloud/Keychain path have not been verified here, and Google still lacks its
production adapter/callback. Use an existing valid Desktop session unless the
target Server has `20260672`, stable secrets and the Login Transaction API
deployed. A manual cookie or generic-JWT workaround is not release evidence.

For a production-like Server boot, provision a stable secret through the
process environment: `WORKMAX_SECRETS_KEY` must decode from Base64 to exactly 32
bytes. Missing, malformed or wrong-length values must fail Login API
construction and Server startup. Do not generate a new value on every restart:
the current v1 AES-GCM envelope has no keyring/rotation fallback. Route-catalog
and other database-free offline tests intentionally need no deployment key.

Also record the network topology. Gin currently trusts no forwarding proxy, so
`X-Forwarded-For` must not affect the process-local IP bucket; do not claim
client-IP-grade abuse protection behind a gateway until an explicit trusted
CIDR policy is implemented and tested. Confirm SQL logs retain placeholders and
do not contain submitted passwords, authorization codes or capabilities.

1. Launch Desktop and confirm the window loads the packaged bundled Renderer.
2. Confirm the existing Desktop session loads. On a prepared target Server,
   also exercise fresh-profile password sign-in through the bundled form; track
   it as unverified until the schema, real cloud and packaged Keychain path are
   proven. Track Google separately until its production adapter/callback exists.
3. Confirm the account panel can load `/auth/userinfo`.
4. Wait for thread sync, then confirm the left rail shows active threads only.
5. Open a previously unseen thread and confirm messages appear after on-demand
   sync.
6. Send one chat turn and confirm streaming text arrives, persists, and remains
   visible after refresh/restart.
7. Toggle network offline and confirm history remains readable while send is
   disabled.
8. Restore network and confirm send re-enables.
9. Sign out and confirm the renderer returns to LoginPage.
10. Restart the app and confirm the previous logged-out state is preserved.

Record:

- the single binary's version (there is no separate shell version any more)
- `WORKMAX_CLOUD_BASE`
- data directory path
- `/system/diagnostics` SQLite db path, daily backup path, integrity status,
  applied migrations, and any heap/goroutine trend during the chat turn
- failures with `logs/sidecar-main.log` and `logs/renderer.log`

## 3. Packaged Early-Access Smoke

Build through the wrapper. It runs `inspect-mac-package.sh` on the result before
reporting success, so a bundle that reports ok has already been enumerated: no
unreviewed files, no stale executable, no packaged sidecar binary, entitlements
matching the plist exactly.

```bash
./desktop/scripts/build-mac.sh --preflight-only arm64   # validate inputs only
./desktop/scripts/build-mac.sh arm64
```

x64 is not buildable on an Apple Silicon machine: cgo needs a matching C
toolchain, and the wrapper refuses rather than producing something that fails at
launch. Build it on Intel hardware or set `CC` yourself.

Then exercise the packaged bundle itself, not the working tree. All three of
these resolve the renderer and the data directory the way a user's install
does:

```bash
APP="desktop/wails/release/arm64/WorkMax Desktop.app/Contents/MacOS/WorkMax Desktop"

# The whole user path: unmodified renderer, shipped shim, real sidecar, real
# webview. Asserts the boot sequence from the proxy without touching the page.
"$APP" --verify-app

# The shipped shim satisfies the renderer's bridge contract and streams a turn.
"$APP" --verify-shim

# SSE through WKWebView, byte for byte against a Go control.
"$APP" --kill-check

# Sidecar-only, then drive it with the local smoke helper.
WORKMAX_LOCAL_TOKEN=<token> "$APP" --serve-only
```

Record for each: exit code 0, and for `--verify-app` that the boot sequence
reached `/auth/status`. Signed in, `--verify-app` should also show the
session-gated requests (threads, skill catalog).

**Not yet covered by a helper.** The Electron-era `smoke-packaged-app.sh` was
deleted with that shell and has no replacement. What it uniquely covered:

- a packaged GUI run against a **previously authenticated** cache with an
  unreachable cloud base, proving cached history renders offline through the
  real Keychain path;
- local-token fingerprint rotation across restarts;
- renderer-reported diagnostics from inside a packaged run.

`--verify-app` covers the boot path but not the authenticated-offline case,
because it has no cloud session. Until a replacement exists this stays a
**manual** step, and §4 treats it as an open gate rather than a passed one.

## 4. Public-Release Gates

Do not treat a build as public-release ready until these are cleared or
explicitly waived:

- real packaged GUI smoke against a previously authenticated cache with an
  unreachable cloud base. **There is no helper for this any more** — the
  Electron-era `smoke-packaged-app.sh` was deleted with that shell and has no
  replacement, so this is a manual run: launch the packaged app with
  `WORKMAX_CLOUD_BASE` pointed at an unreachable loopback origin against a data
  directory that already holds an authenticated cache, and confirm cached
  threads and messages render. Repeat against a clean data directory with the
  Desktop Keychain session cleared, and confirm the shell reports an
  unauthenticated state rather than failing open. Writing a replacement helper
  is open W4 work; until it exists this gate is manual and unautomated.
- real-cloud sustained-chat memory observation
- real-cloud 1000-thread sync timing on release hardware
- fresh-profile password and external-IdP Desktop Login Transaction E2E after
  `20260672` is verified on the target MySQL version. The password coordinator,
  four local privileged routes, Main-only IPC and UI now exist in source;
  Google production adapter/callback does not. Hermetic code and 13 consumed
  cloud-route contracts alone are insufficient. Cover exact loopback,
  PKCE, shared pending state, active Device
  Session admission, cancellation/replay, DB-CAS five-failure behavior,
  cross-instance IP/account/device abuse controls, terminal-row cleanup,
  successful-response-loss recovery, DB-authoritative time, key rotation/AAD,
  Keychain persistence, and absence of any Web or generic-JWT-cookie dependency
- packaged logout/refresh persistence fault injection: prove the SQLite
  tombstone survives app restart and blocks stale Keychain credentials when a
  Keychain delete fails. Explicitly resolve or waive the remaining
  marker-write-plus-Keychain-delete double failure and SQLite loss/rollback
  cases. Before supporting standalone Sidecars or multiple data directories,
  replace the current per-data-dir marker/global Keychain-slot combination
  with a global instance lock or a migrated profile/device-scoped Keychain key
- packaged session-epoch revocation: the hermetic code gate now proves login
  replacement (including same-UID re-login) and logout cancel already-authorized
  Chat/UserInfo/Skills/Threads/Messages calls without letting old work adopt a
  new session. Threads/Messages page data and cursor share a transaction-level
  epoch fence. Repeat the matrix in a signed fresh profile with real Keychain,
  SQLite and Cloud; Chat `partial` cache finalization remains intentional local
  recovery work rather than a universal zero-write-after-cancel guarantee
- Server-owned checkout return plus signed-webhook/entitlement-refresh E2E;
  navigation back to Desktop must not be treated as payment proof
- signing and notarization workflow
- decision on whether missing full WorkAgent features are acceptable for the
  target release audience
