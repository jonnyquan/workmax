# WorkMax Desktop Smoke Checklist

Use this checklist when validating a Desktop build against the current code.
It separates local sidecar readiness from authenticated cloud behavior and
public-release packaging gates. The supported product boundary is Go Server
plus Desktop only; no check may rely on or recreate an independent Web/Admin
client.

## 1. Local Sidecar Readiness

Prerequisite: start the sidecar via Electron (`./desktop/scripts/dev.sh`) or by
running `server/cmd/workagent-desktop` with a known `WORKMAX_LOCAL_TOKEN`.

```bash
WORKMAX_LOCAL_TOKEN=<token> ./desktop/scripts/smoke-local.sh --port <port>
WORKMAX_LOCAL_TOKEN=<token> ./desktop/scripts/smoke-local.sh --port <port> --expect-version 0.1.0-p1-ea
WORKMAX_LOCAL_TOKEN=<token> ./desktop/scripts/smoke-local.sh --port <port> --check-token-rejection
WORKMAX_LOCAL_TOKEN=<token> ./desktop/scripts/smoke-local.sh --port <port> --check-pid-lock --sidecar-binary desktop/electron/bin/workagent-desktop
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
  including electron-builder config drift for hardened runtime, runtime entry
  allowlist, bundled renderer resources, and side-effect hooks.
- `desktop/scripts/check-bundled-renderer.test.sh` covers bundled-renderer
  source preflight failures before packaging: missing files, missing or unsafe
  CSP, extra remote CSP connect sources, non-relative CSS/JS references, and
  unexpected or token-like embedded files.

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
Electron Main-only IPC and bundled password UI without reading
`server/config.yaml` or connecting to an external database:

```bash
cd server
go test ./service/desktop/logintransaction ./service/identity ./service/secrets ./api/desktop/login ./service/desktop/oauth ./router/desktop ./initialize ./initialize/internal ./core ./migrations
go test -tags desktop ./desktop/... ./cmd/workagent-desktop
cd ../desktop/electron && npm test && cd ../..
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
credentials enter the Preload/Renderer response contract. Electron and Renderer
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
Sidecar coordinator and four local privileged routes to Electron Main-only IPC
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

- sidecar version and Electron version
- `WORKMAX_CLOUD_BASE`
- data directory path
- `/system/diagnostics` SQLite db path, daily backup path, integrity status,
  applied migrations, and any heap/goroutine trend during the chat turn
- failures with `logs/sidecar-main.log` and `logs/renderer.log`

## 3. Packaged Early-Access Smoke

Build through the wrapper, not `electron-builder` directly. The wrapper inspects
the generated `.app` for the requested arch before it reports success:

```bash
./desktop/scripts/build-mac.sh arm64
./desktop/scripts/build-mac.sh x64
./desktop/scripts/build-mac.sh --preflight-only arm64
```

For a public-release candidate, build through the stricter wrapper mode so the
same package pass also verifies the bundled renderer, custom icon, and
Developer ID Application hardened-runtime signature gates:

```bash
./desktop/scripts/build-mac.sh --public-release arm64
cd desktop/electron && npm run dist:mac:public:x64
```

For a public-distribution candidate, notarize only after the app is signed with
a Developer ID Application certificate:

```bash
WORKMAX_NOTARY_KEYCHAIN_PROFILE=workmax-notary ./desktop/scripts/notarize-mac.sh arm64
./desktop/scripts/notarize-mac.sh --dry-run x64
```

If you are validating an already-built or extracted `.app`, inspect it before
launch:

```bash
cd desktop/electron && npm install   # only needed if node_modules is missing
cd ../..
./desktop/scripts/inspect-mac-package.sh "desktop/electron/release/mac-arm64/WorkMax Desktop.app"

# Public-release candidates only; verifies the bundled renderer, icon assets,
# and Developer ID Application hardened-runtime signature made it into the packaged .app.
./desktop/scripts/inspect-mac-package.sh --require-bundled-renderer --require-app-icon --require-developer-id-signature "desktop/electron/release/mac-arm64/WorkMax Desktop.app"

# Runtime bundled-renderer smoke for already-built arm64 app.
./desktop/scripts/smoke-packaged-app.sh --timeout 45 "desktop/electron/release/mac-arm64/WorkMax Desktop.app"
./desktop/scripts/smoke-packaged-app.sh --timeout 45 --check-cached-history "desktop/electron/release/mac-arm64/WorkMax Desktop.app"
./desktop/scripts/smoke-packaged-app.sh --timeout 45 --check-token-rotation "desktop/electron/release/mac-arm64/WorkMax Desktop.app"

# Real offline-cache public-release gate, after an online authenticated run has
# populated the listed data dir. The helper forces an unreachable cloud base.
./desktop/scripts/smoke-packaged-app.sh --timeout 60 \
  --data-dir "$HOME/.workmax" \
  --expect-thread-text "Known cached thread title" \
  --expect-message-text "Known cached message text" \
  "desktop/electron/release/mac-arm64/WorkMax Desktop.app"

# Unauthenticated/no-cache public-release gate. Use a clean data dir after
# clearing/revoking the Desktop Keychain session. The helper forces the sidecar
# cloud base to an unreachable loopback origin for this assertion.
mkdir -p /tmp/workmax-empty-desktop-data
./desktop/scripts/smoke-packaged-app.sh --timeout 60 \
  --data-dir "/tmp/workmax-empty-desktop-data" \
  --expect-body-text "Auth state: unauthenticated" \
  "desktop/electron/release/mac-arm64/WorkMax Desktop.app"

# Packaged route-negative smoke: both must fail before a bridged window opens.
./desktop/scripts/smoke-packaged-app.sh --timeout 15 \
  --renderer-url https://workmax.app/ \
  --expect-failure "desktop renderer URL must point to a /desktop route" \
  "desktop/electron/release/mac-arm64/WorkMax Desktop.app"
./desktop/scripts/smoke-packaged-app.sh --timeout 15 \
  --renderer-url file:///tmp/not-workmax/index.html \
  --expect-failure "desktop renderer URL must use http, https, or the bundled file renderer entry" \
  "desktop/electron/release/mac-arm64/WorkMax Desktop.app"
```

Expected:

- The packaged app starts the sidecar from `Resources/workagent-desktop`.
- The app/sidecar versions match; mismatch fails fast.
- A second standalone sidecar launch against the same `WORKMAX_DESKTOP_DATA_DIR`
  fails before opening SQLite with an "another sidecar instance" message;
  stale or corrupt `sidecar.pid` files are recovered on startup.
- `desktop/scripts/build-mac.sh` verifies the built sidecar binary contains
  the Electron package version before electron-builder packages it, then runs
  `desktop/scripts/inspect-mac-package.sh` on the requested-arch app bundle and
  fails if the expected `.dmg` or `.zip` artifact is missing or empty.
- `desktop/scripts/build-mac.sh --preflight-only <arch>` validates the static
  packaging inputs without compiling or invoking electron-builder. It checks the
  electron-builder config values that affect distribution shape (`appId`,
  `productName`, runtime entry allowlist, sidecar/renderer `extraResources`,
  output directory, DMG/ZIP targets, artifact name, `publish: null`, hardened
  runtime, entitlements, icon path, and absence of `extraFiles`, `afterSign`,
  or inline `mac.notarize` side-effect hooks), then checks entitlements existence,
  bundled renderer source/behavior, exact minimal entitlement contents, and icon
  file/header. It is a fast local
  sanity check, not a release artifact.
- `desktop/scripts/build-mac.sh --public-release <arch>` is the public-release
  build wrapper. It preserves the same sidecar/version/artifact checks and
  adds the bundled-renderer, app-icon, and Developer ID Application signature
  package inspection gates before reporting success. Any prior unsigned local
  wrapper pass is structural evidence only, not public-release evidence.
- Current arm64 and x64 package passes complete structurally on the local build
  host, but both remain unsigned / Gatekeeper-rejected without Developer ID
  signing and notarization.
- `desktop/scripts/notarize-mac.sh` is the post-build notarization gate for
  public distribution. For arch-based builds it re-runs package inspection with
  the bundled-renderer and app-icon requirements, rejects unsigned/ad-hoc
  bundles, requires `TeamIdentifier`, `Authority=Developer ID Application: ...`,
  and hardened runtime signature metadata, runs strict
  `codesign --verify --deep` on the `.app`, submits the DMG with
  `xcrun notarytool`, staples the ticket, and verifies the stapled DMG.
  `--dry-run` validates paths, bundled-renderer/icon
  readiness, signing state, and credential environment without contacting
  Apple. `--allow-hosted-renderer` bypasses only the bundled-renderer
  requirement for controlled early-access dry-runs; the icon gate still
  applies, and Apple submission with that escape hatch is rejected. If a
  versioned release DMG path is passed explicitly, the helper still infers and
  inspects the neighboring `release/mac*` app bundle before dry-run or
  submission. Arbitrary DMG paths that cannot be mapped to a neighboring `.app`
  are dry-run only, even with `--allow-hosted-renderer`.
- `desktop/scripts/inspect-mac-package.sh` verifies the app bundle id, app
  version, main executable, Electron app payload, packaged sidecar location,
  executable bit, sidecar version marker, and packaged Electron payload
  contents. The payload check requires exactly one payload mode (`app.asar` or
  `Resources/app`), verifies the package name/main/version and compiled runtime
  entry points, and rejects any unexpected payload entry. Known-bad examples
  include compiled tests, source maps, source files, nested `.app` bundles,
  duplicated sidecar binaries, and stale packaged build output such as
  `release/` or `dist/mac*` artifacts in both asar and unpacked modes. It also
  rejects unexpected `app.asar.unpacked` content and unexpected top-level
  `Contents/Resources` entries, then reports signing, strict codesign
  verification, and Gatekeeper assessment status; unsigned/ad-hoc output is
  acceptable only for controlled early-access smoke.
- `desktop/scripts/inspect-mac-package.sh --require-bundled-renderer --require-app-icon --require-developer-id-signature`
  is the manual public-release package-inspection mode for already-built apps.
  `desktop/scripts/build-mac.sh --public-release <arch>` invokes it
  automatically during packaging. It keeps all structural checks above and
  additionally requires
  `Contents/Resources/renderer/en/desktop/index.html`,
  `styles.css`, `renderer.js`, and `Contents/Resources/icon.icns` to exist and
  be non-empty. It parses the packaged renderer CSP and requires the exact
  static-shell directives, including loopback-only
  `connect-src http://127.0.0.1:*`; it also verifies relative asset references
  and absence of unexpected or token-like embedded files, and requires the icon
  file to have an `icns` header and requires `CFBundleIconFile=icon.icns`. It
  also fails ad-hoc signatures, non-Developer-ID authority chains, missing
  TeamIdentifier, missing hardened runtime metadata, and strict
  `codesign --verify --deep --strict` failures. Passing this public-release
  gate requires `codesign -dv` to report
  `Authority=Developer ID Application: ...`.
  The source bundled renderer is checked by
  `desktop/scripts/check-bundled-renderer.sh` before packaging; that checker
  statically verifies CSP, relative assets, expected file inventory and absence
  of token-like embedded text. Dynamic missing-bridge handling, authenticated
  cached thread/message reads, password begin/recovery/cancel/polling, ambiguous
  response reconciliation without credential replay, and user-safe status/error
  text are exercised separately by
  `desktop/scripts/check-bundled-renderer-behavior.mjs`.
- `desktop/scripts/inspect-mac-package.test.sh` covers the inspector's local
  structural invariants with fake `.app` fixtures: valid unpacked and
  `app.asar` payloads, ambiguous payload modes, sidecar duplication inside
  either Electron payload mode, package main drift, missing runtime entry, and
  extra unexpected payload entry, including `app.asar.unpacked` content and
  unexpected top-level `Contents/Resources` entries. It also covers missing
  renderer assets, non-loopback packaged renderer CSP, extra remote CSP connect
  sources, unexpected packaged renderer files, and token-like strings in
  packaged renderer files. It also covers the opt-in bundled-renderer, app-icon,
  and Developer ID Application signature requirements.
- `desktop/scripts/notarize-mac.test.sh` covers the notarization helper's local
  validation paths with a fake DMG: unsupported target, missing artifact,
  arbitrary-DMG app-inspection bypass rejection, versioned release-DMG
  app-bundle inference, missing credentials, keychain-profile dry-run, and
  Apple ID credential dry-run. It also builds a fake neighboring release `.app`
  and stubs `codesign` to prove ad-hoc signatures, non-Developer-ID authority
  chains, missing TeamIdentifier, missing hardened runtime metadata, and strict
  verification failures are rejected before Apple submission, while a Developer
  ID Application signature can pass dry-run validation.
- `desktop/scripts/smoke-packaged-app.test.sh` covers packaged-smoke prelaunch
  validation paths: missing app path, missing values for value-taking options,
  invalid timeout, negative-mode option pairing, incompatible
  cached-history/token-rotation modes, malformed app bundle, missing
  `Info.plist` / `CFBundleExecutable`, and the local
  signature-repair decision for valid, ad-hoc, and non-repairable signatures.
  It also covers positive-mode rejection when a plausible renderer result is
  paired with a non-zero app exit, a local-token leak flag, or a local-token
  redaction marker. It covers the real-cache helper options too:
  `--expect-message-text` requires `--expect-thread-text`, cached text
  assertions are rejected in negative mode, `--data-dir` is rejected unless
  paired with `--expect-body-text` or `--expect-thread-text`, missing
  `--data-dir` paths fail preflight, the helper forces an unreachable cloud
  base for text assertions, and valid text assertions pass when a fixture reports the expected cached
  thread/message visibility. It also covers generic `--expect-body-text`
  assertions for unauthenticated/no-cache packaged smoke.
- `desktop/electron/src/security-helpers.test.ts` and
  `desktop/electron/src/main-log.test.ts` cover Electron main-process and
  sidecar-output redaction for token-like strings, URL credentials, bare bearer
  fragments, API-key shaped fields, sensitive-key values, and dynamic object
  keys. Sidecar stderr is sanitized before both dev-terminal output and
  `logs/sidecar-main.log`.
- Packaged builds require `Resources/renderer/en/desktop/index.html` and fail
  startup when it is absent. They never load a hosted Renderer fallback and
  reject `WORKMAX_DESKTOP_RENDERER_URL` overrides.
- `desktop/scripts/smoke-packaged-app.sh` launches an already-built packaged
  macOS `.app` with a temporary data directory and a one-shot Electron smoke
  reporter. It asserts `app.isPackaged=true`, verifies the loaded URL is the
  exact bundled `file://.../Contents/Resources/renderer/en/desktop/index.html`,
  confirms `window.workmaxLocal` is exposed with matching app/sidecar versions,
  verifies the live packaged sidecar rejects missing, wrong, and duplicate
  local-token requests on `/health`, and also checks missing/wrong local-token
  rejection across representative packaged sidecar surfaces:
  `/auth/status`, `/system/diagnostics`,
  `/agent/threads?include_paused=false`, `/system/log`, and
  `/system/trigger-sync`. It asserts `/system/diagnostics` reports readable
  SQLite data/db/backup paths, `integrity_check: "ok"`, non-empty applied
  SQLite migrations, and Go heap/goroutine counters. Endpoint unit coverage
  also verifies diagnostics redacts token-like strings and URL credentials from
  sync-worker `last_error`; DiagnosticsPanel defensively re-sanitizes before
  rendering or copying support payloads, including bearer/basic auth,
  `client_secret`, `password`, compact `apikey`, and generic `secret` fields.
  The
  packaged smoke fails if the renderer body is blank. The Electron smoke
  reporter redacts and flags any raw local-token occurrence plus token/secret-
  like renderer text in renderer-observation fields before writing the JSON
  artifact, including compact `apikey`, generic `secret`, URL credentials,
  bearer/basic auth, and sensitive object keys; positive smoke requires both
  leak flags to be explicitly false. Positive smoke also requires the packaged
  app process to exit successfully after writing the renderer result; a
  non-zero process exit is treated as failure even if the JSON result exists.
  The helper
  repairs unsigned/ad-hoc local app signatures before launch so controlled
  smoke can run on hosts without Developer ID signing; this does not replace
  the separate notarization gate.
  The arm64 smoke passed on 2026-05-21 against
  `desktop/electron/release/mac-arm64/WorkMax Desktop.app`.
  With `--check-cached-history`, it enables a hermetic smoke-only sidecar mode
  that uses an in-memory token store, seeds one local thread/message in the
  temporary data directory, forces `WORKMAX_CLOUD_BASE=http://127.0.0.1:9`, and
  verifies the bundled renderer displays both the cached thread and cached
  messages without a live Go Server dependency. This does not touch the user's
  Keychain.
  With `--data-dir <existing-dir> --expect-thread-text <text>
  [--expect-message-text <text>]`, it launches against an existing Desktop data
  directory without seeding smoke data. Use this after an online authenticated
  packaged run has populated a real cache; the helper forces the sidecar cloud
  base to an unreachable loopback origin, while the user's normal
  Keychain-backed session remains the auth source.
  With `--data-dir <clean-dir> --expect-body-text "Auth state:
  unauthenticated"`, it can also capture the unauthenticated/no-cache packaged
  state after clearing the Desktop Keychain session.
  With `--check-token-rotation`, it launches the packaged app twice against the
  same temporary data directory and asserts the SHA-256 local-token fingerprint
  changes between sidecar spawns; the 2026-05-21 arm64 run passed.
  The same helper also has `--renderer-url ... --expect-failure ...` negative
  mode. The 2026-05-21 arm64 packaged runs proved an origin-root override and
  an arbitrary local `file:` override are rejected during startup before a
  renderer smoke result is written.
- `WORKMAX_DESKTOP_RENDERER_URL` must point to an exact `/desktop` route or the
  exact bundled file renderer entry; origin root, nested subpage, arbitrary
  local file, or URL-credential misconfiguration fails before opening the
  bridged window.
- The app remains single-instance; a second launch focuses the first window.

This is not yet a full public-release pass: the packaged `.app` now has local
runtime evidence that it chooses the bundled file renderer, reads seeded cached
history, rejects bad local tokens, and rotates the local-token fingerprint
across restarts. It still needs a real manual smoke against a previously
authenticated cache using the helper-enforced unreachable cloud base.

## 4. Public-Release Gates

Do not treat a build as public-release ready until these are cleared or
explicitly waived:

- real packaged GUI smoke against a previously authenticated cache with an
  unreachable sidecar cloud base; hermetic seeded cached-history smoke now has
  arm64 package evidence and forces the same unreachable base, but it does not
  exercise a real Keychain-backed authenticated cache. The executable helper
  path for this gate is now `smoke-packaged-app.sh --data-dir <existing-dir>
  --expect-thread-text <known-title> --expect-message-text <known-message>`
  which forces the sidecar cloud base to an unreachable loopback origin. Also
  run the unauthenticated/no-cache path with `--expect-body-text "Auth state:
  unauthenticated"` against a clean data dir after clearing the Desktop
  Keychain session; that helper mode forces the same unreachable sidecar cloud
  base.
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
