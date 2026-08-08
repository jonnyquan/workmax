# WorkMax

[![License: AGPL v3](https://img.shields.io/badge/license-AGPL--3.0-blue.svg)](LICENSE)

WorkMax is an open-source Go service platform with one official Desktop Agent
client. Its product/runtime deliverables are only `server/` and `desktop/`;
there is no independent Web or Admin client. All user-facing Agent workflows
belong in Desktop, while Go Server owns cloud APIs and durable services.

The canonical architecture is documented in
[`ProjectDocs/writer-work-agent-plugin-platform-design-2026-08.md`](ProjectDocs/writer-work-agent-plugin-platform-design-2026-08.md).
The open-source **end-user runtime** is local-first Desktop: SQLite on device,
remote auth at `https://workmax.app`, then either user-owned local model config
or official hosted model quota — see
[`ProjectDocs/oss-local-desktop-runtime-mode-2026-08.md`](ProjectDocs/oss-local-desktop-runtime-mode-2026-08.md).
**Commercialization** (hosted SaaS web/admin, billing packaging, Team/Enterprise)
lives in the sibling **WorkMax Plus** repository (`workmaxplus`), not as a
required surface in this open-source tree. The repository boundary is recorded in
[`ProjectDocs/adr/2026-08-08-workmax-plus-repository-boundary.md`](ProjectDocs/adr/2026-08-08-workmax-plus-repository-boundary.md).
Local vs official model route settings (Sidecar API, Keychain for API keys) are
specified in
[`ProjectDocs/design/local-model-route-settings-2026-08.md`](ProjectDocs/design/local-model-route-settings-2026-08.md). The cloud/self-hosted Server wiring
slice after P0-048 is
[`ProjectDocs/p0-049-production-wiring-evidence-gates-design-2026-08.md`](ProjectDocs/p0-049-production-wiring-evidence-gates-design-2026-08.md)
(defaults remain fail-closed; not required to start Desktop).

## Current repository state

Keep source changes scoped and reviewed: do not use broad staging commands that
could include credentials, uploads, caches or release artifacts. The read-only
`make baseline-audit` gate classifies untracked files against the repository's
Server/Desktop-only source policy before they are considered for inclusion.

| Surface | Current location | Current state |
|---|---|---|
| Go Server | `server/` | Cloud APIs, durable Agent services and Desktop resources |
| Desktop shell | `desktop/electron/` | Electron main/preload and Go Sidecar lifecycle |
| Desktop Renderer | `desktop/renderer/` | Bundled sole Agent UI; login, cached history, idempotent PPT-thread creation, synced-thread continuation, explicit interrupted-turn recovery, and **Models** local/official route settings (bridge alpha.7) are current; local model **inference** and event-cursor Attach / full durable workbench remain partial |
| Architecture | `ProjectDocs/` | Consolidated Server/Desktop design baseline |

Stripe callbacks now have a migration-owned Provider Event Inbox and
transactional Commerce Outbox candidate. The reconciler remains opt-in
(`system.cron.commerce_event_reconciler: false`) until migration `20260811`,
real MySQL/Stripe recovery tests, operational review, and an Outbox dispatcher
are complete.

Some Go Server packages still preserve legacy/internal route groups while the
backend is consolidated. They are not separate client deliverables. Desktop
loopback and cloud-resource contracts remain the supported user-client boundary.

## Prerequisites

- Go 1.24.1 or newer, with the toolchain declared by `server/go.mod`
- Node.js 20 LTS or newer
- npm with the committed `desktop/electron/package-lock.json`
- Network access for the first download of the pinned Go and npm dependencies

## Bootstrap and verification

```bash
make doctor
make baseline-audit
make bootstrap
make verify-core
```

Useful focused targets:

```bash
make test-go
make test-go-desktop
make test-agent-platform
make test-electron
make test-boundaries
make test-config
make package-preflight
make source-baseline-audit
make secret-audit
make license-audit
```

`make fmt-audit` reports the imported formatting backlog without rewriting it.
Formatting the entire imported tree must be a separate mechanical change.

## Configuration and secrets

Live YAML configuration, local model-switch scripts, signing material and
environment files are ignored. Track only sanitized `*.example` templates.
The current loader uses `BODO_CONFIG` to select a YAML file; it does not provide
field-by-field environment expansion. Local development uses the private,
ignored `server/config.yaml` (initially copied from `server/config.example.yaml`)
and its configured external database. Do not start a local MySQL instance for
WorkMax development. Default unit/verification targets explicitly clear all
MySQL contract-test environment switches and use fakes or SQLite fixtures; only
the opt-in `make test-agent-platform-mysql` target may connect to the database
selected by `server/config.yaml`. Production should render a mode-0600
configuration file from a Secret Manager and point `BODO_CONFIG` at it. Use
`server/config.release.example.yaml` only as a production schema reference. The
operational rules and credential boundaries are documented in
`server/config/README.md`.

The Server-owned Desktop Login Transaction also requires
`WORKMAX_SECRETS_KEY`: base64 (standard padded or raw URL-safe) that decodes to
exactly 32 random bytes. It is intentionally not stored in `config.yaml`.
Production Server construction validates it before serving login traffic and
fails startup when it is missing or malformed.

Never commit:

- `server/config.yaml` or production/local variants;
- `switchglm.sh` / `switchkimi.sh`;
- `server/uploads/`, SQLite databases, logs or caches;
- Electron `bin/`, `dist/`, `release/` or `node_modules/`;
- signing keys or notarization credentials.

Run `make baseline-audit` before preparing any imported source area. The
source-baseline gate classifies only untracked paths and refuses live config,
runtime data, caches, binaries and release/archive artifacts; the secret gate
reports only rule names and file paths. Neither command stages files or prints
credential values. These fast gates do not replace a maintained entropy and
Git-history scanner in CI.

## Generated and runtime data

Generated build output is reproducible and excluded from source control.
`server/uploads/` is runtime/user data and must not be treated as a cleanable
build directory. There is intentionally no broad `make clean` target.

## Contributing and support

Contributions are welcome. Read [CONTRIBUTING.md](CONTRIBUTING.md) before making
a change and follow the [Code of Conduct](CODE_OF_CONDUCT.md) in all project
spaces. For usage help and ordinary bug reports, see [SUPPORT.md](SUPPORT.md).

Do not report vulnerabilities publicly. Follow the private disclosure process
in [SECURITY.md](SECURITY.md).

## License

WorkMax is licensed under the
[GNU Affero General Public License v3.0 only](LICENSE) (`AGPL-3.0-only`).
Third-party dependencies and bundled components remain subject to their own
license terms; see [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md). The public
source for Desktop releases is this repository. Maintainers should follow
[RELEASING.md](RELEASING.md) so binaries, corresponding source, and notices stay
aligned.
