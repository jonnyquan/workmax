# WorkMax Desktop — P1 Completion Report

**Date**: 2026-05-20
**Last updated**: 2026-05-21 — bundled static shell/package/runtime smoke evidence
added; packaged smoke now fails raw local-token renderer leaks and non-zero
positive app exits; real previously-authenticated cache smoke with an
unreachable sidecar cloud base still open.
**Status**: P1 core complete for early access; not public-release ready
**Source checkpoint**: [P1_CHECKPOINT.md](./P1_CHECKPOINT.md) v0.5
**Companion plan**: historical plan removed during Server/Desktop consolidation

> Historical snapshot: the open-source repository now ships only Server and
> Desktop. References to the former Web/Admin clients below describe old
> evidence and must not be used as current commands or dependencies.

---

## 1. Executive Decision

**Go** for internal-team use and 1-2 friendly external early-access users.

**No-Go** for public release and P2 expansion until the remaining gates in
§5 are either verified or explicitly waived.

P1 is considered complete in the narrowed product sense documented on
2026-05-20: a mac-first native Desktop client with OAuth login, local
SQLite cache, thread/message pull-sync, delete propagation, slim chat UI,
markdown rendering, active-only thread list, and offline-disabled writes.

The original P1 plan also named full WorkAgent component mounting,
`render_jobs` sync, `thread_files` sync, and offline write queue. Those are
not counted as unfinished blockers here because the current design moved
them out of the P1 critical path:

- full WorkAgent mount is deferred for sequencing and merge-risk reasons;
- `render_jobs` / `thread_files` wait for renderer consumer surfaces;
- offline write queue is rejected for current P1 behavior because chat send
  should not be replayed later from an offline queue.

---

## 2. Requirement Evidence

| Area | Evidence | Status |
|---|---|---|
| OAuth sign-in / account / logout | `/auth/status`, `/auth/userinfo`, `/auth/logout`; Desktop OAuth/cloud-proxy tests + AccountSettings renderer tests | ✅ |
| Sidecar lifecycle + renderer bridge | `desktop/electron/src/main.ts`, `preload.ts`, `sidecar-manager.ts`; Desktop dev workflow. P1 EA does not auto-restart after unexpected exit because preload captures port/token; app restart is required. Electron single-instance lock prevents a second app launch from spawning another sidecar; the sidecar also claims `<dataDir>/sidecar.pid` before opening SQLite, rejects a second live owner, and recovers stale/corrupt pid files. Preload runtime-surface tests pin the exposed bridge members and the missing/invalid-env no-bridge path, including strict decimal port parsing and token whitespace/control rejection. | ✅ |
| App/sidecar version alignment | Electron package version and sidecar default both `0.1.0-p1-ea`; main process fails fast on handshake mismatch | ✅ |
| Packaged renderer URL | Current packaged builds require bundled `Resources/renderer/en/desktop/index.html` and have no hosted fallback. Development-only overrides remain guarded. | ✅ partial |
| Thread pull-sync | `server/desktop/sync/threads_job.go`, `server/desktop/local_history.go`; backend Desktop sweep | ✅ |
| 1000-thread cold-start sync baseline | `BenchmarkThreadsJob_ColdStart1000Threads` with httptest cloud + fresh SQLite | ✅ partial |
| Message sync | `server/desktop/sync/messages_job.go`, `server/desktop/sync/messages_syncer.go`; on-demand + periodic paths | ✅ |
| Delete propagation | Tombstone sidecar writer + cloud tombstone merge + GC sweeper documented in checkpoint | ✅ |
| Active thread list | `/agent/threads?include_paused=false` in sidecar + renderer hook | ✅ |
| Slim chat UI | `desktop/renderer/`; Desktop renderer and Electron suites | ✅ |
| Offline-disabled writes | `ChatComposer` + `OfflineBanner`; renderer tests | ✅ |
| Network-state SSE keepalive | `/system/network-state` emits periodic SSE comments; `TestSSE_HandlerEmitsKeepaliveCommentWhenIdle` | ✅ |
| Large local-cache read baseline | `BenchmarkListLocalThreads_5000Rows`, `BenchmarkListLocalMessages_10000Rows` | ✅ partial |
| Crash-leftover streaming message cleanup | `OpenLocalDB` marks abandoned `streaming_state='streaming'` rows as `partial` during SQLite bootstrap and reports the count; `TestOpenLocalDBMarksAbandonedStreamingMessagesPartial` covers complete/partial rows remaining unchanged | ✅ |
| SQLite daily backup | `OpenLocalDB` creates/reuses `backups/workagent-YYYYMMDD.db` with SQLite `VACUUM INTO`, refreshes the same-day backup via a same-directory temporary file when bootstrap mutates the DB, keeps the newest 7 `workagent-*.db` files, reports `BackupPath`, and logs it at sidecar startup; `TestOpenLocalDBCreatesReadableDailyBackup`, `TestOpenLocalDBMarksAbandonedStreamingMessagesPartial`, and `TestPruneLocalDBBackupsKeepsNewestSeven` cover the behavior | ✅ |
| SQLite integrity detection | `OpenLocalDB` runs `PRAGMA integrity_check` before bootstrap mutation and again before backup; only a single `ok` result is accepted, non-ok details fail startup loudly; `TestOpenLocalDBChecksIntegrityBeforeBootstrapMutation`, `TestOpenLocalDBReportsPostBootstrapIntegrityResult`, and `TestValidateIntegrityCheckResults` cover ordering and clear error summarization | ✅ partial |
| Sidecar diagnostics observability | `/system/diagnostics` exposes Go heap alloc/sys, goroutine count, schema migrations, data/db/backup paths, and SQLite integrity status; DiagnosticsPanel renders and copies those fields for support | ✅ partial |
| Local sidecar smoke helper | `desktop/scripts/smoke-local.sh` checks loopback health/auth-status/diagnostics/thread-list; optional userinfo, server-version, and trigger-sync | ✅ partial |
| Manual smoke checklist | `desktop/SMOKE_CHECKLIST.md` separates local sidecar readiness, manual real-cloud smoke, packaged EA smoke, and public-release gates | ✅ partial |
| Markdown rendering | `DesktopMarkdown` + `ThreadView` | ✅ |
| Full WorkAgent mount | Explicitly deferred; not part of narrowed P1 completion | ⏳ |
| `render_jobs` / `thread_files` sync | Explicitly deferred until file/render UI exists | ⏳ |
| Bundled static renderer | Minimal static shell exists under `desktop/renderer/en/desktop/`, is copied into the `.app`, passes source/package inspection, and has packaged runtime evidence for seeded cached-history reads with `WORKMAX_CLOUD_BASE=http://127.0.0.1:9`; public-release gate remains open until real previously-authenticated cache evidence exists with the same unreachable sidecar cloud base | ✅ partial |
| Windows / Linux | Explicitly out of P1 scope; mac-first | ⏸ |
| App icon / notarization | App icon is now wired into electron-builder from `desktop/build/icons/icon.icns`; notarization still requires Developer ID + Apple notary credentials before public release | ⏸ |

---

## 3. Verification Snapshot

Verified in this P1 closeout pass:

```bash
cd server && GOCACHE=/tmp/workmax-go-build go test -tags desktop ./desktop/... ./api/desktop/... ./service/desktop/... ./router/desktop/... ./middleware/...
cd desktop/electron && npm test
cd server && GOCACHE=/tmp/workmax-go-build go build ./...
cd server && GOCACHE=/tmp/workmax-go-build go build -tags desktop -o /tmp/workmax-workagent-desktop-smoke ./cmd/workagent-desktop
cd server && GOOS=linux GOARCH=amd64 GOCACHE=/tmp/workmax-go-build go test -c -tags desktop ./desktop/cloud_proxy
desktop/scripts/build-mac.sh arm64
desktop/scripts/build-mac.sh x64
```

Results recorded from the current thread:

- Desktop backend sweep passed, including network-state SSE keepalive coverage.
- Desktop renderer suite passed: 20 files / 243 tests, including the
  bridge-fetch invariant that rejects direct browser `fetch` calls, aliases,
  destructuring, and `call`/`bind` bypass forms in Desktop runtime components.
- Electron shell tests passed: 40 tests covering preload bridge runtime-surface
  invariant / route guard / OAuth IPC validation / sidecar-manager lifecycle /
  packaged-smoke diagnostics helper coverage.
  Preload fetch now rejects non-string, empty, whitespace-padded, absolute-URL,
  scheme-relative, fragment-bearing, and control-character paths before token
  injection or network I/O, while preserving sidecar-relative path support.
  Current route guard coverage includes fail-fast rejection for
  `WORKMAX_DESKTOP_RENDERER_URL` values that are not exact `/desktop` routes,
  credential-bearing renderer URLs, encoded/dot-segment route lookalikes, and
  privileged IPC sender URLs outside the configured Desktop route. It also
  covers the bundled file renderer entry and rejects arbitrary sibling file
  paths. External handoff now rejects local/private HTTP(S) targets instead of
  opening loopback callback or LAN URLs in the OS browser. OAuth loopback
  callback detection now rejects credential-bearing or fragment-bearing
  callback lookalikes before they can close the OAuth window. The Go loopback
  callback parser now rejects duplicate callback parameters and callbacks that
  contain both `code` and `error` as `invalid_request`, so ambiguous redirects
  cannot select the first value and proceed to token exchange. Denied or
  invalid callbacks render a "sign-in not completed" loopback page instead of
  the success page. OAuthFlow now validates callback `state` before accepting
  success or error callbacks, so local forged error callbacks cannot bypass the
  CSRF nonce and surface as trusted vendor errors.
  Bundled-renderer source checks now parse and pin the exact static-shell CSP,
  verify static asset references, and exercise the static shell's
  missing-bridge, authenticated cache-read, unauthenticated OAuth-start, and
  malformed `/auth/status` / `/auth/start` / cached-thread payload paths.
  Sidecar `/auth/start` now also validates the OAuth start result before
  returning it to the renderer, requiring an HTTP(S) authorize URL at
  `/api/desktop/oauth/authorize`, no credentials or fragment, a 1..65535
  `auth_port`, and exactly one loopback `redirect_uri` matching
  `http://127.0.0.1:<auth_port>/oauth/callback` with no query/fragment;
  malformed results cancel the pending flow and return 502. Renderer and
  Electron main-process validators now enforce the same unambiguous redirect
  rule before invoking or opening the OAuth BrowserWindow. It also rejects
  duplicate, empty, malformed, or non-`workagent` request `scope` form values
  with 400 before starting OAuth.
  Current main-log coverage verifies token-like strings and HTTP(S) URL
  credentials are redacted across structured Electron main-process log fields,
  including dynamic object keys, sensitive-key values, bare bearer-token
  fragments, Basic auth, `client_secret`, password, compact `apikey`, and
  API-key shaped fields. Sidecar stderr is redacted before both dev-terminal
  output and `sidecar-main.log`, including `X-Local-Token:` and
  `X-Local-Token=` forms. SidecarManager also redacts the malformed-handshake
  first-line excerpt before surfacing startup errors, so boot failures do not
  depend solely on the outer main logger for token scrubbing.
  Current smoke-diagnostics coverage verifies the packaged smoke reporter maps
  `/system/diagnostics` into the assertion shape used by
  `smoke-packaged-app.sh`, including the local token header, readable SQLite
  data/db/backup paths, `integrity_check`, filtered non-empty migration IDs,
  heap/goroutine counters, non-JSON diagnostics failure preservation, token
  fingerprints that do not reveal the raw local token, and recursive redaction
  of any raw local-token occurrence in values or object keys before a smoke
  artifact can be written.
  Sidecar diagnostics endpoint coverage also verifies sync-worker `last_error`
  is redacted before it is rendered or copied into support payloads.
  Renderer-log coverage verifies the browser helper redacts token-like strings
  and URL credentials, including Basic/Bearer auth, `client_secret`, password,
  generic `token=...`, compact `apikey`, and dynamic object keys, before POST /
  console fallback; the sidecar redacts the same classes of values and dynamic
  object keys again before appending `renderer.log`. Local request-size guards reject oversized
  `/agent/chat` bodies at 1 MiB before cloud proxying and oversized
  `/system/log` bodies at 64 KiB before disk writes.
  Cloud-proxy `proxy_error` coverage now verifies upstream error messages,
  body-prefix details, nested details, and returned Go error strings redact
  bearer/basic auth, URL credentials, token/API-key query pairs,
  `client_secret`, `password`, `secret`, sensitive detail keys, and
  `X-Local-Token` before anything reaches the renderer.
  The shared cloud-proxy body-prefix helper now also redacts JSON token fields
  and is used by OAuth token exchange / revoke and other Desktop cloud-client
  errors before those errors can reach sync `last_error`, diagnostics, or logs.
- Production Go build without the `desktop` tag passed, proving Desktop-only
  imports still do not leak into normal cloud server builds.
- Desktop sidecar binary build with `-tags desktop` passed when writing the
  output to `/tmp`, avoiding generated binaries in the repo tree.
- The sidecar now supports `--version`; dev and mac packaging scripts verify
  the built binary version before launch/package handoff.
- The sidecar now has a data-dir PID lock at `<dataDir>/sidecar.pid`; unit
  coverage verifies lock creation/removal, live-owner rejection, stale-owner
  recovery, corrupt-lock recovery, and not removing a lock whose owner changed.
- `desktop/scripts/inspect-mac-package.sh` now provides a repeatable
  post-build `.app` bundle inspection for bundle id, app version, main
  executable, Electron app payload, packaged sidecar location, executable bit,
  sidecar version marker, packaged Electron payload contents, and
  signing/Gatekeeper status reporting. The payload check requires exactly one
  payload mode (`app.asar` or `Resources/app`), verifies the package
  name/main/version and compiled runtime entry points, and rejects any
  unexpected payload entry. It also rejects unexpected top-level
  `Contents/Resources` entries so extraResources drift cannot smuggle files
  around the Electron payload allowlist. Known-bad examples include compiled
  tests, source maps, source files, nested `.app` bundles, duplicated sidecar
  binaries, debug resources, and stale packaged build output such as `release/`
  or `dist/mac*` artifacts in both asar and unpacked modes.
- `desktop/scripts/check-bundled-renderer.test.sh` backs the source bundled
  renderer preflight with disposable renderer fixtures, covering missing files,
  missing or unsafe CSP, non-relative CSS/JS references, and unexpected or
  token-like embedded files before `build-mac.sh` invokes electron-builder.
- `desktop/scripts/inspect-mac-package.test.sh` backs the inspector with local
  fake-bundle regression coverage for valid unpacked and `app.asar` payloads
  plus the core negative invariants: ambiguous payload modes, sidecar
  duplication inside either Electron payload mode, package main drift, and
  missing or extra runtime entries, including unexpected top-level
  `Contents/Resources` entries. It also covers the public-release
  `--require-bundled-renderer` mode, which requires
  `Contents/Resources/renderer/en/desktop/index.html`, `styles.css`, and
  `renderer.js` to exist and be non-empty, and rejects exact CSP drift,
  including non-loopback or extra remote `connect-src` entries, plus unexpected
  or token-like files in the packaged renderer. It also covers the
  `--require-app-icon` mode, which rejects the default
  Electron icon and requires `CFBundleIconFile=icon.icns` with a packaged
  `Contents/Resources/icon.icns` whose file header is `icns`, and the
  `--require-developer-id-signature` mode, which rejects ad-hoc signatures,
  non-Developer-ID authority chains, missing TeamIdentifier, missing hardened
  runtime metadata, and strict verification failures while accepting a
  Developer ID Application signature fixture.
- `desktop/scripts/notarize-mac.test.sh` backs the notarization helper with
  local fake-DMG validation coverage for unsupported targets, missing artifacts,
  arbitrary explicit-DMG app-inspection bypass rejection, versioned release-DMG
  app-bundle inference, missing credentials, and both supported dry-run
  credential modes through the explicit
  `--allow-hosted-renderer` early-access escape hatch. The test now also builds
  a fake neighboring release `.app` and stubs `codesign`, proving the helper
  rejects ad-hoc signatures, signatures without TeamIdentifier,
  non-Developer-ID authority chains, missing hardened runtime metadata, and
  strict verification failures before Apple submission; a Developer ID
  Application signature can pass dry-run validation.
- `desktop/scripts/build-mac.sh` now invokes that inspector on the generated
  `.app` for the requested arch before reporting packaging success.
- `desktop/scripts/build-mac.sh --preflight-only <arch>` validates static
  packaging inputs without compiling or invoking electron-builder. The matching
  `desktop/scripts/build-mac.test.sh` covers wrapper argument validation,
  current packaging input readiness, and electron-builder config drift for
  output directory, hardened runtime, runtime entry allowlist, bundled renderer
  resources, `publish: null`, `extraFiles` / `afterSign` / inline
  `mac.notarize` side-effect hooks, and exact entitlement contents.
- `desktop/scripts/build-mac.sh --public-release <arch>` is the stricter
  wrapper mode for public-release candidates. It keeps the sidecar version,
  package structure, and artifact checks from the early-access wrapper, then
  requires the bundled renderer, custom app icon, and a `codesign -dv`
  `Authority=Developer ID Application: ...` hardened-runtime signature through
  the inspector before reporting success.
  `desktop/electron/package.json` exposes matching
  `dist:mac:public`, `dist:mac:public:arm64`, and `dist:mac:public:x64`
  wrappers.
- Real arm64 and x64 electron-builder package passes now complete after the
  app.asar contamination fix and automatically inspect the requested-arch app
  bundle. A prior unsigned arm64 package pass on 2026-05-21 proved the
  bundled-renderer and app-icon structural gates against
  `desktop/electron/release/mac-arm64/WorkMax Desktop.app`, but that pass is no
  longer public-release evidence because `--public-release` now also requires
  a Developer ID Application hardened-runtime signature. The generated `.app`,
  `.dmg`, `.zip`, and blockmap artifacts are ignored by `desktop/.gitignore`.
  Current artifact sizes are normal for this Electron app: fresh arm64 is about
  112 MB DMG / 108 MB ZIP, and the previous x64 pass is about 117 MB DMG /
  113 MB ZIP. Both passes use the local `electron-builder@25.1.8` from
  `desktop/electron/package-lock.json`.
- Packaged runtime bundled-renderer smoke now exists:
  `desktop/scripts/smoke-packaged-app.sh --timeout 45
  "desktop/electron/release/mac-arm64/WorkMax Desktop.app"` launches an already
  built macOS `.app` with a temporary data directory and an explicit one-shot
  Electron reporter. The 2026-05-21 arm64 run passed and observed
  `app.isPackaged=true`, the exact bundled
  `file://.../Contents/Resources/renderer/en/desktop/index.html` loaded URL,
  `window.workmaxLocal` exposure, matching app/sidecar versions, and a nonblank
  renderer body. The same positive packaged smoke now performs raw loopback
  `/health` requests while the packaged sidecar is alive and verifies missing,
  wrong, and duplicate local-token headers all return 403. It also verifies
  missing/wrong local-token rejection across representative packaged sidecar
  surfaces: `/auth/status`, `/system/diagnostics`,
  `/agent/threads?include_paused=false`, `/system/log`, and
  `/system/trigger-sync`, without writing the actual token into the smoke
  result. It also calls `/system/diagnostics` and asserts the packaged sidecar
  reports readable SQLite data/db/backup paths, `integrity_check: "ok"`,
  non-empty applied SQLite migrations, and Go heap/goroutine counters. This is
  runtime evidence that the packaged app chooses the bundled file renderer and
  that the live packaged sidecar enforces local-token rejection and diagnostics
  observability. The same helper's
  `--check-token-rotation` mode launches the packaged app twice against one
  temporary data directory and asserts the SHA-256 local-token fingerprint
  changes across sidecar spawns; the 2026-05-21 arm64 run passed. Its
  `--check-cached-history` mode uses a smoke-only in-memory token store, seeds
  one local thread/message in the temporary data directory, forces
  `WORKMAX_CLOUD_BASE=http://127.0.0.1:9`, and verifies the bundled renderer
  displays both without a live cloud dependency. These are still not a full
  real-network-off, previously-authenticated-cache, or signing pass.
  A new real-cache mode accepts `--data-dir <existing-dir>` plus
  `--expect-thread-text` / `--expect-message-text`, launches against that
  existing Desktop cache without seeding rows, forces
  `WORKMAX_CLOUD_BASE=http://127.0.0.1:9`, and lets the public-release offline
  gate assert known cached content while the user's normal Keychain-backed
  session remains the auth source. The helper also accepts
  `--expect-body-text`, which is now documented for the clean-data-dir,
  unauthenticated/no-cache packaged state after clearing the Desktop Keychain
  session.
  The reporter also redacts and flags raw local-token occurrences plus broader
  token-like renderer text in renderer-observation fields before writing the
  JSON artifact, including compact `apikey`, generic `secret`, URL credentials,
  bearer/basic auth, and sensitive object keys. The shell helper requires leak
  flags to be explicitly false and redaction markers to remain absent for
  positive smoke.
- `desktop/scripts/smoke-packaged-app.test.sh` now covers packaged-smoke
  prelaunch validation paths without launching Electron: missing app path,
  missing values for value-taking options, invalid timeout, negative-mode option
  pairing, incompatible cached-history / token-rotation modes, malformed app
  bundle, and missing `Info.plist` / `CFBundleExecutable`. It also stubs
  `codesign` against a fake `.app` so the
  controlled-smoke signature behavior is covered: valid signatures pass strict
  verification, ad-hoc signatures are repaired locally, and other signature
  verification failures remain hard failures. A positive-mode fixture also
  writes a valid-looking renderer smoke result and exits non-zero, proving the
  helper treats the process exit status as authoritative and cannot mask a
  crash behind a stale/plausible JSON result. The same fixture now covers
  shell-level rejection for `smokeLocalTokenLeakDetected=true`,
  `smokeSensitiveLeakDetected=true`, missing `smokeSensitiveLeakDetected`, and
  local-token / sensitive redaction markers in the would-be successful smoke
  result. It also rejects malformed local-token rejection evidence instead of
  accepting duplicate or unexpected proof rows. It also covers the
  real-cache option preflights, including rejection of `--data-dir` without an
  offline text assertion, successful cached text assertions, and a successful
  body-text assertion fixture for the unauthenticated/no-cache path.
- Packaged route-negative runtime smoke now exists in the same helper:
  `--renderer-url <url> --expect-failure <text>` launches the built `.app` with
  a bad `WORKMAX_DESKTOP_RENDERER_URL` and asserts startup rejection before any
  renderer smoke result is written. The 2026-05-21 arm64 runs proved
  `https://workmax.app/` fails with "desktop renderer URL must point to a
  /desktop route" and `file:///tmp/not-workmax/index.html` fails with "desktop
  renderer URL must use http, https, or the bundled file renderer entry".
- Signing credentials remain unresolved. The notarization workflow is now
  scripted in `desktop/scripts/notarize-mac.sh`: arch-based public distribution
  runs package inspection with the bundled-renderer and app-icon requirements,
  rejects unsigned/ad-hoc bundles, requires `TeamIdentifier`,
  `Authority=Developer ID Application: ...`, and hardened runtime signature
  metadata, submits the DMG with `xcrun notarytool`, staples the ticket, and
  verifies the stapled DMG. Non-dry-run submission requires an arch target or
  versioned release DMG that maps to an inspectable neighboring `.app`, and
  `--allow-hosted-renderer` is rejected outside dry-run because public
  notarization requires the bundled renderer gate; arbitrary explicit DMG paths
  remain dry-run only. Inspector evidence from the current arm64
  artifact: signing status is `adhoc`, strict
  `codesign --verify --deep --strict` fails, and Gatekeeper assessment fails.
  Inspector evidence from the current x64 artifact: signing status is
  `unsigned-or-unreadable`, strict `codesign --verify --deep --strict` fails,
  and Gatekeeper assessment rejects the app. This is acceptable only for
  controlled early-access smoke.
- Linux cross-compile of the keychain stub passed.
- Local-cache read benchmark baseline on the current machine, using
  production-equivalent desktop SQLite indexes:
  - `BenchmarkListLocalThreads_5000Rows`: ~7.2 ms/op reading 1000 rows from 5000 cached threads.
  - `BenchmarkListLocalMessages_10000Rows`: ~3.03 ms/op reading 1000 rows from one 10000-message thread.
- Local cache privacy coverage verifies Desktop list/read handlers filter
  thread rows and message rows by the uid embedded in the active local token,
  including mismatched-uid message rows attached to an otherwise visible
  thread, and return no cached rows when TokenStore is configured but no valid
  session exists or the refresh chain is expired. Auth status no longer reports
  `authenticated` for malformed local session entries with an empty access
  token, and `AcquireAccessToken` refreshes that case instead of returning an
  unusable bearer. Thread/message sync writers now also reject upsert UUID
  conflicts when the existing local row belongs to a different uid, preserving
  the other account's cache row and failing the sync page before cursor
  advancement. Thread/message sync jobs also fail loudly when cloud pagination
  returns `has_more=true` without a `next_cursor`, leaving the cursor unchanged
  so the page is retried rather than silently treating the tick as complete.
- Chat relay coverage verifies that the upstream 401 auto-refresh path still
  writes the successfully retried SSE stream through the original cache writer,
  preserving offline history instead of only forwarding recovered frames to the
  renderer.
- Version discovery now sanitizes optional release-notes links before they
  reach the Desktop banner: only absolute `http`/`https` URLs without
  credentials or token/secret-like query keys are rendered, with both sidecar
  client and renderer tests pinning the behavior.
- DesktopMarkdown now sanitizes model/cloud-provided markdown links before
  rendering anchors: unsafe schemes, relative URLs, and credential-bearing URLs
  render as plain text, while safe HTTP(S) links keep `target="_blank"` with
  `rel="noopener noreferrer"`. Markdown images are downgraded to inert text so
  assistant output cannot auto-load remote tracking pixels or data URLs.
- Network-state hook coverage now rejects malformed sidecar SSE payloads
  (invalid JSON, unknown `state`, or missing timestamp fields) as an error
  banner path instead of treating them as ready network data; a malformed
  `network_state` frame is fatal for that stream so a later valid frame cannot
  overwrite the protocol error.
- Auth-status and server-version renderer hooks now reject malformed sidecar
  JSON payloads before committing ready state, so unknown auth states, wrong
  field types, or missing version fields cannot drive login/update UI. Shared
  Desktop error redaction is applied to hook-level loopback failures before
  those messages reach auth, update, diagnostics, network, login, or local
  history UI.
- Account Settings now validates `/auth/userinfo` before display, including
  string account fields, finite non-negative quota counters, and safe optional
  HTTP(S) avatar URLs. It also redacts token/secret-like text, Basic auth, and
  URL credentials from userinfo/logout errors before rendering account panel
  messages.
- LoginPage now validates `/auth/start` authorize URL shape and loopback
  callback port before invoking Electron `openOAuthWindow`, complementing the
  main-process origin-aware OAuth validator. It also rejects malformed
  `openOAuthWindow` IPC results instead of treating unknown bridge output as a
  successful window lifecycle.
- Chat stream hook coverage now validates terminal `proxy_error` messages at
  runtime and redacts token/secret-like strings, Basic auth, and URL
  credentials from proxy, HTTP, and fetch error text before those messages
  reach composer state. It also treats a stream close without explicit `done`
  or `proxy_error` as an error while preserving partial text, so truncated
  answers are not silently marked complete.
- ChatComposer now mirrors the Desktop allowlist before sending: empty
  `chatMode` normalizes to `ppt`, and non-allowlisted modes disable input/send
  before any `/agent/chat` request is built.
- `/agent/chat` handler coverage now rejects blank `user_text` before local
  cache or upstream relay work.
- Skills catalog coverage now drops malformed allowed items before they reach a
  future picker surface and redacts token-like strings from cloud catalog
  failure messages.
- Diagnostics hook coverage now rejects malformed support snapshots before the
  panel renders/copies them, while still accepting optional newer sidecar
  fields when their types are valid.
- DiagnosticsPanel "Open logs" now treats rejected or malformed
  `revealDataDir` IPC results as a visible `Open failed` state instead of
  throwing from the click handler.
- Local-history renderer hooks now validate `/agent/threads` and
  `/agent/threads/:uuid/messages` response arrays before committing ready
  state; malformed arrays, row shapes, or empty/whitespace/control/oversize
  cached UUIDs, non-finite/negative counters, and unparsable timestamps enter
  error/retry instead of reaching ThreadList/ThreadView render paths or
  follow-up sidecar path construction as invalid offline cache data. Fetch
  rejection messages from the local-history hooks are redacted before rendering
  so loopback/bridge errors cannot leak token-like text. The bundled static
  renderer applies the same cached thread/message row guard for packaged
  fallback cache reads and redacts token/secret-like status errors before
  display, and Diagnostics Force sync validates the current thread UUID before
  adding `?thread=...`.
- Local-history sidecar handlers now reject malformed `limit` and
  `include_paused` query values, plus malformed message thread UUID path
  values, before local reads or background messages-sync triggers, instead of
  silently falling back to defaults.
- Cold-start thread sync benchmark baseline on the current machine:
  - `BenchmarkThreadsJob_ColdStart1000Threads`: ~1.09 s/op syncing 1000 threads via httptest cloud into a fresh SQLite file.
- Sidecar diagnostics now expose Go runtime memory trend fields:
  - `heap_alloc_bytes`
  - `heap_sys_bytes`
  - `num_goroutine`
  - DiagnosticsPanel now renders the data directory, SQLite db path, daily
    backup path, integrity status, and applied migration list directly in the
    table, and includes the same snapshot plus computed log/backup paths in the
    copy-to-clipboard support payload.
  - `/system/diagnostics` redacts token-like strings and URL credentials from
    sync-worker `last_error`, and DiagnosticsPanel defensively re-sanitizes the
    snapshot before rendering or copying support payloads, including
    bearer/basic auth, URL credentials, `*_token`, `api_key` / `apikey`,
    `client_secret`, `password`, `secret`, and `X-Local-Token`.
  - `desktop/scripts/smoke-local.sh` now asserts these fields are numeric and
    that diagnostics reports readable SQLite data/db/backup paths, non-empty
    applied migrations, and `integrity_check: "ok"`.
- Local sidecar smoke helper:
  - Local-token middleware unit coverage rejects missing, wrong, and duplicate
    `X-Local-Token` headers before comparing fixed-length token digests.
  - `WORKMAX_LOCAL_TOKEN=<token> ./desktop/scripts/smoke-local.sh --port <port>`
  - add `--expect-version <version>` to assert the diagnostics sidecar version.
  - add `--with-userinfo` after OAuth sign-in to include `/auth/userinfo`.
  - add `--with-server-version` when cloud client/network access is expected.
  - add `--check-token-rejection` to verify missing/wrong local-token rejection
    across representative loopback read/write surfaces, including `/health`,
    `/auth/status`, `/system/diagnostics`, `/agent/threads`, `/system/log`,
    and `/system/trigger-sync`; the same smoke also checks duplicate
    `X-Local-Token` headers on `/health`.
  - add `--check-pid-lock --sidecar-binary <path>` to verify a second
    standalone sidecar launch is rejected for the same diagnostics data dir.
  - add `--trigger-sync` to include the manual sync endpoint after OAuth sign-in;
    unauthenticated / expired sessions return 401 by design.
    The endpoint now rejects empty, malformed, or duplicate `thread` query
    values before triggering sync work, while preserving the documented silent
    skip for unknown local threads.
  - add `--diagnostics-samples <n> --diagnostics-interval <seconds>` during
    sustained chat to capture heap/goroutine trend snapshots; every captured
    diagnostics sample is now schema-checked for SQLite paths, integrity,
    migrations, and Go runtime counters.
  - `desktop/scripts/smoke-local.test.sh` covers preflight validation paths
    that must fail before any token-bearing curl request, including unsafe
    remote, credential-bearing, path, query, and fragment `--base` values. It
    also stubs curl to prove later diagnostics samples are validated, not just
    fetched and printed.
  - Verified with `bash -n`, `--help`, invalid-argument checks, executable-bit
    check, and a live unauthenticated sidecar run against `/health`,
    `/auth/status`, `/system/diagnostics`, and
    `/agent/threads?include_paused=false`. The live smoke confirmed the
    handshake and diagnostics sidecar version as `0.1.0-p1-ea`; the same
    check was repeated against a sidecar binary built with the Electron
    package version injected via `-ldflags`.
  - The local sidecar smoke was also repeated with `--check-pid-lock`, which
    launched a second standalone sidecar against the same diagnostics
    `data_dir` and verified startup failed with the expected live-owner lock
    message before opening SQLite.

---

## 4. Deliberate Scope Changes

### 4.1 Full WorkAgent Mount

The original P1.B.5 plan was to mount the former browser WorkAgent directly
inside Desktop. P1 instead shipped a slim Desktop-specific shell using the
stable sidecar endpoints already available.

Reason: the full WorkAgent surface is large, actively changing, and still
has features that the Desktop sidecar does not proxy. The slim shell closes
the important user path without coupling P1 to unrelated Web WorkAgent
refactors.

### 4.2 File and Render Sync

`thread_files` and `render_jobs` remain in the sync design, but no current
Desktop renderer view consumes them. Implementing the endpoints now would
add unverified contract surface without user-visible value.

Trigger to resume: file attachment/list UI or render status UI lands in
Desktop.

### 4.3 Offline Write Queue

Offline writes are intentionally not supported. The current Desktop write
surface is chat send, and queueing a chat to replay later would create
delayed AI turns with unclear context. Offline mode is read-only.

Trigger to revisit: rename/delete/settings/rating UI lands and product
explicitly asks for offline editing.

---

## 5. Remaining Gates Before Public Release or P2

These are not blockers for friendly early access, but they are blockers for
public release and for broad P2 investment:

1. Measure sidecar memory under sustained chat and run a real-cloud
   1000-thread cold-start smoke. Local 1000-thread sync and large-cache
   SQLite read benchmarks now exist, and diagnostics can track Go heap /
   goroutine trends; still run this on release hardware with real network
   timing before public release.
2. Launch the packaged arm64 `.app` and run a real manual smoke test against
   current workmax.app: sign in,
   load threads, open a cold thread, send chat, restart sidecar, sign out.
   The local unauthenticated sidecar half has now been scripted and smoke-tested
   with `desktop/scripts/smoke-local.sh`; `desktop/SMOKE_CHECKLIST.md` now
   defines the manual real-cloud sequence, but OAuth and chat still require an
   authenticated cloud environment.
3. Decide whether the missing full WorkAgent features are required for the
   target early-access cohort: file upload, skill picker, thread settings,
   rating UI, and structured tool-use rendering.
4. Run real offline packaged renderer smoke before public release.
   The hermetic seeded-cache packaged smoke now proves the bundled renderer can
   display cached history from the local sidecar, but a previously
   authenticated cache still needs manual evidence using the helper-enforced
   unreachable cloud base. The executable path is now documented and partially
   automated with
   `desktop/scripts/smoke-packaged-app.sh --data-dir <existing-dir>
   --expect-thread-text <known-title> --expect-message-text <known-message>`.
   The no-cache half is also executable with `--data-dir <clean-dir>
   --expect-body-text "Auth state: unauthenticated"` after clearing the Desktop
   Keychain session.
   The concrete gate is documented in `desktop/README.md` and enforced by the
   packaged-smoke helper tests.
5. Provide a Developer ID Application certificate and notarization credentials
   before distribution beyond controlled testers. The app icon is now wired into
   `electron-builder.yml`, hardened runtime is pinned in the helper's local
   signature gate, and the post-build notarization helper exists, but it cannot
   clear the distribution gate without a signed artifact and Apple notary
   credentials.
6. Keep Windows/Linux deferred unless product explicitly commits to
   non-macOS Desktop targets.

---

## 6. P2 Recommendation

Do not start marketplace, Windows/Linux, or bidirectional sync work yet.

The next practical slice should be one of:

- performance measurement for the P2 gate;
- a current-cloud manual smoke run and bug list using
  `desktop/SMOKE_CHECKLIST.md`;
- full WorkAgent mount feasibility spike after Web WorkAgent parallel edits
  settle;
- file/render UI design, if product wants those sync endpoints unblocked.

---

*Document version: v1.2 — 2026-05-21*
