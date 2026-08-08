# WorkMax Desktop

Official Desktop Agent client. The current Electron package, bundle ID and
environment variable prefix still use the legacy `workmax` identity for release
compatibility; renaming them is not a Phase 0 task.

WorkMax has only two product/runtime deliverables: `server/` and `desktop/`.
Desktop is the sole user client and Agent UI; Go Server owns every cloud
service. A top-level Web or Admin client must not be restored. Allowlisted live
Renderer URLs below are development inputs only, never a shipped product
surface or a fallback for packaged builds.

- **P0 Spike** complete (2026-05-18) — see
  [SPIKE_REPORT.md](./SPIKE_REPORT.md) for the spike capability
  matrix + known issues + "Go for P1" recommendation.
- **P1 early-access closeout** (2026-05-20) — see
  [P1_COMPLETION_REPORT.md](./P1_COMPLETION_REPORT.md) for the
  early-access Go / public-release No-Go decision, shipped evidence,
  and remaining release gates. The earlier mid-milestone snapshot is
  still available in [P1_CHECKPOINT.md](./P1_CHECKPOINT.md).

The consolidated architecture is documented in
[the Server/Desktop platform design](../ProjectDocs/writer-work-agent-plugin-platform-design-2026-08.md).
Smoke/release-readiness checklist:
[SMOKE_CHECKLIST.md](./SMOKE_CHECKLIST.md).

## Quick verification

```bash
./desktop/scripts/test-desktop.sh
```

This verifies the observed Desktop boundary manifest, Electron shell and
Desktop Go packages. Run `make bootstrap` first when dependencies are absent.

`dev.sh` now defaults explicitly to the repository-bundled static Renderer, so
the launch path is self-contained and does not depend on a Web repository. A
live Renderer may be selected only through the development policy described
below. The bundled Renderer includes an early PPT Agent preview: an authenticated
user can idempotently create a synchronized thread or select an existing one,
inspect local cached history, stream one new turn, and stop that active turn. It
does not attach to a turn after reload or provide the complete
artifact/question-form workbench.

Prerequisites: macOS (Apple Silicon or Intel), Go 1.24.1+,
Node 20.11+ LTS.

## What lives where

| Path | Purpose |
|---|---|
| `desktop/electron/` | Electron main process + preload (`src/`), compiled to `dist/` |
| `desktop/build/` | electron-builder assets, macOS entitlements, and app icon |
| `desktop/scripts/` | `dev.sh` (local) + `build-mac.sh` (release packaging) |
| **`server/cmd/workagent-desktop/`** | Go sidecar binary entry |
| **`server/desktop/`** | Desktop-only Go subpackages (`cloud_proxy/`, `auth/`, `migrations_desktop/`) |
| **`server/api/desktop/`** | Mounted Desktop OAuth/sync/version/Agent APIs plus the Login Transaction Phase 1 Server API |

The Go code lives in `server/` because that's the Go module root
(`server/go.mod`). Only Electron + packaging assets live at the
top-level `desktop/` folder.

## Desktop Login Transaction Phase 1 status

Phase 1 is now a **code-wired fresh-profile password slice, not yet a
production-validated sign-in release**. The current source contains:

- `server/service/desktop/logintransaction/`: a ten-minute state machine that
  freezes the client, exact loopback URI, OAuth state, S256 challenge,
  canonical scope and device; hashes bearer capabilities; persists through an
  in-memory test repository or a shared GORM repository; and uses versioned CAS
  transitions for password, Google-adapter and one-time exchange states. After
  the transaction secret is verified, rejected credentials consume a durable
  database-CAS budget of five attempts; the fifth rejection moves the
  transaction to `failed`, while a wrong transaction secret or identity
  infrastructure error does not consume that budget;
- `server/migrations/20260672_create_desktop_login_transaction.sql`: the
  transaction table plus nullable authorization-code `device_id` for a legacy
  compatibility window. It stores `failed_attempts` / `last_failed_at` and pins
  the five-attempt maximum with a CHECK. The OAuth rename bridge distinguishes
  a fresh prefixed schema, a complete legacy schema and an unsafe mixed schema;
- `server/service/identity/PasswordAuthenticator`: uniform public credential
  failure, bcrypt verification, and successful CAS upgrade of historical MD5
  rows without minting a generic JWT or browser cookie;
- `server/api/desktop/login/`: strict, no-store Create, Status, Password and
  Exchange handlers. Exchange consumes its capability and inserts the existing
  device-bound OAuth authorization code in one database transaction, then
  redirects only to the frozen loopback. The boundary rejects duplicate JSON
  keys and Authorization headers, unknown fields, trailing JSON, oversized
  bodies, non-canonical Base64URL/scope/device values and non-exact loopbacks;
- the mounted legacy `/api/desktop/oauth/token` now validates client,
  redirect, PKCE and any frozen device before a `FOR UPDATE` plus `used=false`
  CAS consumes the code. Legacy codes with a null device remain compatible.

Those pieces are deliberately not described as end to end. `LoginApi` is now
present in `DesktopApiGroup`; `DesktopLoginRouter` mounts the four paths through
`mountDesktopResourceSurface`, and their bootstrap/transaction credential types
are recorded in RouteSpec. With the system database initialized,
`initialize.Routers` constructs the real API; database-free route-catalog tests
retain the paths with an empty API that fails closed. The route layer also adds
no-store/security headers, Desktop client metadata, and bounded process-local
per-IP rate limits. Per-transaction password protection is the durable five-
failure database-CAS budget above; no limiter is keyed by an unauthenticated,
public transaction ID. The production constructor also validates
`WORKMAX_SECRETS_KEY` before serving: it must be Base64-decodable to exactly 32
bytes, otherwise Server startup fails closed. Database-free route-catalog tests
do not require that environment variable. The Gin composition trusts no proxy
by default, so forged `X-Forwarded-For` cannot reset a login bucket; any future
proxy deployment needs an explicit trusted CIDR policy. Public Server header
parsing is bounded to 15 seconds and 64 KiB, and GORM SQL logging is
parameterized so credentials, OAuth codes and capabilities are not expanded
into log statements. No real MySQL migration is implied by the checked-in SQL
or SQLite tests. The production constructor injects only the password adapter;
Google still has only a domain seam and has no provider adapter, start/callback
HTTP routes or dedicated callback configuration.

The current protections are intentionally narrower than full production abuse
and lifecycle control. The five-attempt budget covers one transaction, while
the IP buckets reset per process; there is no cross-instance IP aggregation or
account/device/global abuse policy. Terminal `failed`, `expired` and `exchanged`
rows have no sweeper/retention path. If the successful password response is
lost, only the exchange-token digest is durable, so there is no safe recovery
or reissue protocol. TTL and mutation timestamps use the Server process clock,
not database-authoritative time. The v1 AES-GCM envelope has one active key, no
key ID/keyring/rotation backfill, and nil AAD rather than table/row/column
binding. SQLite and static-SQL tests do not establish real MySQL CHECK,
collation, InnoDB locking, CAS contention, timestamp or failure semantics.

The Sidecar now has a typed cloud client for Create, Inspect, Password and
Exchange in `server/desktop/cloud_proxy/login_transaction_client.go`. It keeps
transaction and exchange capabilities inside the Go call boundary. Every
OAuth/capability/Bearer call — token, revoke, userinfo, skills, sync and chat —
revalidates the Server origin and uses a no-cookie/no-redirect client; HTTPS is
required except exact `127.0.0.1` / `::1` development origins. Token, userinfo,
skills and sync JSON responses use max+1 reads with hard aggregate limits;
Token responses additionally require strict JSON MIME, unique keys, URL-safe
ASCII credentials, Bearer type, bounded lifetimes and exact scope. Login
Transaction additionally validates the exact loopback, OAuth state and unique
authorization code.
Together with the ten proxy routes, including idempotent Agent thread PUT, the
four Login Transaction routes bring the Sidecar cloud-route inventory to 14
consumed contracts.

That client is now owned by a Go `LoginTransactionCoordinator`. It allocates an
actually listening `127.0.0.1` callback, freezes device/scope/state/S256 PKCE,
keeps every transaction/exchange/code/token capability private, performs no
application-level password replay, checks the exact returned scope, saves the
session through `TokenStore`, and generation-fences cancel versus Keychain
commit. Main supplies a canonical 32-byte Base64URL flow ID for each generation;
the coordinator binds Start, Password and precise Cancel to it, so a delayed
request from A cannot mutate replacement B. The final commit fence rechecks both
the pending Context and frozen absolute expiry. A bounded local expiry timer
releases abandoned loopback listeners and allows a new transaction. Four fixed,
Local-Token-protected Sidecar routes require the Main-only flow header on every
mutation but expose only `{state}` or `{state,error}`; the Sidecar route inventory
is therefore 19.

Electron bridge `1.0.0-alpha.7` keeps begin/status/password/cancel as four
Main-only IPC commands. Main generates and retains the flow ID without adding it
to any Preload/Renderer type, pins method/path, omits cookies, rejects redirects,
validates the exact 4 KiB public response envelope and restricts calls to the
main window's main frame. Candidate/active ownership covers repeated/busy Begin,
ambiguous transport, and Begin/Cancel request reordering; Cancel waits for the
same generation's in-flight Begin to settle before its exact DELETE, while a
late A continuation cannot reactivate A or clear B. The compatibility fetch facade blocks `/auth/start`
and the complete `/auth/login-transaction` namespace, including encoded and
trailing-slash lookalikes. The bundled Renderer now provides the email/password
form, recovery, cancellation and submitting-state polling; it clears the
password before IPC and again in `finally`, never logs or persists it, and on an
ambiguous local response checks `/auth/status` without replaying credentials.
The old `/auth/start` flow remains registered only as a deferred compatibility
route and is no longer used by bundled sign-in.

The password path is therefore connected in source without a Web client or
generic JWT cookie. It is still not release evidence: this pass did not read
`server/config.yaml`, apply `20260672`, connect MySQL, call a real cloud Server,
or exercise the packaged app against the macOS Keychain. Google still has only
a domain seam and no production adapter/start/callback. Cross-instance account/
device abuse controls, terminal-row retention, safe recovery after the Server
consumed a successful password response but its exchange capability was lost,
database-authoritative time, key rotation/AAD, real-MySQL semantics and
fresh-profile packaged E2E remain blockers. Production now stores a fixed,
non-secret logout tombstone in local SQLite before mutating Keychain; this blocks
restart resurrection when Keychain deletion fails, while completed credential
writes clear it only after the new pair is durable. Logout waits behind a
refresh only within its request budget; on timeout it still clears locally and
advances the revision so the late refresh cannot restore the session. If the
SQLite marker and Keychain delete both fail, the current process remains logged
out but a restart can still recover stale Keychain bytes; SQLite loss or
rollback has the same residual risk.
The Keychain service/account is still app-global while the Sidecar PID lock and
SQLite marker are per data directory: normal Electron use relies on its app-wide
single-instance lock, and standalone multi-profile support still requires a
global lock or profile/device-scoped Keychain account plus migration.

Agent bearer use is account-bound at both foreground and background boundaries.
Chat derives the expected subject from the active local-history UID and requires
the initial token and the token returned by 401 recovery to carry that exact JWT
UID. An initial mismatch produces no Chat business-cloud request or cache
message; a post-401 mismatch produces no additional retry or cache write.
`MessagesSyncer.Trigger` freezes the selecting account's expected UID and exact
`SessionLease` before its goroutine starts. The job checks that lease before
token acquisition and then uses `SameSession` to match TokenStore identity plus
epoch after acquisition; it does not rely on asynchronous context-cancellation
delivery or a numeric epoch alone. The job also validates any token obtained
after 401 recovery; an initial mismatch produces no messages-delta request, and
a post-401 mismatch produces no additional retry. Neither mismatch can upsert
messages or advance the cursor. Threads delta resume points are also UID-scoped;
the legacy global cursor remains readable for diagnostics but is never consumed
by production sync, so a newly signed-in account always starts from its own
history position. `/system/diagnostics` exposes TokenStore
durability separately as `auth.persistence_state`: `ok`, `degraded`, or
`unavailable`.

Bearer requests are now bound to a TokenStore session epoch that is independent
from credential revision. Login replacement (including same-UID re-login),
logout and explicit fencing cancel the old lease with `session_changed`; a
same-session access-token refresh advances revision without replacing the
epoch. Chat, UserInfo, Skills, Threads and Messages bind their cloud requests
and one allowed 401 recovery to the original lease, so old work cannot adopt a
new login. UserInfo and Skills expose this as a closed HTTP 409 response instead
of treating it as authentication failure or a soft empty catalog.

For Threads and Messages, the complete short local `Begin -> entity/cursor
write -> Commit` transaction runs inside `SessionLease.WithCurrent`. This keeps
the shared tombstone path and sync path in the same `TokenStore -> SQLite` lock
order; code must never open/write a SQLite transaction and then acquire the
session guard. If replacement wins first, the transaction callback is not run
and the page has zero writes. If the transaction wins first, the whole page
commits legally before the waiting `Save/Clear` starts the new epoch. Chat
intentionally may finalize an already-created streaming cache row as `partial`
after cancellation; this is recovery metadata, so the implementation does not
claim a universal "zero local writes after cancel" contract. Packaged
fresh-profile testing against real Keychain/SQLite and the remaining
tombstone-plus-Keychain double-failure case are still release gates.

## Dev workflow

### Daily loop

```bash
./desktop/scripts/dev.sh
```

This command explicitly selects the repository-bundled Renderer unless a safe
development override is configured. It is not the default verification
command for this imported baseline.

What it does (script is self-documenting at the top):
1. Detect host platform → `GOOS`/`GOARCH`
2. Build the Go sidecar with `-tags desktop` into `desktop/electron/bin/workagent-desktop`
3. `npm ci` if `node_modules/`, local `tsc`, or local `electron` is missing
4. `tsc -p .` in `desktop/electron/`
5. Select the bundled, loopback, or allowlisted HTTPS development Renderer
6. `npm start` (which is `electron .`)

To use a live local Renderer, configure an exact `/desktop` route:

```bash
WORKMAX_DESKTOP_RENDERER_URL=http://127.0.0.1:3000/en/desktop \
  ./desktop/scripts/dev.sh
```

Remote development Renderers must use HTTPS and require a matching bare Origin
allowlist entry:

```bash
WORKMAX_DESKTOP_RENDERER_URL=https://desktop.dev.example/en/desktop \
WORKMAX_DESKTOP_TRUSTED_RENDERER_ORIGINS=https://desktop.dev.example \
  ./desktop/scripts/dev.sh
```

Packaged applications reject `WORKMAX_DESKTOP_RENDERER_URL` and fail startup when
`Resources/renderer/en/desktop/index.html` is absent. There is no hosted
Renderer fallback. Current password sign-in stays inside the bundled form,
typed Main IPC and Go Coordinator; it does not launch a browser. The legacy
`/auth/start` route is retained only for deferred compatibility and has no
Bundled Renderer entry point.

Sidecar logs land on **stderr** (visible in your terminal) and Electron also
persists them, along with SidecarManager lifecycle diagnostics, under
`<dataDir>/logs/sidecar-main.log` with targeted token and URL-credential
redaction across message strings and structured fields. SidecarManager applies
the same redaction before logging sidecar output or malformed handshake
excerpts. stdout is reserved for the one-shot handshake JSON that Electron
parses; any later stdout line is treated as an unexpected diagnostic. Renderer logs are written by the sidecar to
`<dataDir>/logs/renderer.log` with the same best-effort token and URL-credential
redaction applied in the renderer helper and again before disk append;
Diagnostics → **Open logs** reveals the data directory.

### Working on just one layer

If you're iterating on the Go sidecar only and don't need Electron
rebuilds:

```bash
cd server
GOOS=darwin GOARCH=arm64 WORKMAX_LOCAL_TOKEN=$(uuidgen) \
  WORKMAX_DESKTOP_SKIP_STDIN_WATCHER=1 \
  go run -tags desktop ./cmd/workagent-desktop
```

The `SKIP_STDIN_WATCHER` env var prevents the sidecar from shutting
down when stdin is `/dev/null` (which is what you get when running
under `go run` from a tty without piping anything in). Token-via-env
lets you `curl` the local HTTP endpoints:

```bash
# In another terminal (use the port printed in the handshake on stdout):
curl -H "X-Local-Token: $WORKMAX_LOCAL_TOKEN" http://127.0.0.1:<port>/health
```

For a repeatable local sidecar smoke after the process is running:

```bash
WORKMAX_LOCAL_TOKEN=<token> ./desktop/scripts/smoke-local.sh --port <port>
WORKMAX_LOCAL_TOKEN=<token> ./desktop/scripts/smoke-local.sh --port <port> --expect-version 0.1.0-p1-ea
WORKMAX_LOCAL_TOKEN=<token> ./desktop/scripts/smoke-local.sh --port <port> --check-token-rejection
WORKMAX_LOCAL_TOKEN=<token> ./desktop/scripts/smoke-local.sh --port <port> --trigger-sync
WORKMAX_LOCAL_TOKEN=<token> ./desktop/scripts/smoke-local.sh --port <port> --with-userinfo
WORKMAX_LOCAL_TOKEN=<token> ./desktop/scripts/smoke-local.sh --port <port> --with-server-version
WORKMAX_LOCAL_TOKEN=<token> ./desktop/scripts/smoke-local.sh --port <port> --with-skills-catalog
WORKMAX_LOCAL_TOKEN=<token> ./desktop/scripts/smoke-local.sh --port <port> --check-pid-lock --sidecar-binary desktop/electron/bin/workagent-desktop
WORKMAX_LOCAL_TOKEN=<token> ./desktop/scripts/smoke-local.sh --port <port> --diagnostics-samples 6 --diagnostics-interval 10
```

This checks `/health`, `/auth/status`, `/system/diagnostics`,
and the active thread list, and verifies diagnostics contains a non-empty
sidecar version, readable SQLite data/db/backup paths, `integrity_check: "ok"`,
non-empty applied SQLite migrations, and Go heap/goroutine counters. Add
`--expect-version <version>` to make the diagnostics version an assertion. Add
`--check-token-rejection` to verify
missing and wrong `X-Local-Token` requests are rejected across representative
loopback read/write surfaces: `/health`, `/auth/status`, `/system/diagnostics`,
`/agent/threads`, `/system/log`, and `/system/trigger-sync`. Add `--with-userinfo`
after OAuth sign-in to also verify the Account panel's `/auth/userinfo` path.
Add `--with-server-version` when the sidecar has cloud client/network access and
you want to verify the update-floor path. Add `--with-skills-catalog` after
OAuth sign-in to verify the cloud-backed `/agent/skills/catalog` path and
Desktop allowlist filtering; the helper asserts `allowed_modes` includes `ppt`,
`items` is an array, `count === items.length`, and every returned item is
allowlisted. Add `--trigger-sync` only after the sidecar has an active Desktop
session; when TokenStore is wired but the refresh chain is missing or expired,
`/system/trigger-sync` returns 401 and the script reports "login required before
manual sync". The smoke helper accepts only loopback origin `--base` values and
rejects credentials, paths, query strings, and fragments so the local token is
not sent to a remote URL. Each curl request has a bounded timeout.
Loopback request bodies are bounded before expensive handling: `/agent/chat`
caps the local JSON envelope at 1 MiB before proxying to cloud, while
`/system/log` keeps its 64 KiB per-entry cap before writing `renderer.log`.
Add `--check-pid-lock --sidecar-binary <path>` to launch a second sidecar
against the diagnostics-reported data directory and verify it fails before
opening SQLite. The second-sidecar probe is also bounded by
`--pid-lock-timeout` so a PID-lock regression fails the smoke instead of
leaving the helper blocked on a successfully started sidecar.
Use `--diagnostics-samples` during a manual cloud chat smoke to capture a short
heap/goroutine time series from `/system/diagnostics`; every captured sample is
validated for readable SQLite data/db/backup paths, `integrity_check: "ok"`,
non-empty migrations, and runtime counters. The script does not complete OAuth
or send a real cloud chat turn; use it as the local half of the P1
release-readiness smoke. Verified local-only flow: launch the sidecar with a
temporary `WORKMAX_DESKTOP_DATA_DIR`, run the smoke command above, then stop the
sidecar with Ctrl-C.

If you're iterating on a live local Renderer only:

```bash
cd desktop/electron
npm run watch       # tsc --watch in one terminal
# In another terminal, point dev.sh at the local /desktop URL as shown above.
```

### Testing

Go-side (sidecar):

```bash
cd server
go test -tags desktop ./desktop/...
go test -tags desktop -race ./desktop/...
```

Broader Desktop backend sweep (sidecar + cloud desktop API + OAuth/sync
services + middleware):

```bash
cd server
GOCACHE=/tmp/workmax-go-build go test -tags desktop ./desktop/... ./api/desktop/... ./service/desktop/... ./router/desktop/... ./middleware/... ./cmd/workagent-desktop
```

Focused Login Transaction Phase 1 candidate checks:

```bash
cd server
go test ./service/desktop/logintransaction ./service/identity ./service/secrets ./api/desktop/login ./service/desktop/oauth ./router/desktop ./initialize ./initialize/internal ./core ./migrations
go test -tags desktop ./desktop/cloud_proxy
```

These focused commands use fakes, SQLite, local `httptest` servers and static
migration-contract tests. They do not read the real YAML configuration,
connect to MySQL, prove a deployed schema or constitute packaged real-cloud
fresh-profile E2E evidence. The listed package suites passed against the
2026-08-06 worktree, including the Server Router/RouteSpec, durable
five-failure budget, secrets readiness, Sidecar typed client/coordinator/local
routes, cancel/expiry/session-write fences, TokenStore revision/CAS and refresh
singleflight against login/logout, canonical input, trusted-proxy,
public-header-budget and database-free fail-closed composition. Darwin-only
fake-command tests also verify Keychain secret-via-stdin, five-second production
deadlines and redacted command errors without invoking the real Keychain. Tests
install isolated keys where encryption is exercised; the database-free route
catalog does not require `WORKMAX_SECRETS_KEY`.

This identity-safety pass also covers Chat initial/401 subject binding with no
mismatched business-cloud or cache effects, MessagesSyncer trigger-time exact
lease/UID freezing, different-TokenStore same-numeric-epoch rejection,
initial/401 subject mismatch with no message/cursor writes, replacement-first
zero-write and transaction-first commit linearization on a shared SQLite
tombstone database, account-scoped Threads cursors, cancellable refresh-gate
waits, bounded messages Drain, and the three diagnostics persistence states. It used only fakes, SQLite and local
`httptest` servers; it did not read `server/config.yaml`, connect to any
external database/MySQL or real cloud, or invoke the real OS Keychain. This is
contract-level evidence, not packaged or fresh-profile E2E.

Electron shell tests:

```bash
cd desktop/electron
npm test
cd ../..
node desktop/scripts/check-desktop-boundaries.mjs
node desktop/scripts/check-bundled-renderer-behavior.mjs
```

This suite includes the preload bridge runtime-surface invariant, fail-closed
Renderer selection, route/redirect guards, privileged sign-in IPC validation, and
SidecarManager lifecycle coverage.

Mac package inspector regression tests:

```bash
./desktop/scripts/build-mac.test.sh
./desktop/scripts/check-bundled-renderer.test.sh
./desktop/scripts/smoke-local.test.sh
./desktop/scripts/smoke-packaged-app.test.sh
./desktop/scripts/inspect-mac-package.test.sh
./desktop/scripts/notarize-mac.test.sh
```

The build wrapper test covers argument validation and the `--preflight-only`
path against the current packaging inputs, including electron-builder config
drift for output directory, hardened runtime, runtime entry allowlist, bundled
renderer resources, `publish: null`, and rejection of `extraFiles`,
`afterSign`, or inline `mac.notarize` side-effect hooks. The bundled-renderer test verifies
source preflight failures before packaging: missing files, missing or unsafe
CSP, extra remote CSP connect sources, non-relative asset references, and
unexpected or token-like embedded files. Its behavior probe also rejects
malformed auth/OAuth/cache payloads before the static shell renders state or
invokes the sign-in IPC. The smoke helper test verifies local validation paths,
including unsafe `--base` values, before any token-bearing curl can run. The
package inspector test
creates disposable fake `.app` bundles under `/tmp` and verifies that the
inspector accepts minimal valid unpacked and `app.asar` payloads while rejecting
ambiguous payload modes, sidecar duplication inside either Electron payload
mode, package main drift, missing compiled runtime entries, and extra unexpected
payload entries. Bundled-renderer package inspection parses the CSP meta tag and
requires the exact static-shell directives, including loopback-only
`connect-src http://127.0.0.1:*`. The notarization helper test uses disposable
fake DMG/app
fixtures to verify local validation paths: unsupported targets, missing
artifacts, missing credentials, release-DMG app-bundle inference,
keychain-profile dry-run, Apple ID credential dry-run, and the Developer ID
gate. It proves ad-hoc signatures, missing TeamIdentifier, non-Developer-ID
authority chains, and strict codesign verification failures are rejected before
Apple submission, and requires the Developer ID signature to report a
`Developer ID Application` authority and hardened runtime metadata.
The packaged-smoke helper test covers prelaunch validation paths such as missing
app path, missing values for value-taking options, invalid timeout,
negative-mode option pairing, incompatible cached-history/token-rotation modes,
missing app bundle shape, and missing `Info.plist` / `CFBundleExecutable`. It
also stubs `codesign` against a fake
`.app` to verify valid signatures pass strict verification, ad-hoc signatures
are repaired for controlled local smoke, and other signature failures are not
auto-repaired.

TypeScript type-check:

```bash
cd desktop/electron
npm run lint:types
```

It should be silent. The Go production build path must also stay clean:

```bash
cd server
go build ./...   # NO -tags desktop; production server build
```

If `go build ./...` (without the tag) fails, you've leaked a desktop
import into a file that isn't gated. Each desktop-only `.go` file
must start with `//go:build desktop`.

When manually checking the sidecar build, write the binary outside the repo or
to `desktop/electron/bin/` via the existing scripts:

```bash
cd server
go build -tags desktop -o /tmp/workmax-workagent-desktop-smoke ./cmd/workagent-desktop
```

### Useful env vars

| Var | Purpose | Default |
|---|---|---|
| `WORKMAX_DESKTOP_DATA_DIR` | SQLite + state dir | `~/.workmax` |
| `WORKMAX_LOCAL_TOKEN` | X-Local-Token (renderer→sidecar auth) | auto-generated |
| `WORKMAX_CLOUD_BASE` | Direct Go Server API origin (OAuth + Cloud Proxy target) | official hosted Server gateway |
| `WORKMAX_DESKTOP_RENDERER_URL` | Explicit development Renderer URL | `dev.sh`: repository bundle; direct Electron: required; packaged: rejected |
| `WORKMAX_DESKTOP_TRUSTED_RENDERER_ORIGINS` | Comma-separated bare HTTPS Origins allowed for remote development Renderer URLs | unset |
| `WORKMAX_DESKTOP_OPEN_DEVTOOLS` | Force Electron devtools in packaged builds | unset |
| `WORKMAX_DESKTOP_SKIP_STDIN_WATCHER` | Don't shut down on stdin EOF (curl smoke tests) | unset |

Point at a local cloud dev server:

```bash
WORKMAX_CLOUD_BASE=http://127.0.0.1:8888 ./desktop/scripts/dev.sh
```

`WORKMAX_CLOUD_BASE` must be a bare HTTPS origin, for example
`https://api.example.invalid`. Plain HTTP is accepted only for the exact IP
loopback hosts `127.0.0.1` and `::1`, such as `http://127.0.0.1:8888`; the
hostname `localhost` is intentionally not an exception. The sidecar rejects
credentials, paths, query strings, and fragments at startup because cloud routes
are appended as absolute `/api/...` paths. Self-hosted builds must point this
directly at Go Server or an explicitly configured API gateway, never at a
separate browser application.

### Versioned preload bridge

Preload exposes `window.desktopBridge` version `1.0.0-alpha.7` alongside the
`window.workmaxLocal` compatibility surface. The bundled Renderer can continue
using `workmaxLocal.fetch` for non-privileged local auth, history and system
operations. The compatibility fetch explicitly rejects Agent chat and skill
catalog paths, `PUT /agent/threads/:uuid`, turn recovery mutations, and
`/settings/model-route` (including encoded, dot-segment, query and
trailing-slash lookalikes). The method-aware thread guard preserves legacy GET
list/message history. Sign-in, Agent mutation/streaming, and model-route
settings use the typed facade.

The typed slice exposes only real current behavior:

- `auth.status`, `auth.userInfo`, `auth.beginLogin`, `auth.loginStatus`,
  `auth.submitLoginPassword`, `auth.cancelLogin`, and `auth.logout`;
- `history.listThreads` and `history.listMessages` over the local cache;
- `agent.listSkills`, idempotent asynchronous
  `agent.createThread({threadUUID,name,agentMode})`,
  `agent.listRecoverableTurns` / `agent.resumeTurn` / `agent.cancelTurn`,
  and synchronous `agent.startTurn(input, callback)` returning `{turnID}`;
- `settings.getModelRoute` and `settings.putModelRoute` (local vs official
  model route; API keys stay in Keychain and never appear in GET responses);
- `system.health`, `system.diagnostics`, `system.serverVersion`,
  `system.triggerSync`, `system.writeLog`, and reveal-data-directory IPC.

Sidecar loopback inventory is **24** routes (policy table +
`desktop-boundaries.v0.json`), including `GET|PUT /settings/model-route`.
Local model **inference** is not wired yet: saving a local route preference
does not switch PPT chat off the cloud path.

Each HTTP method owns its Method, route template, query allowlist, body policy,
content type and body limit. Callers cannot supply a URL or `RequestInit`.
The four login-transaction methods are privileged: Renderer uses fixed typed
IPC, Electron Main calls only the declared `/auth/login-transaction` routes with
the Local Token, and the Renderer receives only the closed public state/error
envelope. Password is the one permitted transient input; transaction secret,
exchange token, OAuth state, PKCE verifier, callback port, authorization code
and OAuth tokens stay in Go. Neither `/auth/start` nor the privileged namespace
is reachable through the compatibility bridge. The Sidecar alone owns the
loopback binding, cloud capability exchange, token exchange and Keychain save.
Agent create accepts only a canonical caller-generated v4 UUID, a bounded name,
and an enabled mode. Preload owns fixed `PUT /agent/threads/:uuid` and emits the
exact `{name,agent_mode}` body. Preload rejects malformed successful create
responses, including wrong status/state pairs, foreign UUIDs, extra fields and
invalid local rows; the returned `cloud_sync_state` remains the server's exact
allowlisted `synced` or `paused` value. `202 pending_local_sync` retains the same
UUID and request fields for an idempotent retry. Agent start accepts only
`{threadUUID,userText,chatMode}` plus an event callback;
Renderer cannot supply a URL, `RequestInit`, token, raw `Response`/reader,
`AbortSignal`, cloud conversation ID or arbitrary Sidecar payload. Preload owns
the fixed `/agent/chat` fetch, reader and abort controller, bounds every SSE
frame to 1 MiB, and emits only `text_delta`, `unknown`, `done`, `proxy_error`,
`canceled` or `protocol_error` DTOs with one terminal event. `cancelTurn` acts
only on the matching active Preload turn and invokes reader cancellation plus
fetch abort without exposing either primitive.

`desktopBridge.capabilities()` therefore reports Agent as supported with
`listSkills`, `createThread`, `startTurn` and `cancelTurn`; attach, reconnect and
artifact work remain deferred. Artifact is still unsupported and network-state
SSE remains a deferred System method. The bundled PPT preview can create a
synced thread from its compact sidebar form or continue one from local history.
It holds one `crypto.randomUUID()` identity across explicitly retryable 5xx and
202 responses. Once creation has been attempted, refresh and thread switching
are blocked until that draft succeeds or is explicitly canceled. Authentication
and identity conflicts are permanent for that draft and never offer same-UUID
retry. A replayed `paused` local row is preserved as paused, is not inserted or
selected as a resumable thread, and requires explicit cancellation. A synced
ready row is selected, then waits for an explicit first prompt. Generation
fences still ignore late cancel/account-change results. Older alpha.4 bridges
retain existing-thread chat while New remains unavailable. `session_changed`
HTTP/SSE failures return to authenticated recovery without replaying the
submitted prompt or thread creation. Durable attach/replay after reload and the
artifact/question-form workbench remain future contracts.

### Release build (macOS)

```bash
./desktop/scripts/build-mac.sh            # host arch
./desktop/scripts/build-mac.sh arm64      # Apple Silicon
./desktop/scripts/build-mac.sh x64        # Intel
./desktop/scripts/build-mac.sh --preflight-only arm64
./desktop/scripts/build-mac.sh --public-release arm64

# Public distribution only, after Developer ID signing:
WORKMAX_NOTARY_KEYCHAIN_PROFILE=workmax-notary ./desktop/scripts/notarize-mac.sh arm64

# Equivalent npm wrappers from desktop/electron/:
cd desktop/electron
npm run dist:mac
npm run dist:mac:arm64
npm run dist:mac:x64
npm run dist:mac:public:arm64
npm run notarize:mac:arm64
```

Cross-compiles the Go sidecar with the Electron package version
embedded into `server/desktop/buildinfo.Version`, compiles Electron,
runs electron-builder, inspects the generated `.app`, then drops a `.dmg` +
`.zip` in `desktop/electron/release/` named
`WorkMax Desktop-<version>-<arch>.<ext>`.
The build wrapper verifies the freshly built sidecar contains the expected
Electron package version before invoking electron-builder, and verifies the
expected `.dmg` and `.zip` artifacts exist and are non-empty before reporting
success. `--preflight-only` runs the static packaging input checks without
compiling or packaging; it is useful for script regression tests and local
sanity checks, but it does not replace a real build. The preflight also rejects
`extraFiles`, `afterSign`, and inline `mac.notarize` so artifact-shape changes
and Apple submission stay behind the reviewed package inspector and
`notarize-mac.sh` gates.

Use these wrappers instead of invoking `electron-builder` directly. Direct
`electron-builder` packaging only copies the already-present
`desktop/electron/bin/workagent-desktop` binary; it does not rebuild the sidecar
or inject the Electron package version, so it can produce a stale bundle that
fails the startup version check. The wrapper installs Electron dependencies when
the local `tsc` or `electron-builder` binary is missing, then invokes
`./node_modules/.bin/electron-builder` so packaging uses the version pinned in
`desktop/electron/package-lock.json`.
The wrapper runs `desktop/scripts/inspect-mac-package.sh` on the generated
`.app` for the requested arch before reporting success. That verifies the bundle
id, app version, packaged main executable, Electron app payload, sidecar path,
executable bits, and sidecar version marker before launch. The inspector also
requires exactly one Electron payload mode (`app.asar` or `Resources/app`),
checks the payload package name/main/version plus all compiled runtime entry
points, and rejects any unexpected payload entry. Known-bad examples include
compiled test files, source maps, source files, nested `.app` bundles, stale
`release/` / `dist/mac*` package output, or a duplicated
`workagent-desktop` sidecar in both asar and unpacked packaging modes. It also
rejects unexpected `app.asar.unpacked` content and unexpected top-level
`Contents/Resources` entries so files cannot bypass the asar/runtime resource
allowlists. It reports signing, strict codesign verification, and Gatekeeper
assessment status.
Every packaged channel requires and inspects the bundled Renderer; the app
aborts startup if it is missing and never falls back to hosted JavaScript.
Public-release candidates must build with
`desktop/scripts/build-mac.sh --public-release <arch>` or the equivalent
`npm run dist:mac:public:<arch>` wrapper. That mode adds the app-icon and
Developer ID signature gates and fails unless
`Contents/Resources/renderer/en/desktop/index.html`, `styles.css`,
`renderer.js`, and `Contents/Resources/icon.icns` are present and non-empty,
and the app has a `codesign -dv` `Authority=Developer ID Application: ...`
hardened-runtime signature that passes strict codesign verification. It also
verifies exact packaged renderer CSP, relative asset references, absence of
unexpected or token-like embedded files, the icon file's `icns` header, and
`CFBundleIconFile=icon.icns`. The CSP gate rejects both non-loopback
`connect-src` replacements and added remote `connect-src` fallbacks.
Run the inspector manually only when checking an already-built or extracted app
bundle.

Config lives at `desktop/electron/electron-builder.yml`. App bundle
id is `ai.workmax.desktop` — matches the macOS Keychain service the
sidecar uses, so a packaged build sees the same session entries the
dev (un-bundled) build wrote. Entitlements at
`desktop/build/entitlements.mac.plist` are minimal: JIT (Electron
V8), unsigned-executable-memory (Electron V8), dyld-env-vars, and
network.client. Everything else is intentionally absent — adding
more entitlements without a justified reason gets the binary
rejected by notarization. The build preflight pins this exact entitlement set
and rejects both missing required entitlements and unexpected
`com.apple.security.*` keys.

Notarization is a separate public-distribution step. After building a signed
DMG with a Developer ID Application certificate, run
`desktop/scripts/notarize-mac.sh arm64` or `desktop/scripts/notarize-mac.sh x64`.
The script first re-runs the package inspector in public-release mode, requiring
the bundled renderer entry, custom app icon, and Developer ID Application
hardened-runtime signature, then rejects unsigned/ad-hoc bundles, requires a
`TeamIdentifier`, `Authority=Developer ID Application: ...`, and hardened
runtime signature metadata, runs strict `codesign --verify --deep` on the
`.app`, submits the DMG with
`xcrun notarytool`, staples the ticket, and verifies the result. It accepts
either `WORKMAX_NOTARY_KEYCHAIN_PROFILE` or
`APPLE_ID` + `APPLE_TEAM_ID` + `APPLE_APP_SPECIFIC_PASSWORD`; use `--dry-run`
to validate local paths, bundled-renderer/icon readiness, signing state, and
credentials without contacting Apple. The legacy-named
`--allow-hosted-renderer` option bypasses only structural bundle inspection for
controlled dry-runs; it cannot enable a hosted Renderer in the application.
The icon gate still applies, and Apple submission with that option is rejected.
Passing a versioned release DMG path such as
`release/WorkMax Desktop-<version>-arm64.dmg` still infers and inspects the
neighboring `release/mac-arm64/WorkMax Desktop.app`; arbitrary DMG paths are
accepted only with `--allow-hosted-renderer --dry-run` because the script cannot
prove their contained app bundle. Non-dry-run submission requires an arch target
or a versioned release DMG path that maps to an inspectable neighboring `.app`.

The electron-builder file list is intentionally narrow. The app payload should
contain only the compiled runtime entry points and `package.json`, not the whole
`dist/` directory. This prevents stale package output such as nested
`dist/mac*/*.app` bundles, test files, source maps, or release artifacts from
being captured recursively into `app.asar`. The package inspector opens
`app.asar` and rejects any entry outside the expected `dist/*.js` runtime set
and `package.json` before a build is treated as usable. It also rejects a second
`Resources/app` payload beside `app.asar`, rejects unexpected
`app.asar.unpacked` content, and verifies that
`workagent-desktop` is only present as `Resources/workagent-desktop`, not inside
the Electron JS payload.

The sidecar executable is packaged as an `extraResources` file at
`Resources/workagent-desktop`; Electron resolves that path via
`process.resourcesPath` in packaged builds. In dev it still uses
`desktop/electron/bin/workagent-desktop`.

Renderer URL resolution is build-mode aware:

- `dev.sh` explicitly defaults to the repository Renderer under
  `desktop/renderer/en/desktop/`.
- Development `WORKMAX_DESKTOP_RENDERER_URL` may select that exact file or an
  exact loopback `/desktop` route. Remote development requires HTTPS plus a
  matching bare Origin in `WORKMAX_DESKTOP_TRUSTED_RENDERER_ORIGINS`.
- Packaged builds require
  `Resources/renderer/en/desktop/index.html`, which electron-builder copies as
  an extra resource. Missing files abort startup.
- Packaged builds reject `WORKMAX_DESKTOP_RENDERER_URL`, including otherwise valid
  remote `/desktop` routes.

Because the renderer receives privileged preload bridges, Electron
locks the main window to the configured desktop route. In-window navigation
outside that route is blocked, and popups / external links are opened through
the OS browser instead of a bridged Electron window only when the target is a
non-credential, non-local/private HTTP(S) URL. OS permission prompts from the
main renderer session are denied by default. Privileged IPC handlers also verify
the sender URL before starting sign-in or revealing the data directory.

Desktop is also single-instance at the Electron layer. A second launch focuses
the existing window instead of spawning another sidecar. The sidecar also
claims `<dataDir>/sidecar.pid` before opening SQLite, so standalone sidecar
launches using the same data directory fail fast when another live sidecar owns
the cache. Stale or corrupt pid files are removed and replaced during startup.

Current password Login Transaction does not open an OAuth page: the bundled
form calls four fixed Main-only commands, and Go keeps the callback binding,
PKCE, capabilities, code and tokens outside Renderer memory. The old
`/auth/start` authorize route remains registered only as deferred compatibility;
the compatibility fetch facade blocks it and Main/Preload expose no browser-open
command for it. A future Google/external-IdP flow may hand an allowlisted URL to
the system browser only after a dedicated Main contract and callback adapter are
implemented and reviewed; that flow is not present today.

The bundled static renderer path now exists and is covered by source/package
inspection plus packaged runtime smoke. The seeded cached-history smoke launches
the `.app` against an unreachable `WORKMAX_CLOUD_BASE=http://127.0.0.1:9`, so it
proves the bundled renderer can read a local cached thread/message without a
live hosted renderer or cloud dependency. The packaged smoke also validates
live sidecar diagnostics: readable SQLite data/db/backup paths,
`integrity_check: "ok"`, non-empty applied SQLite migrations, and Go
heap/goroutine counters. It verifies missing/wrong local-token rejection across
representative packaged sidecar read/write surfaces, with duplicate-header
rejection pinned on `/health`. The Electron reporter redacts and flags any raw
local token or token-like renderer text found in renderer-observation fields
before writing the smoke artifact, and the shell helper requires both leak
flags and redaction markers to be absent from a positive result. It also
treats a non-zero packaged app process exit as failure even when a renderer
smoke result was written. The helper now has an opt-in real-cache mode for the
public-release offline gate:
`--data-dir <existing-dir> --expect-thread-text <text>
--expect-message-text <text>` launches against an existing Desktop data dir
without seeding smoke rows and forces
`WORKMAX_CLOUD_BASE=http://127.0.0.1:9`, so a previously authenticated
Keychain-backed cache can be verified without a live cloud dependency. It also
supports
`--expect-body-text "Auth state: unauthenticated"` for the clean-data-dir,
no-session packaged path after clearing the Desktop Keychain session. The helper
rejects `--data-dir` unless one of those text assertions is present, so an
existing profile cannot be smoke-launched accidentally without the forced
unreachable cloud base. Public
release still needs that real offline GUI smoke evidence. Until that
evidence exists, packaged builds are suitable for controlled smoke only. The
public-release gate and implementation evidence are maintained by this README
together with `desktop/scripts/smoke-packaged-app.sh` and its tests.

**Not yet shipped**:

- Complete Desktop Agent workbench — the bundled PPT preview can idempotently
  create a synchronized thread, stream a turn, and stop it. It still has no
  durable attach/replay across reload or artifact/question-form workflow, and
  has no real-cloud packaged evidence.
- Fresh-profile Desktop Login Transaction production E2E — the password chain
  is connected in source through Server API, Sidecar coordinator and four local
  privileged routes, Electron Main-only IPC, bundled password UI and Keychain
  commit. It has hermetic tests but no target-MySQL migration/semantics,
  real-cloud, real-Keychain packaged evidence. Google adapter/callback,
  cross-instance account/device abuse controls, terminal cleanup, recovery from
  a lost successful Server password response, DB-authoritative time and key
  rotation/AAD are also absent.
- Notarization — helper script exists at `desktop/scripts/notarize-mac.sh`,
  but public distribution still needs a Developer ID Application certificate
  and Apple notary credentials configured in CI/local env. Until then, the
  `.dmg` will trigger Gatekeeper warnings on a different machine. The current
  build host has no valid Developer ID Application certificate, so
  electron-builder skips application signing.
- Packaged offline renderer smoke — seeded packaged smoke exists, but release
  still needs a packaged `.app` launch against cached local history from a
  previously authenticated Keychain-backed session with the helper-enforced
  unreachable sidecar cloud base.

## Version Pins

Electron `package.json` version and the sidecar default version must match.
`desktop/electron/src/main.ts` fails fast after the sidecar handshake if they
diverge, which catches stale bundled sidecar binaries during packaging or local
dev. `desktop/scripts/dev.sh` and `desktop/scripts/build-mac.sh` read the
Electron package version and inject it into the Go sidecar with
`-ldflags -X server/desktop/buildinfo.Version=...`.

| Component | Pinned | Source of truth |
|---|---|---|
| Desktop app | 0.1.0-p1-ea | `desktop/electron/package.json` |
| Sidecar | 0.1.0-p1-ea default; CI can override via `-ldflags -X server/desktop/buildinfo.Version=...` | `server/desktop/buildinfo/buildinfo.go` |
| Go | 1.24.1 | `server/go.mod` |
| TypeScript | 5.8.3+ | `desktop/electron/package.json` |
| Node | 20.11+ LTS | not pinned in repo yet |
| Electron | 40.x | `desktop/electron/package.json` |
| electron-builder | 25.x | `desktop/electron/package.json` |

## On `//go:build desktop`

The sidecar binary requires the `desktop` build tag. Files like
`server/desktop/db.go` and `server/desktop/cloud_proxy/*` use
`//go:build desktop` so they're invisible to normal cloud builds. The
cloud server build (`go build ./...`) does not see them and continues
to use MySQL + Redis + account pool as before.

If a new desktop file needs to import an existing production package
(e.g. `server/utils`), that's fine — the build tag flows one way.
What you must NOT do is have a production file `import "server/desktop/..."`
— the desktop package literally doesn't exist outside `-tags desktop`.

## Historical P0 spike status

This table records the original spike milestones. It does not override the
current Login Transaction boundary above; in particular, completion of the
legacy OAuth flow does not mean clean-profile password or Google sign-in is
available.

| Task | Status | Commit(s) |
|---|---|---|
| P0.1 — scaffold + sidecar stub | ✅ | (pre-spike) |
| P0.2 — SQLite + 4-table migration | ✅ | (pre-spike) |
| P0.3 — sidecar HTTP + handshake + /health | ✅ | (pre-spike) |
| P0.4 — Electron main + preload | ✅ | (pre-spike) |
| P0.5 — Renderer LoginPage + apiPaths | ✅ | (pre-spike) |
| P-1.1..P-1.7 — backend OAuth Authorization Server | ✅ | `b8d3a02f` → `ff8b4f19` |
| P0.6a..d — OAuth integration (Keychain + Flow; historical BrowserWindow later replaced by guarded system-browser launch) | ✅ legacy flow only | `1f1d5dbe`, `c86151cd`, `77a7cc61`, `81847c1f` (+ `9c59bf23` re-stage) |
| P0.7a..d — Cloud Proxy (classifier + cache + relay + endpoint) | ✅ | `24361c21`, `e1b1ed3f`, `8fcd0666`, `015684ca` |
| P0.8 — Skill allowlist + chat-mode gate | ✅ | `7009f633` |
| P0.9a..c — network-state SSE + local reads + banner | ✅ | `e2acc8e7`, `26eb2653`, `5308b295` |
| P0.10 — SPIKE_REPORT + dev polish | ✅ | *(this commit)* |

See [SPIKE_REPORT.md](./SPIKE_REPORT.md) for capability matrix +
known issues + P1 recommendations.

## Branch policy

Single `main` branch with PR-sized commits per slice. The spike was
delivered as ~25 commits on main (no side branch). Rationale: the
spike code shared too much surface area with production refactors
in the same period; a side branch would have meant constant rebase.

This means: every commit on main builds, every commit on main keeps
tests green, every commit on main passes lint. **No exceptions.** If
a P1 commit needs to break that, it should be split into smaller
commits that each preserve the invariant.
