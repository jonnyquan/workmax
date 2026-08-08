# Server configuration

The Go API Server reads one YAML document selected in this order:

1. `-c /absolute/path/to/config.yaml`;
2. `BODO_CONFIG=/absolute/path/to/config.yaml`;
3. the Gin-mode default (`config.yaml`, `config.release.yaml` or
   `config.test.yaml`).

The separate `cmd/agent-worker` uses the same first two priorities but fixes
its local-development fallback to `config.yaml`: `-c` -> `BODO_CONFIG` ->
`config.yaml`. The API Server still uses its legacy watched Viper lifecycle;
the Worker reads its selected source exactly once into an immutable startup
snapshot. Changing the Worker file requires a process restart.

For local development, copy `../config.example.yaml` to the ignored
`../config.yaml`, then point its MySQL block at the existing external
development database. Do not start a local MySQL instance for WorkMax. Use
`../config.release.example.yaml` only as the release schema reference; never
edit either checked-in example with live values. Ordinary test and verification
targets do not read `config.yaml` or connect MySQL; database preflight and
contract targets are explicit opt-ins.

The loader does not expand `${ENV_VAR}` inside individual fields. Production
deployment must render the complete YAML from a Secret Manager, set file mode
`0600`, and point `BODO_CONFIG` at that file. Do not expose the file through a
container image layer, CI artifact, support bundle, log, or Desktop package.

`WORKMAX_SECRETS_KEY` is the one required secret that deliberately stays
outside YAML. Set it in the Server process environment to standard padded
base64 or raw URL-safe base64 that decodes to exactly 32 random bytes. The
Desktop Login Transaction production constructor validates this key during
startup and fails closed if it is absent or malformed; never commit the value
or print it in diagnostics. Route-catalog and ordinary offline tests use an
isolated test key and do not need a production value.

Before deployment, reject:

- any `CHANGE_ME` value;
- empty JWT, database, provider or webhook credentials required by the chosen
  feature set;
- public URLs outside the intended Go Server/API/CDN origins;
- writable workspace or upload paths shared by unrelated tenants;
- Agent sandbox bypasses that have not received an explicit security review.

The checked-in examples deliberately keep AI providers and Agent execution
disabled. Provider account credentials that are owned by database-backed
Work Agent accounts remain outside these YAML files.

## Server + Desktop URL boundary

This repository has no independent Web or Admin client. `backend_url`, Desktop
`WORKMAX_CLOUD_BASE`, OAuth callbacks and future commerce landing pages must
resolve to Go Server or an explicitly configured API gateway. The legacy
`system.frontend_url` key is temporarily retained for old auth/email callers,
but sanitized examples deliberately pin it to the same Server origin; new code
must not use it to recreate a browser application dependency.

The examples reserve `/api/desktop/billing/return` as the Server-owned Stripe
landing path. That route and the verified Desktop resume/entitlement-refresh
flow are not implemented yet, so Stripe must remain disabled in a Desktop-only
release until both are delivered. Checkout return is navigation only and must
never be treated as proof of payment; webhook/Server state remains authoritative.

## Agent platform rollout

`agent_platform_rollout` is a target-only cutover contract. Its sanitized
default keeps credential enforcement, Durable Turn public API, Worker and new
Starts off while preserving the current Desktop `legacy` transport. The API
Router and Desktop transport do not consume this block. The separate
`agent-worker` consumes only its Worker-owned startup projection, so editing it
cannot enable the candidate Agent v1 API or switch Desktop traffic.

Worker startup/composition readiness is role-scoped and derived from installed
SQL, lease/fencing, outbox and settlement objects; operator-provided booleans
cannot make an absent dependency ready. API EventStream, credential, token and
active device-session readiness remain owned by their future API/Desktop
process composition. Canary membership is computed from the authenticated
server-side subject; never select it from a client header or request parameter.

### Agent worker startup

The Worker loader uses an isolated Viper instance over the selected file's
bytes. It does not call `core.Viper`, install a watcher, mutate shared globals,
retain raw YAML or expand `${ENV_VAR}` field placeholders. It decodes only the
Worker mode and four Worker readiness declarations from the rollout block, so
even structurally invalid API/Desktop fields cannot block this role. Its
snapshot keeps that Worker-only projection, source kind and a SHA-256 digest;
MySQL is decoded and retained by value only after Worker is known to be on or
the operator explicitly requests the standalone database preflight. An
ordinary Worker-off start still ignores MySQL completely.

When `durable_turn.worker` is `off`, startup exits before the production build
boundary, even if Public API or Desktop fields are enabled or malformed. When
Worker is `on`, Worker declarations and required MySQL fields are validated
first; missing production wiring fails closed. Read or parse failures do not
return the selected path, raw YAML, password, DSN or underlying error.

P0-038 adds a second, pure gate before dependency-acquisition I/O for a future
production dependency plan. It requires a separate artifact-identity input
shape, complete Plugin release/parity/timeout/Topic declarations and typed
factories for the database, Credits Settlement, each Plugin executor and each
Effect Topic deliverer. Validation invokes none of those factories. P0-039's
candidate Builder constructs exact Claim scope, Topic routing and the
all-child composite probe internally from acquired results; they are not
external catalog slots. The gate does not prove factory SQL/Provider behavior,
individual probe truthfulness or a build/parity ledger; no production evidence
producer or catalog is installed, and the shipped Builder remains
`unwiredWorkerComposition`.

The validated database input contains bounded scalar fields and shares none of
the mutable MySQL Driver/TLS/map objects produced during parsing. A future
factory creates a fresh canonical Driver config from that input; it cannot
receive the raw startup snapshot or the legacy DSN helper.

This is a startup contract, not production readiness. The Worker command now
has a `RuntimeProbe` boundary, lifecycle/freshness state machine and an unbound
standard-library Handler for exact `GET /livez` and `GET /readyz`. Constructing
that Handler opens no socket and does not register on Gin, the API Router or
`http.DefaultServeMux`; current tests use fake probes only. Ready additionally
requires fresh scheduler pulses from Worker, Reconciler and Dispatcher. Build,
probe and shutdown boundaries have hard deadlines, so a dependency that
ignores Context cannot block process return; a violating implementation can
still leave one quarantined goroutine until process exit.

### Read-only database preflight

The explicit command below reads the selected owner-only regular file whose UID
matches the process, opens a bounded MySQL pool, performs checks with no
persistent data or schema mutation, closes the pool and exits. Connection
initialization may issue session-only `SET NAMES` and
`SET foreign_key_checks=1`; it never applies a migration, writes a row, starts
a Worker loop, binds a listener or changes rollout state:

```bash
make check-agent-worker-db
# Or select another absolute file:
make check-agent-worker-db AGENT_WORKER_CONFIG=/absolute/path/to/config.yaml
```

Remote endpoints use hostname-verified TLS 1.2 or newer by default. A legacy
development database that cannot negotiate verified TLS may be inspected only
with the narrow, explicit command below after review and only through a trusted
private network, VPN or SSH tunnel. Plaintext can expose the complete database
credential to interception and is not a routine fallback:

```bash
cd server
go run ./cmd/agent-worker -check-database \
  -allow-remote-plaintext-database \
  -c /absolute/path/to/config.yaml
```

The plaintext flag is invalid without `-check-database`; it cannot authorize a
normal Worker start and an explicit `tls=true` is never downgraded. The driver
accepts only the role-owned `charset`, `parseTime`, `loc`, TLS and timeout
options, forces UTC and foreign-key checks, rejects unsafe/unknown options,
caps the pool at 32 open / 16 idle connections and caps dial/read/write
timeouts at 30 seconds. Startup pins one connection and checks the exact
database, session foreign-key enforcement, TLS cipher when required, five
InnoDB tables, twelve exact non-prefix unique indexes and seven exact RESTRICT
foreign keys. Errors expose only a stable settings/network/TLS/auth/database/
schema phase, never a path, DSN, credential or driver detail.

Linux and macOS enforce UID ownership in code. Other platforms fail closed
until an equivalent owner policy is implemented. Deployment must additionally
control filesystem ACLs and use atomic secret-file replacement rather than
modifying the selected inode while it is being read.

The check-only MySQL factory and schema probe are now present. P0-039 also
delivers unmounted exact Plugin-snapshot Claim filtering, per-Plugin Effect
Topic enforcement, internally constructed Topic routing/composite probing, a
cancellable acquisition guard and an ownership-transfer seal. Trusted
artifact/parity evidence; real database, Settlement, domain and Effect
factories with truthful child probes; the shipped production Worker
composition; a protected operator listener; metrics; and deployment wiring
remain pending and fail closed. Ordinary tests use fakes, in-memory SQLite and
no-network Driver/config fixtures. The external MySQL contract is a separate
opt-in target that writes and cleans only its owned rows, never migrates schema,
and must not be run as part of ordinary local development.
A successful Worker lease heartbeat does not prove that a long-running Plugin
executor is making domain progress, and a `/readyz` failure does not itself
pause this pull-based Worker from claiming another Turn. Production wiring must
therefore define per-Plugin execution/progress limits and an explicit
pause-claim/drain or alert/restart policy. Database reachability does not prove
the schema contract; a passing schema contract would still not prove domain
adapter readiness, migration completeness or production operational soak.
