# Writer Agent capabilities: skills, tools, plugins, and external research

This document designs five related product surfaces:

1. platform-authored Writer Agent skills and first-party tools;
2. per-user capability enablement;
3. a first-party Capability Library and reusable instruction presets;
4. first-party Scenario Recipes that compose a repeatable writing-integrity
   workflow without creating permissions; and
5. external research that the writer explicitly enables for one turn.

Status: detailed implementation proposal, refined 2026-07-27. The current
workspace policy hook and shipped empty setting-source configuration are
implemented; tenant OS isolation, the capability catalog, selection model,
platform plugin, external-research MCP tools, and Capability Library described
here are not; neither is the Scenario Recipe catalog. Repository facts were
rechecked against the current Go and Next.js implementation on the same date;
the exact limits are recorded below.

## Decisions

- The first release supports only platform-authored, prompt-only skills and
  first-party typed tools.
- A plugin is a deployment package, not a user-facing product or an installable
  tenant object.
- Platform skills are enabled per user. External research is granted per turn
  and defaults to off. Thread and project capability overrides are not part of
  the MVP.
- Enabling a skill never grants an operation. Availability, user selection,
  action grants, and destructive confirmation are separate policy layers.
- One immutable capability snapshot is resolved during turn admission. The
  spawn options, runtime hooks, prompt projection, retry/idempotency checks,
  audit record, events, and API projection all derive from that snapshot.
- The current CLI-native `WebSearch` and `WebFetch` tools must be removed from
  Writer Agent before the per-turn control ships. External research exposes
  server-owned `research.search` and `research.read` MCP tools together.
- A search result is discovery, not evidence. Only an immutable page capture
  created by `research.read` can later participate in claim-level citation
  evidence.
- The first catalog UI is called the **Capability Library**, not a marketplace.
  Third-party plugins remain unscheduled until tenant process isolation exists.
- A **Scenario Recipe** is a first-party, code-owned composition of inputs,
  preset/profile references, required capabilities, suggested turn settings,
  and an output contract. Applying one opens a reviewable draft; it never
  installs code, enables a capability, grants an action, or starts a run.
- Writer Agent has no user-selectable `Full Access` mode. A confirmation can
  approve only one sufficiently specific action that every stronger policy
  layer already permits.
- A future connected service is not automatically offered or authorized.
  Connector health, turn offering, operation authorization, and side-effect
  confirmation remain separate.
- External research remains inside the normal flat turn price for the first
  release, with separate provider-cost telemetry and abuse limits.

## Scope and current status

| Area | Current state | Target state |
| --- | --- | --- |
| Host settings isolation | Shipped config is empty and produces explicit `--setting-sources=`; non-empty config is still accepted | Reject non-empty sources at startup and regression-test |
| Tool authorization | Implemented: default-deny `PreToolUse` hook | Drive it from a per-turn policy snapshot |
| Built-in web tools | Offered and hook-authorized in code; gateway operation is unverified | Remove from the base tool set |
| Capability catalog/resolver | Not implemented | Code-owned, versioned, fail-closed |
| Platform skills | Not shipped | One read-only, prompt-only plugin package |
| User selection | Not implemented | Account-scoped selection with optimistic versioning |
| External research | No real per-turn boundary | First-party typed MCP tools behind one turn grant |
| Web-source provenance | Not implemented | Immutable capture before citation binding |
| Capability Library | Not implemented | First-party catalog and toggles |
| Instruction presets | Only thread `customPrompt` exists | Official and private text presets |
| Scenario Recipes | Not implemented | First-party code-owned recipes with review-before-run |
| Third-party marketplace | Not supported | Deferred and unscheduled |

The current-state rows above distinguish four facts that must not be collapsed
into the word "enabled":

- **packaged**: code or an asset exists on the node;
- **offered**: the CLI receives the skill or tool for this turn;
- **authorized**: the runtime policy would allow its exact invocation;
- **operational**: the backing CLI/provider has been exercised successfully.

As of this design, `writerAgentTools()` offers `Read`, `Write`, `Edit`, `Glob`,
`Grep`, `WebSearch`, `WebFetch`, planning tools, `AskUserQuestion`, and the
internal citation-evidence tool. The hook also authorizes both web tools.
Whether the configured Anthropic-compatible gateway actually implements the
CLI's server-side `WebSearch` is still an operational unknown. Related runtime
and roadmap documents that describe a smaller tool set must be corrected after
the Phase 0 baseline audit.

### Repository cross-check on 2026-07-27

The proposal was checked against the current repository rather than inferred
from product UI alone:

- `server/router/pro/tools/writer_agent_router.go` has no capability, preset, or
  recipe route. Every such endpoint in this document is a target contract.
- `writerAgentRunRequest` in
  `server/api/pro/tools/writer_agent_helpers.go` and its frontend request in
  `web/app/[locale]/(tools)/tools/writer-agent/document-model.ts` contain only
  prompt, selected sources, and document key. `externalResearch` and recipe
  provenance are not implemented.
- `server/service/tools/writeragent/runner.go` still builds one static tool
  list containing native `WebSearch` and `WebFetch`, and configures only the
  citation MCP server. There is no capability resolver, platform plugin, or
  `research.search`/`research.read` server.
- The same runner installs a wildcard default-deny hook, but its current
  allow-path authorizes non-empty `WebSearch` queries and validated public
  `WebFetch` URLs without a per-turn grant. Snapshot-driven policy and
  `UserPromptExpansion` control remain target work.
- Development and production configuration currently leave setting sources
  empty, but the normalizer still accepts `user`, `project`, and `local`.
  Consequently host-settings isolation is a shipped configuration invariant,
  not yet a hard startup invariant.
- `server/model/writer_agent.go` stores provider session ID and creation time
  but no session contract hash. Settings updates clear the ID eagerly, while
  any non-empty ID can otherwise be resumed.
- Durable admission, leasing, fencing, cancellation, and credit reservation
  exist in `writer_agent_admission.go` and `model/writer_agent_turn.go`.
  Capability snapshots, snapshot hashes, complete request fingerprints, and
  capability usage records do not.
- Current replay comparison covers the document tuple, prompt, and sources.
  It does not cover capability or per-turn flags. Generic message-metadata
  parsing also tolerates malformed JSON, so the future security snapshot needs
  its own strict fail-closed codec.
- Citation evidence currently binds selected uploaded/output files, successful
  reads, source/read hashes, exact quotes, and document persistence. The source
  kind has no web variant, and immutable web capture is not implemented.
- The current composer exposes source selection, writing settings, and send.
  It has no Web toggle, Capability Library, preset, or recipe entry. Its
  `customPrompt` limit uses JavaScript UTF-16 `.length`, so the code-point
  consistency fix below is also target work.

## Non-goals

The following are intentionally outside the first releases:

- user-uploaded or URL-installed skills/plugins;
- plugin hooks, commands, agents, LSP servers, monitors, MCP declarations, or
  executable scripts;
- arbitrary shell access;
- a general connector marketplace;
- a global or user-selectable `Full Access` mode;
- community, user-authored, or executable Scenario Recipes;
- implicit long-term memory learned from full conversations or documents;
- task/project capability inheritance or organization policy administration;
- multiple agents concurrently writing the same document;
- a user-facing Expert Group or raw multi-agent conversation surface;
- publishing, sending, sharing, overwriting, or deleting external resources;
- treating a live URL, search snippet, or model-authored link as verified
  citation evidence.

## Runtime facts and corrected security boundary

### One process still serves every tenant

The service starts a CLI subprocess per turn and currently runs as root. Thread
workspaces are path-isolated, but there is no per-tenant OS/container boundary.
A capability that escapes the tool policy can therefore threaten more than the
current thread.

### Filesystem settings stay off

The shipped development and production configuration is empty and produces an
explicit `--setting-sources=`. Host-account settings, skills, hooks, agents,
and permissions must not enter tenant turns again. The configuration model
still accepts non-empty sources, so Phase 0 must make an empty list a startup
invariant for Writer Agent rather than relying on YAML discipline.

The local Go SDK constructs `--plugin-dir` independently from
`--setting-sources`, and its `WithSkills` default does not replace an explicitly
empty setting-source list. That makes a platform plugin plus an exact skill
allowlist plausible. It is not a release fact until a real CLI canary verifies
that:

1. the platform plugin skill is discovered with `--setting-sources=`;
2. host user/project skills remain absent;
3. an omitted skill is neither listed nor invocable;
4. a direct slash invocation cannot bypass the selected skill list;
5. the CLI reports the expected qualified name, such as
   `writego:source-grounded-review`, to the SDK hooks.

Official SDK documentation normally associates skill discovery with user and
project setting sources, while plugin directories are a separate CLI path. The
canary, not inference from argument construction alone, is the shipping gate.

### `PreToolUse` is not an OS sandbox

`PreToolUse` is the current tool-call authorization boundary. It is not the
complete plugin boundary and not tenant process isolation.

This distinction matters because a Claude plugin can contain hooks, MCP
servers, agents, LSP servers, monitors, settings, and binaries. A skill can also
carry lifecycle hooks or dynamic shell-backed context. Those paths are not made
safe merely because the ordinary `Bash` tool is absent. User-entered slash
commands expand through `UserPromptExpansion`, outside the model's normal
`Skill` tool call.

Therefore the first release relies on all of the following:

- an exact `SkillsList("writego:...")` at spawn;
- an exact skill-name check for model `Skill` calls;
- a `UserPromptExpansion` deny policy for direct commands, or a verified CLI
  mechanism that makes platform skills non-user-invocable;
- a restrictive plugin/skill linter described below;
- the existing default-deny tool hook;
- a read-only, deployment-owned plugin directory;
- no third-party plugin content.

If the current Go SDK cannot expose `UserPromptExpansion`, either extend it and
test it or do not ship user-addressable platform skills. An API-side string
check for prompts beginning with `/` is not an adequate replacement.

### Skill tool declarations are not grants

The SDK documentation says `allowed-tools` in `SKILL.md` does not control SDK
skill access; the query's main allowed-tool configuration does. The platform
profile rejects `allowed-tools` anyway so behavior cannot change silently
across CLI versions or alternate invocation paths.

A catalog skill may declare which product grants it depends on, but it cannot
create those grants. For example, a source-review skill can depend on selected
source reads; it cannot turn on external research by naming a web tool.

## Terminology and namespaces

| Term | Definition |
| --- | --- |
| Capability | A user-understandable Writer Agent behavior, backed by one skill, one or more typed tools, or both |
| Skill | A versioned, platform-authored prompt package that contains instructions only |
| Tool | A typed server-owned operation with input validation, output bounds, policy, and telemetry |
| Plugin | The read-only Claude packaging directory used to deliver platform skills |
| Preset | Saved instruction text copied into a thread's standing instruction; it has no tools or grants |
| Scenario Recipe | A versioned first-party workflow composition that pre-fills reviewed inputs, settings, and an output contract; it is neither a capability nor a grant |
| Connector | A credential and account boundary for one external service; being connected does not make an operation offered or authorized |
| Writer preferences | Future opt-in structured preferences such as language, audience, tone, formatting, and citation style; never implicit document memory |
| Selection | A user's explicit preference for an account-scoped capability |
| Action grant | Runtime permission for an operation class such as external research |
| Confirmation | User approval attached to a specific future side effect such as publish or delete |
| Resolved snapshot | The immutable capability and grant contract admitted for one durable turn |
| Session contract | Standing behavior that must match before an SDK session may resume |

Writer Agent capability keys use a distinct namespace:

```text
writer.skill.source-grounded-review
writer.tool.external-research
writer.tool.integrity-review
```

Plugin skill invocation names use the plugin namespace:

```text
writego:source-grounded-review
```

This catalog must not be confused with `server/service/platform`, whose
existing capability IDs such as `content.detect` and `research.search` describe
callable public platform operations. A future Writer Agent tool may adapt one
of those operations, but the runtime policy catalog remains Writer
Agent-specific.

Capability keys are lowercase ASCII, validated against a fixed grammar,
immutable, and never reused. A renamed product label does not rename its key.

## Policy layers

The effective policy is deliberately not a single toggle:

```text
shipped and healthy
    intersect entitled
    intersect not denied by deployment/admin policy
    intersect selected at a supported scope
    intersect requested and granted for this turn
```

A narrower scope can remove access, but it cannot override an entitlement,
deployment deny, or emergency deny.

### Current action grants

| Action | Initial grant source | Confirmation |
| --- | --- | --- |
| Read the turn's selected task sources/context | Core turn admission | None |
| Create a new output and immutable document revision | Core turn admission | None |
| Edit a file created by the same turn | Core turn admission | None |
| Search the public web | Per-turn `externalResearch=true` | The explicit composer toggle is the grant |
| Read a search result page | Same external-research grant | No second confirmation |
| Publish, send/share, overwrite existing external data, delete | Unsupported | Future action-specific confirmation |

Skill selection is absent from the grant-source column on purpose.

An emergency policy may only narrow an already-admitted snapshot. It is checked
again by the hook/provider adapter on each call. It can stop new operations in
an in-flight turn, but it cannot remove skill instructions already loaded into
that session; a critical instruction-package incident also requires cancelling
or draining affected turns.

### No `Full Access` and no confirmation escalation

Writer Agent never exposes a global permission bypass. A confirmation is valid
only after the server has established all of the following:

```text
actor and tenant access
    intersect entitlement
    intersect deployment and organization policy
    intersect admitted capability snapshot
    intersect operation-specific action grant
    intersect target-specific confirmation
```

The confirmation cannot widen any preceding set. A static package scan,
readiness probe, connector health check, capability selection, or recipe
application is not authorization.

A future side-effect confirmation is a server-minted, single-use envelope:

```go
type WriterAgentActionConfirmation struct {
    ActionKey      string
    Target         WriterAgentActionTarget // tenant/project/document/revision/resource
    ImpactScopes   []ActionImpact          // write | send | overwrite | delete
    Reason         string
    PreviewHash    string
    SnapshotHash   string
    ExpiresAt      time.Time
    IdempotencyKey string
}
```

The UI shows the operation, exact target, impact scope, reason, and proposed
diff/result before approval. Execution rejects an expired, replayed,
wrong-actor, wrong-target, changed-preview, or changed-snapshot envelope.
There is no "allow all this turn" fallback for unrelated operations.

## Code-owned capability catalog

The database stores user state, never executable capability definitions.
Definitions stay in Go so an unknown database row cannot name a filesystem
asset or tool.

A representative shape is:

```go
type WriterAgentCapabilitySpec struct {
    Key                   string
    Kind                  CapabilityKind // skill | typed_tool | composite
    ImplementationVersion string
    ContentDigest         string

    Category               string
    NameKey                string
    DescriptionKey         string
    FallbackName           string
    FallbackDescription    string
    DefaultEnabled         bool
    SelectionScope         SelectionScope // always | user | turn
    RequiredEntitlements   []string
    Dependencies           []string
    ToolPolicies           []WriterAgentToolPolicyKey
    RequiredActionGrants   []string
    SkillInvocationNames   []string
    SessionAffecting       bool
    Risk                   CapabilityRisk
}

type CapabilityRisk struct {
    ReadsTaskContent bool
    WritesDocument   bool
    ExternalNetwork bool
}
```

`WriterAgentToolPolicyKey` values are compile-time constants. A fixed runtime
registry maps each key to exact tool names, validators, MCP aliases, and
required grants. The catalog may reference those constants, but neither a
database row nor an asset manifest can supply an arbitrary tool name or policy
function. `ToolPolicies` declares the maximum dependency of an implementation;
it does not add those tools to the turn's grants.

The catalog exposes a deterministic release digest over sorted keys, versions,
content digests, dependencies, and policy fields. Display copy changes do not
change a session contract. Instruction/tool behavior changes do.

Optional and networked capabilities default off. Once a user-configurable
capability reaches GA, changing its default is a migration/rollout event rather
than a casual catalog edit because every user without an explicit row would
change behavior.

### Skills-only plugin profile

The first package is one read-only plugin:

```text
server/agent-plugin/
├── .claude-plugin/
│   └── plugin.json
└── skills/
    └── source-grounded-review/
        └── SKILL.md
```

The runtime resolves the configured path to an absolute path and passes:

```go
claudesdk.WithPlugins(claudesdk.PluginConfig{
    Type: claudesdk.PluginTypeLocal,
    Path: absolutePluginPath,
})
claudesdk.WithSkills(claudesdk.SkillsList(
    "writego:source-grounded-review",
))
```

Only pass the plugin when at least one platform skill is effective. A turn with
zero selected platform skills passes `SkillsNone()` and no plugin directory.

### Required release/startup validation

The validator fails closed if any of these checks fail:

- plugin path is absolute, read-only to tenants, and contains no symlink;
- `plugin.json` has the expected plugin name and release version and contains
  no settings or experimental executable components;
- every catalog skill maps to exactly one directory and one regular
  `SKILL.md`;
- every `SKILL.md` name and qualified invocation matches the catalog;
- the canonical content SHA-256 matches the catalog digest;
- YAML frontmatter is restricted to an explicit safe allowlist;
- `user-invocable` is false unless the direct-command authorization path has
  been implemented and tested;
- `allowed-tools`, `disallowed-tools`, `hooks`, `context`, `agent`, `model`,
  `background`, and executable/dynamic-context fields are rejected;
- shell interpolation, command expansion, `${CLAUDE_SKILL_DIR}`,
  `${CLAUDE_PROJECT_DIR}`, and script references are rejected;
- no `commands/`, `agents/`, `hooks/`, `scripts/`, `bin/`, `monitors/`,
  `workflows/`, `.mcp.json`, `.lsp.json`, `settings.json`, or extra executable
  files exist;
- dependency graphs are acyclic and every declared tool/grant is registered.

The first profile allows no supporting files. The workspace `Read` policy would
deny those paths anyway, and a single-file profile is auditable.

The development tree currently resolves the Go Agent SDK through a mutable
local `replace`. Release builds must instead pin or vendor one audited SDK
revision and pair it with an exact CLI version. A canary verifies the supported
plugin, skill, hook, event, resume, and setting-source behavior before that pair
can serve Writer Agent traffic.

`GET /api/ready` should add `checks.writerAgentPlugin`. The existing readiness
endpoint treats optional module failures as degraded while returning HTTP 200;
deployment automation must therefore enforce the check explicitly. A turn that
would use a missing/mismatched skill is rejected before credit reservation,
never silently narrowed.

During rolling deployment, an old node that encounters an enabled selection key
it does not recognize returns `CAPABILITY_CATALOG_SKEW` before admission. It
must not run the turn without a capability that the user was told was enabled.
New user-configurable capabilities default off, and the catalog endpoint should
not be exposed until all serving nodes have the matching asset release.

## Code-owned Scenario Recipe catalog

A recipe is a product composition, not another capability catalog row. Keeping
the catalogs separate prevents a result-oriented shortcut from becoming a
permission shortcut.

```go
type WriterAgentScenarioRecipeSpec struct {
    Key                  string
    Version              string
    ContentDigest        string
    NameKey              string
    DescriptionKey       string
    RequiredInputs       []RecipeInputRequirement
    PromptTemplateKey    string
    InstructionPresetRef *RecipePresetRef
    RequiredCapabilities []string
    SuggestedTurnFlags   RecipeTurnDefaults
    OutputContract       []RecipeDeliverable
}

type RecipeTurnDefaults struct {
    ExternalResearch bool // UI suggestion only; never a grant
}
```

The initial catalog is compiled into the server and contains no executable
content, arbitrary tool names, connector credentials, or policy functions.
Candidate keys include:

```text
writer.recipe.academic-originality-review
writer.recipe.citation-fact-check
writer.recipe.publishing-integrity-review
writer.recipe.responsible-rewrite
writer.recipe.enterprise-content-compliance
```

Applying a recipe:

1. resolves its exact key, version, and digest from the serving node;
2. opens a review screen with required inputs, standing-instruction changes,
   required capabilities, external data destinations, limits/cost, and output
   contract;
3. pre-fills the composer and writing setup only after the user chooses
   `Use recipe`;
4. does not save a capability toggle, grant external research, mutate a thread
   setting, or submit a turn without the corresponding explicit user action;
5. blocks submission with a stable explanation if a required capability is
   locked, denied, degraded, or unavailable.

The run request carries only a recipe reference that the server re-resolves:

```go
type WriterAgentRecipeRef struct {
    Key           string `json:"key"`
    Version       string `json:"version"`
    ContentDigest string `json:"contentDigest"`
}
```

If the reviewed version has changed, admission returns
`SCENARIO_RECIPE_CHANGED` and requires a new review. The request fingerprint,
turn snapshot, audit record, and message projection persist recipe
key/version/digest. A catalog update affects only newly reviewed turns.
Required capabilities are assertions checked against the normal resolver; they
do not become effective merely because the recipe lists them. Likewise,
`SuggestedTurnFlags.ExternalResearch` may prefill a visible draft toggle, but
only the submitted `externalResearch=true` creates the one-turn grant.

Initial API surface:

```http
GET /api/tools/writer-agent/scenario-recipes
```

There is no recipe `POST`, `PUT`, import, publish, community, or installation
endpoint in the MVP.

## Persistence and supported scopes

Four product states remain separate:

| State | Scope | Persistence | Session effect |
| --- | --- | --- | --- |
| Enabled platform capabilities | User account | Capability selection tables | Changes the standing session contract |
| Instruction preset / `customPrompt` | Thread | Thread settings with preset snapshot | Changes the standing session contract |
| Applied Scenario Recipe | One reviewed turn draft | Recipe reference in request/snapshot/history | Does not grant access; referenced standing changes follow their own rule |
| External research | One turn | Run request and resolved turn snapshot | Does not change the standing session contract |

MVP capability selection is account-scoped. Task/project inheritance requires a
separate scoped policy design with actor identity, ACLs, tri-state inheritance,
admin denies, and billing ownership. It is not "one nullable column later."

### Proposed schema

```sql
CREATE TABLE w_workagent_capability_profile (
  uid         bigint NOT NULL,
  revision    bigint unsigned NOT NULL DEFAULT 0,
  created_at  datetime NOT NULL,
  updated_at  datetime NOT NULL,
  PRIMARY KEY (uid)
);

CREATE TABLE w_workagent_capability_selection (
  id              bigint unsigned NOT NULL AUTO_INCREMENT,
  uid             bigint NOT NULL,
  capability_key  varchar(120)
    CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  enabled         tinyint(1) NOT NULL,
  created_at      datetime NOT NULL,
  updated_at      datetime NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_workagent_capability_selection (uid, capability_key)
);

ALTER TABLE w_workagent_thread
  ADD COLUMN agent_session_contract_hash char(64) NOT NULL DEFAULT '';

ALTER TABLE w_workagent_turn
  ADD COLUMN capability_snapshot json NULL,
  ADD COLUMN capability_snapshot_hash char(64) NOT NULL DEFAULT '',
  ADD COLUMN request_fingerprint char(64) NOT NULL DEFAULT '';

CREATE TABLE w_workagent_capability_usage (
  id                       bigint unsigned NOT NULL AUTO_INCREMENT,
  turn_id                  bigint unsigned NOT NULL,
  uid                      bigint NOT NULL,
  capability_key           varchar(120)
    CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  provider_key             varchar(64)
    CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
  search_attempts          int unsigned NOT NULL DEFAULT 0,
  page_read_attempts       int unsigned NOT NULL DEFAULT 0,
  success_count            int unsigned NOT NULL DEFAULT 0,
  failure_count            int unsigned NOT NULL DEFAULT 0,
  result_count             int unsigned NOT NULL DEFAULT 0,
  returned_bytes           bigint unsigned NOT NULL DEFAULT 0,
  limit_hit                tinyint(1) NOT NULL DEFAULT 0,
  estimated_cost_micros    bigint unsigned NOT NULL DEFAULT 0,
  created_at               datetime NOT NULL,
  updated_at               datetime NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_workagent_capability_usage
    (turn_id, capability_key, provider_key),
  KEY idx_workagent_capability_usage_uid_created (uid, created_at)
);
```

There are no foreign keys, consistent with the Writer Agent aggregate. Account
cleanup hard-deletes the profile and selection rows.

The usage row is a minimal per-turn/provider aggregate for cost, abuse, and
reliability accounting. It stores no query, URL, page body, prompt, or excerpt.
It follows the turn's audit retention rather than message-content retention,
and its counters are updated idempotently from provider-call attempt IDs.

The profile row supplies optimistic concurrency and a cheap revision fence.
It is created lazily. Repeating an update that already has the desired value is
idempotent and does not increment `revision`.

Unknown or retired selection rows never enable anything. Preserve them for a
bounded rollback window, then remove them by migration; never reuse the key.

### Locking and update semantics

Capability update and turn admission use the same order:

```text
user row -> capability profile/selection -> thread row
```

Admission currently locks the user only for some quota paths. Capability
resolution requires taking that lock for all new Writer Agent turns, then
loading all selections with one indexed set query before the credit reservation
and durable turn are created.

Every quota and credit-reservation branch must adopt this universal order; a
conditional user-lock shortcut would recreate a `thread -> user` inversion.

Reading inside the admission transaction still costs a database query. The
performance contract is "one bounded query, not one query per capability," not
"zero extra round trips."

Changing a selection while a turn is running is allowed. It affects only future
admissions. The in-flight turn keeps its snapshot, while the session-contract
hash prevents its older session from being resumed by a later turn.

Selection maintenance must not update every thread's user-visible `updated_at`.
That timestamp orders and groups the Session Rail; touching it would move all
threads into "Today." The contract hash, not a mass thread update, is the
correctness mechanism.

## Resolution and the immutable turn snapshot

`ResolveTurnCapabilities` is a pure policy calculation around catalog and
database inputs. It runs in admission after ownership, entitlement, canonical
document, and source selection have been validated, but before credits are
reserved.

Inputs:

```text
actor uid
thread/project identity
catalog release and local asset health
user selection/profile revision
resolved account entitlements
deployment rollout and emergency denies
turn request flags and optional reviewed recipe reference
```

Output:

```go
type ResolvedTurnCapabilities struct {
    CatalogRelease      string
    ProfileRevision     uint64
    Effective           []ResolvedCapability
    Unavailable         []UnavailableCapability
    SkillNames          []string
    OfferedToolNames    []string
    ActionGrants        []string
    ResearchBudget      *ExternalResearchBudget
    ScenarioRecipe      *ResolvedScenarioRecipe
    StandingContractHash string
    SnapshotHash        string
}
```

The serialized snapshot stored on `w_workagent_turn` contains only stable keys,
versions/digests, grants, limits, release/profile revisions, and hashes. It
contains no prompt, custom instruction, query, page text, credentials, or
personal source content.

Snapshot serialization is versioned. Missing fields, unknown versions, JSON
parse failures, or a hash mismatch fail closed before spawn or replay; they are
not treated as an empty capability set.

Detailed user-facing usage and evidence remain in message metadata and are
deleted with the task. The soft-deleted turn retains only the minimal
capability/cost audit used for quota, reliability, and abuse analysis.

### Resolution order

1. Load and validate the local catalog release.
2. Apply catalog defaults to user-scoped capabilities.
3. Apply explicit user selection rows.
4. Apply entitlements and deployment/admin denies. Selection may remain true
   while effective access is locked, so an upgrade can restore it.
5. Re-resolve the optional recipe key/version/digest and verify every required
   input and capability. Recipe defaults do not mutate the policy result.
6. Apply turn-scoped requests. `externalResearch` is false when omitted and can
   become true only for that turn.
7. Reject an explicitly requested but unavailable turn capability before
   billing. Do not silently continue without it.
8. Build action grants independently from skill selection and recipe content.
9. Derive the exact qualified skill names, MCP servers, and tool names.
10. Compute the standing session contract and the complete snapshot hash.
11. Persist the snapshot atomically with the processing message, reservation,
    and durable turn.

### More than three consumers

The original "one resolution, three consumers" rule is directionally right but
too narrow. All of these consumers must receive projections of the same object:

1. CLI plugin, skill, MCP, and allowed-tool options;
2. `PreToolUse`, `UserPromptExpansion`, and provider-adapter policy;
3. the system-prompt builder: its stable base remains capability-agnostic and
   any standing-capability projection comes from this snapshot;
4. `RunRequest` passed from API executor to `Runner`;
5. idempotency/replay conflict validation;
6. live semantic events and completion warnings;
7. durable turn audit and message metadata;
8. API/message projection shown to the writer.

No consumer re-resolves user settings after admission.

Per-turn external research is represented by the exposed MCP tool descriptions,
semantic events, and turn context, not by mutating the standing system prompt.
This keeps the base prompt truthful without making a one-turn grant part of the
resumable session contract.

SDK attempt retry, session-not-found retry, and an awaiting-input continuation
of the same durable turn reuse the original snapshot. An explicit new turn,
including a user-triggered retry after a terminal result, resolves again.
Steering an in-flight turn may narrow or clarify intent but cannot add a grant.

### Idempotency

The current replay path compares the canonical document, prompt, and selected
files. It must also compare the requested turn capability flags and recipe
reference.

Reusing an idempotency key with a different `externalResearch` value returns an
idempotency conflict. Changing recipe key/version/digest under the same key does
the same. A selection change after the first admission does not change the
admitted turn or its replay response.

Persist a canonical request fingerprint over the exact prompt representation,
sorted resolved source identities, canonical document tuple, explicit per-turn
request flags, and the reviewed recipe reference. Ambient account selections,
entitlements, rollout state, and defaults are deliberately excluded: replay
loads and validates the already-persisted snapshot rather than re-resolving
today's policy. A request fingerprint or snapshot hash mismatch fails closed.

## Session contract and capability changes

Clearing `agent_session_id` for every thread is not a sufficient fence:

- an in-flight result can write the older session ID back afterward;
- a skill content/version change has no user toggle event;
- entitlement, deployment policy, and defaults can change the effective set;
- bulk thread updates would disturb Session Rail ordering.

Use a unified `agent_session_contract_hash`. Its canonical input includes:

```text
actor identity
normalized mode, tone, language, effective model/tier
custom instruction and applied preset content/version
effective session-affecting capability key/version/content digest
future project/task standing-instruction versions
runtime prompt-contract version
```

It excludes the prompt, selected sources, canonical document, recipe reference,
and per-turn external-research grant. If recipe application separately changes
the thread's standing instruction through a reviewed preset action, that copied
instruction and preset provenance are already included by their normal rule.

At admission:

- resume only when `agent_session_id` is non-empty and the stored contract hash
  equals the newly resolved standing hash;
- otherwise start fresh and log the reset reason without touching user-visible
  thread ordering.

At successful persistence, write the returned session ID and the snapshot's
standing contract hash together under the existing fencing/transaction rules.
If an older in-flight turn finishes after the user changes selections, it may
store its own hash, but the next admission computes the newer hash and refuses
to resume it.

Existing eager clearing for a same-thread settings update may remain as cleanup,
but hash comparison is the correctness boundary.

Disabling a skill affects future behavior only. It never removes document
revisions or other output the skill already helped create.

## HTTP and frontend contracts

### Catalog and selection

```http
GET /api/tools/writer-agent/capabilities
```

```json
{
  "catalogVersion": "sha256:...",
  "selectionVersion": 7,
  "items": [
    {
      "key": "writer.skill.source-grounded-review",
      "kind": "skill",
      "category": "sources",
      "name": "Source-grounded review",
      "description": "Review claims against sources selected for the turn.",
      "defaultEnabled": false,
      "selected": true,
      "effective": true,
      "selectionSource": "user",
      "requiredEntitlements": [],
      "availability": "available",
      "accessSummary": ["Reads selected task sources", "May create a new document revision"]
    }
  ],
  "perTurn": {
    "externalResearch": {
      "available": true,
      "maxSearchCalls": 5,
      "maxPageReads": 8
    }
  }
}
```

Availability is one of `available`, `locked`, `disabled_by_policy`,
`degraded`, or `unavailable`. A reason code accompanies every non-available
state.

```http
PUT /api/tools/writer-agent/capabilities/:key
Content-Type: application/json

{
  "enabled": true,
  "expectedSelectionVersion": 7
}
```

The mutation accepts only a known user-scoped key and returns the new selection
version plus the server's final item. Expected-version mismatch is
`CAPABILITY_VERSION_CONFLICT`. A locked capability may retain a prior
selection, but a new enable request returns `CAPABILITY_LOCKED`.

The browser never submits an enabled-skill list with a run. The server owns
resolution.

### Turn request

```go
type writerAgentRunRequest struct {
    Prompt            string                  `json:"prompt"`
    SourceFileIDs     *[]writerAgentReference `json:"sourceFileIds"`
    DocumentKey       string                  `json:"documentKey"`
    ExternalResearch bool                    `json:"externalResearch"`
    ScenarioRecipe   *WriterAgentRecipeRef   `json:"scenarioRecipe,omitempty"`
}
```

Omitted and explicit false are equivalent. True grants both search and reading;
there is no state in which page reading remains available while search is off.
An omitted recipe means an ordinary turn. A supplied recipe is a provenance and
contract assertion, not an enabled-capability list; the server re-resolves it
and refuses changed or unavailable requirements.

The Next BFF needs an explicit route for catalog/selection, capability-key
validation, existing auth/no-store behavior, and a small mutation body cap.

### Turn/message projection

The API projects a bounded reader-facing view rather than exposing raw message
or turn JSON:

```json
{
  "capabilityContext": {
    "catalogVersion": "sha256:...",
    "scenarioRecipe": {
      "key": "writer.recipe.citation-fact-check",
      "version": "1.0.0",
      "contentDigest": "sha256:..."
    },
    "effective": [
      {
        "key": "writer.skill.source-grounded-review",
        "version": "1.0.0",
        "name": "Source-grounded review"
      }
    ],
    "externalResearch": {
      "requested": true,
      "used": true,
      "searchesUsed": 3,
      "pagesRead": 2,
      "searchLimit": 5,
      "limitReached": false,
      "incomplete": false
    }
  },
  "warnings": []
}
```

The `context_ready` SSE frame may carry the admitted capability keys and public
limits so the live UI can confirm the server contract. It must not reveal
provider credentials, internal paths, raw policy input, or generated queries.

## Product and interaction design

### Keep the three scopes visible

Do not put every control into the existing Writing setup popover.

- **Account scope:** a responsive Capability Library sheet/dialog, opened from
  "Manage capabilities - N enabled." It applies to all of the user's writing
  sessions and is not disabled merely because the current project is read-only.
- **Thread scope:** writing mode, tone, language, tier, custom instruction, and
  an Instruction Presets entry.
- **Turn scope:** a visible `Web` toggle beside source attachment and writing
  setup in the composer.

If the open manager is URL-addressable, use a dedicated parameter such as
`manage=capabilities|presets`; do not overload document companion panel state.

### Start from a Scenario Recipe

Recipe discovery is a result-oriented surface labelled `Start from a
workflow`, separate from account capability toggles and thread instruction
presets. It may appear in the new-task state and from a composer action, but it
does not become a fourth persistence scope.

Each card and review screen shows:

- the outcome and required inputs;
- the exact first-party recipe version;
- required capabilities and any locked/unavailable dependency;
- suggested writing setup and the standing instruction change, if any;
- whether external research is suggested, what content may leave WriteGo, and
  which provider category receives it;
- turn and provider limits plus the user-visible price/credit contract;
- expected deliverables and what document/revision may be created or changed.

`Use recipe` pre-fills a draft. It never starts a run. Before send, the composer
shows a compact run-contract summary covering selected sources, standing
instruction/preset changes, effective capabilities, external research, output
target, and recipe version. A user can edit the prompt and turn flags; removing
a required input makes the contract incomplete rather than silently changing
the recipe.

This surface borrows WorkBuddy Explore's outcome-first discovery while
preserving WriteGo's server-owned resolver and review-before-run boundary.

### Per-turn Web control

The composer row becomes conceptually:

```text
[Add sources] [Web] [Writing setup: Write - Natural]          [Send]
```

The Web control:

- is a text-labelled toggle button with `aria-pressed`, not a globe icon alone;
- has a visible selected marker, not color-only state;
- exposes at least a 44 by 44 px mobile target;
- says that it applies only to the next turn and shows the search-call limit;
- is disabled while the turn is running, thread context is loading, or the
  actor cannot run the task;
- remains focusable with an explanation when deployment/policy makes it
  unavailable.

First-use disclosure:

> External research may send search terms derived from your prompt, document,
> or attached sources to a search or page-reading provider. Original files are
> not uploaded as files, but generated terms can contain source text.

The server-side default remains false even if the client remembers an unsent
draft state.

State lifecycle:

1. A new turn and a full page refresh default to off.
2. An unsent choice is scoped to the current thread/composer and never leaks
   when switching tasks.
3. `runAgent()` captures it before its first `await`, alongside the prompt,
   document key, and source selection.
4. A pre-admission failure restores the draft and the toggle.
5. Once admission succeeds, the choice is consumed and the composer resets to
   off even if the accepted turn later becomes partial or failed.
6. Retrying a historical turn may prefill its old choice, but the writer sees
   and submits it again as a new turn.

### Capability Library

Stage 1 lists only first-party capabilities actually known to the serving
catalog. Do not fill the surface with unavailable "coming soon" cards.

Each row includes:

- result-oriented name and description;
- category and "By WriteGo" attribution;
- human-readable read/write/network access summary;
- entitlement badge when relevant;
- current on/off state and whether it came from a recommended default or an
  explicit user choice.

Loading failure is different from an empty catalog. Toggle only the selected
row while saving; retain the confirmed state until the server responds. A
version conflict reloads the catalog and announces that another page changed
the setting. Locked rows provide a focusable explanation and a separate plan
link rather than a disabled button that cannot open its tooltip.

All saves and row errors use an `aria-live="polite"` or `role="alert"` path.
Dialogs restore focus on close, trap focus, support Escape, respect safe areas
and reduced motion, and avoid color-only state.

Changing a global capability must not visually reorder the Session Rail.
Capability or single-turn Web state does not belong in the rail.

### Historical turn display

The message footer can show a compact, expandable context:

- `Web access enabled` means the writer granted it;
- `Searched 3 times - Read 2 pages` means structured tool activity proves use;
- `Capabilities - 2` expands the exact effective catalog snapshot;
- a limit or partial-provider warning is visible and never described as
  complete verification.

Persist semantic activities such as `web_search`, `web_read`, and
`web_search_limit_reached`; do not make the UI depend on provider-specific raw
tool names. Show result titles/domains, not generated query text, in ordinary
history.

### Domain-specific result review

The existing document companion area should evolve into four review surfaces:

- **Integrity Report**: findings, severity/uncertainty, method, limitations, and
  items requiring human review;
- **Evidence**: selected sources, immutable web captures, exact citation
  bindings, and incomplete/unverified status;
- **Changes**: an accessible, revision-aware diff with accept/reject controls
  only where the current product policy permits them;
- **Preview / Export**: the final immutable revision and explicit export
  action.

Do not copy a generic `All Files` tree into the primary workflow. WriteGo's
unit of accountability is the document revision plus evidence, not an arbitrary
desktop workspace.

If implementation later uses multiple internal agents, the live UI still shows
semantic stages such as `Reviewing originality`, `Checking citations`, and
`Preparing changes`. It does not show agent-to-agent chatter. All internal
workers share the admitted snapshot and bounded turn budget; exactly one final
result object owns completion status, warnings, revision, and evidence chain.

## External research

### Replace, do not wrap, the native web tools

Before exposing the per-turn control:

1. remove `WebSearch` and `WebFetch` from the base `writerAgentTools()` list;
2. remove their always-on system-prompt claims;
3. make the default hook deny both names;
4. add the research MCP server only when the resolved snapshot grants it;
5. authorize only the exact MCP tool names from that server.

With the flag off, the turn has zero Writer Agent external-research egress. A
disabled search tool plus an enabled generic fetch tool would not meet this
contract.

Use one in-process Go MCP server, alongside the existing citation-evidence
server and still under `WithStrictMcpConfig(true)`:

```text
research.search(query, filters, limit)
  -> [{result_id, title, url, domain, snippet, published_at?}]

research.read(result_id)
  -> {web_read_id, title, canonical_url, captured_at, content_hash, markdown}
```

`result_id` is opaque and turn-scoped. `research.read` accepts only a result
returned by that turn's search. Direct arbitrary-URL reading is deferred; a
user-provided URL can later use a separate explicit importer or an allowlist
derived from URLs in the user's prompt. This sharply reduces model-invented
egress while preserving the core search workflow.

Search snippets are untrusted discovery data and cannot be cited. Page markdown
is also untrusted content: wrap it in a clearly delimited external-content
envelope and tell the model that instructions inside it have no authority.

### Provider boundary

Keep provider selection behind two Go interfaces:

```go
type SearchProvider interface {
    Search(context.Context, SearchRequest) (SearchResponse, error)
    Readiness(context.Context) error
}

type PageReader interface {
    Read(context.Context, PageReadRequest) (PageReadResponse, error)
    Readiness(context.Context) error
}
```

Provider credentials and endpoints live only in private deployment
configuration. Search and reader can be supplied by the same provider or
different providers. Do not expose provider-specific schemas to the model.

Evaluate candidates using search quality, citation metadata, privacy/DPA,
regional availability, latency, rate limits, and current pricing. Exa's
official API exposes search/content retrieval and paid request pricing; Jina's
Reader/Search APIs have API-key, token, and rate-limit behavior. Do not design
around the assumption that either service is permanently free.

### Initial bounded budget

Start with configurable conservative defaults:

| Limit | Initial value |
| --- | ---: |
| Search calls per turn | 5 |
| Results returned per search | 8 |
| Page reads per turn | 8 |
| Query length | 500 Unicode code points |
| Normalized content per page | 30,000 UTF-8 bytes |
| Total page content returned to one turn | 120,000 UTF-8 bytes |
| Search timeout | 12 seconds |
| Page-read timeout | 15 seconds |
| Redirects | 5, revalidated at every hop |

The detached durable-turn layer creates one concurrency-safe budget object
before the first `Runner.Run` call. Every MCP handler for that turn captures
the same object, including parallel calls, an SDK-attempt retry, and the
session-not-found fresh-session retry. The provider adapter atomically reserves
capacity before network I/O, so a sixth call makes no provider request even
across retries. The prompt may describe limits, but only the adapter enforces
them.

Add an account/day external-research limit and a host/provider circuit breaker;
a five-call per-turn cap alone does not prevent many zero-credit
failed/cancelled turns from creating provider cost.

The account/day limiter is a shared atomic reservation checked before each
provider call; it is not reconstructed later from eventually updated usage
aggregates. Concurrent turns for one account therefore cannot all spend the
last remaining allowance.

Cancellation and lease fencing propagate to provider requests. Retry at most
once for explicitly retryable idempotent provider failures, honor
`Retry-After`, and never retry past the turn deadline.

### Network and privacy controls

- Search calls go only to configured provider origins.
- Every page URL and redirect is restricted to public HTTP(S); reject
  loopback, private, link-local, credential-bearing, oversized, and malformed
  targets and protect against DNS rebinding.
- Page reading uses a dedicated Go HTTP transport that ignores ambient proxy
  settings, resolves and validates every dial target, pins the validated
  address for that connection, and sends every redirect back through the same
  validation.
- The reader accepts no caller-controlled headers, request body, cookies, or
  tenant credentials.
- Bound compressed and decompressed bytes, content type, redirect count, and
  parsing time.
- Reject obvious credential/token material in generated queries and never
  include raw queries or page bodies in ordinary server logs.
- Do not share a cache of private generated queries across tenants. Public page
  caching, if later added, is keyed by canonical URL plus capture policy and
  has explicit freshness semantics.
- Treat provider results as data, never system instructions.

The disclosure and explicit per-turn grant are required because no redaction
system can guarantee that a model-generated query contains no private fact.

### Failure behavior

- Missing configuration or known provider unavailability makes the capability
  unavailable in the catalog. `externalResearch=true` then fails before credit
  reservation with `EXTERNAL_RESEARCH_UNAVAILABLE`.
- A transient provider failure after admission is a typed tool failure. The
  Agent can continue from attached sources, but its completion must mark
  research incomplete.
- Reaching a cap is non-fatal and produces
  `EXTERNAL_RESEARCH_LIMIT_REACHED`, structured usage, and a visible warning.
- A page that failed to load is not listed as read or cited.
- An emergency deny blocks the next provider call and records that research was
  revoked during the turn.

Do not expose raw provider error text to the browser. Use the existing sanitized
failure path and stable error/warning codes.

### Cost

The first release keeps the configured flat Writer Agent turn price (currently
one standard/pro credit unit per turn) and does not add a per-search user
charge. Record provider calls, returned units/bytes, estimated provider cost,
rate-limit responses, and cap hits separately.

The internal ledger still distinguishes `reserved`, `consumed`, and `refunded`
and attributes model/provider/tool cost and terminal failure reason to the
turn. Do not expose an opaque WorkBuddy-style credit conversion until model
multipliers, failed-turn refunds, rollover/top-up behavior, and idempotent replay
are contractual. If team pooling is introduced later, quota and billing
ownership must support an organization owner without changing the admitted
actor or capability policy owner.

Review pricing after enough data exists for:

- opt-in rate;
- searches and reads per enabled turn;
- search-to-read and read-to-citation conversion;
- provider cost per successful and failed turn;
- account/day cap hits and suspected abuse.

Any future incremental charge requires an explicit price/credit-reservation
contract, not an unannounced addition inside the tool.

## External-source provenance

The current citation collector accepts a task `SourceFileID`, proves a
successful `Read`, and validates an exact quote. A URL or search snippet cannot
enter that contract directly.

External research can launch without claim-level evidence, but the UI must then
label links as web results, not verified citation evidence. The evidence bridge
is a separate phase with this data contract.

### Immutable web capture

Extend the existing source identity rather than pretending a URL is an uploaded
file:

```text
w_workagent_thread_file
  file_source = web
  immutable normalized Markdown stored under uploads/web/<snapshot-uuid>.md
        |
        | 1:1 by thread_file_id
        v
w_workagent_web_source_snapshot
  uuid
  uid
  thread_id
  thread_file_id
  original_url
  canonical_url
  title
  publisher
  published_at
  captured_at
  provider
  content_hash
```

The companion row is metadata; the immutable source bytes and existing
`SourceFileID` remain in the thread-file aggregate. No foreign key is added.

The frontend `AgentFile` source union expands from `upload | output` to include
`web`. Its web projection carries only safe provenance fields such as title,
canonical URL, publisher, capture time, and source ID; it never embeds captured
page content in the general thread payload.

`research.read` stages and commits a bounded capture under the same
owner/thread, workspace-quota, filesystem-reconciliation, and deletion rules as
other sources. It returns the resulting source identity and records an internal
successful source-read observation. Identical `(canonical_url, content_hash)`
inside one thread may reuse the existing immutable capture; changed content
creates a new snapshot.

Only a successful read capture can be selected for a later turn or passed to
the citation-evidence collector. The final citation still validates:

```text
exact captured source version
    -> successful bounded read result
    -> exact quote
    -> exact generated claim
    -> exact immutable document revision
```

Citation evidence version 2 adds `sourceKind=web`, the canonical URL, capture
time, and web-source identity while retaining `sourceFileId`. Deleting a web
source retains only the same bounded evidence tombstone needed to explain an
existing citation: display name/title, canonical URL, capture time, exact
excerpt, and hashes. It does not retain the whole page in message metadata.

"Current" means no newer captured content for the same canonical URL is known
inside the task. It does not claim the live page is unchanged. A later fetch
with different content creates a new snapshot and can mark older evidence as
superseded, not false.

The existing message metadata limit is 56 KiB. Capability summaries and web
citation tombstones share that bound; retain them deterministically and store
full captured content only as the immutable source file.

## Capability Library, instruction presets, and preferences

### Stage 1: first-party Capability Library

Everything shown is authored, reviewed, versioned, and shipped by WriteGo.
Users enable an already deployed capability; they do not install code.

This provides the requested discovery surface without implying an ecosystem,
ratings, third-party submissions, or automatic updates.

### Presets are not skills

Reusable instruction presets deliver most of the safe personalization value:

- official presets are versioned in code and read-only;
- private presets are owned by one user;
- a preset is plain text within the same 2,000-code-point limit as
  `customPrompt`;
- it cannot declare a tool, grant, manifest, hook, script, or network access;
- there is no community publish/share surface in the MVP.

The server's Unicode code-point count is authoritative. The browser must use
code-point iteration rather than JavaScript UTF-16 `.length`, so emoji and
supplementary characters are not rejected earlier than the server.

The library uses `WriteGo presets` and `My presets` tabs. Applying a preset:

1. previews and confirms replacement when `customPrompt` is non-empty;
2. copies the exact text into the thread rather than maintaining a mutable live
   reference;
3. stores preset ID/version/name/source/content-hash as provenance;
4. marks the thread `Modified` when the copied text no longer matches the
   preset hash;
5. does not change existing threads when the preset is later edited/deleted.

Applying to an existing thread persists immediately through the thread settings
API and follows its active-turn/version rules. Applying before a new thread
exists becomes part of thread creation. Other unsaved writing-setup edits must
either persist immediately or be stored as a thread-scoped setup draft; merely
holding them in component state and losing them on navigation is not acceptable.

Suggested private-preset API:

```text
GET/POST   /api/tools/writer-agent/instruction-presets
PUT/DELETE /api/tools/writer-agent/instruction-presets/:presetId
```

### Writer preferences, not implicit memory

Long-term personalization is later work and is not inferred from full
conversations. If shipped, it is an opt-in structured profile limited to fields
such as:

```text
language
audience
tone
formatting conventions
citation style
```

It excludes document or paper bodies, student/author identity, detector scores,
source quotations, uploaded-file content, and generated evidence. Writers can
inspect, edit, delete individual values, and clear the profile. Each value has
origin and update time; each admitted turn that uses preferences stores the
profile version/content hash so a later edit cannot rewrite history. Future
organization policy may disable preferences or allow only specified fields.

This intentionally does not copy WorkBuddy's default conversation-derived
memory. It comes after detector trust surfaces, the base Writer Agent, and
durable audit.

## WorkBuddy benchmark and design consequences

The standalone
[WorkBuddy competitive analysis](./workbuddy-competitive-analysis.md)
separates official facts, analysis, and unresolved questions as of 2026-07-27.
Its most relevant design consequences are:

| WorkBuddy signal | Adopt in WriteGo | Do not copy |
| --- | --- | --- |
| Persistent Task and Results views | Durable semantic stages plus Integrity Report, Evidence, Changes, Preview/Export | Generic desktop file manager |
| Skill, Expert, Expert Group, Explore layers | Capability, text-only preset/profile, and first-party Scenario Recipe | User-installed executable experts or user-facing multi-agent chatter |
| Skill discovery and risk labels | First-party Capability Library with read/write/network/data-destination summaries | Community executable skills, agent-generated installation, auto-install |
| Default/Full Access permission modes | Default-deny and target-specific confirmation | Any user-selectable host or tenant policy bypass |
| Connectors | Visible health and separately scoped read/write/send operations | `connected` means automatically offered or authorized |
| Previewable deliverables and source checking | Domain result review and immutable evidence binding | Treating a preview or live URL as claim-level evidence |
| Memory | Later opt-in structured writer preferences | Default learning from entire conversations/documents |
| Team shared pool and enterprise deployment | Future organization billing/policy ownership and deployment discovery | Current prices, opaque credits, or unverified compliance claims |

WorkBuddy's public product is a personal desktop execution environment, while
WriteGo currently starts tenant turns inside a shared server process without a
tenant OS/container boundary. That difference is decisive: the benchmark
strengthens the existing first-party, prompt-only, snapshot-driven design
instead of justifying executable extensions.

The public WorkBuddy material does not establish immutable page capture or
claim/source/revision binding. WriteGo's external-source evidence contract
therefore remains a meaningful product differentiator, not an implementation
detail to relax.

## Future Connector contract

A Connector is never a Capability, Recipe, or Action Grant. Its lifecycle is:

```text
connected -> healthy -> offered -> authorized -> confirmed
```

- `connected` means an encrypted server-side credential reference exists;
- `healthy` means a bounded provider probe succeeded;
- `offered` means the admitted snapshot includes exact typed operations;
- `authorized` means entitlement, deployment/organization policy, user
  selection, operation scope, and turn grant all permit the call;
- `confirmed` is required for the exact future write/send/overwrite/delete
  target.

Read, write, send, overwrite, and delete are distinct operation keys. The
thread workspace, prompt, snapshot, event stream, and logs never receive an
OAuth refresh token, API key, cookie, or connector secret. A snapshot may store
only an opaque connector ID, provider/version, allowed operation scopes, and
policy hash. First-use disclosure names the external data recipient, content
category, retention contract, and revocation path.

Connector availability is deferred until the current third-party isolation and
organization-ownership questions are resolved. External research remains a
narrow first-party provider boundary, not the first generic connector.

## Agent-Reach assessment

The relevant upstream is
[`Panniantong/Agent-Reach`](https://github.com/Panniantong/Agent-Reach), not
`jonnyquan/Agent-Reach`. Its current documentation describes Agent-Reach as a
local installer/selector/health checker whose agent calls upstream CLI tools
directly, including tools such as `twitter-cli`, `yt-dlp`, `gh`, and
`mcporter`. Some supported platforms use machine-local cookies and explicitly
carry account-ban and credential risk.

That is an appropriate personal-workstation model and not the Writer Agent
runtime contract:

- it assumes shell execution;
- it installs and updates tools on the machine;
- it stores human credentials/cookies locally;
- it exposes a much broader network surface than writing research needs.

Do not install it into the Writer Agent process and do not make it a committed
roadmap stage. Its channel inventory may inform provider research. If a future
connector needs Python/CLI dependencies or a separately isolated network
identity, build a narrow sidecar with a WriteGo-owned HTTP contract and its own
unprivileged user, filesystem, egress allowlist, quotas, and credentials. A Go
provider interface alone is enough to support multiple ordinary search
providers and does not justify a sidecar by itself.

Start with public web search and page reading. Cookie-bearing social-platform
readers remain a separate product and legal/security review.

## Failure, audit, and observability

### Admission failures

These fail before credit reservation:

- unknown enabled capability on an old node;
- catalog/plugin digest mismatch;
- unknown, changed, or unsatisfied Scenario Recipe;
- explicitly requested external research unavailable;
- entitlement or deployment policy refusal;
- invalid capability/profile version.

No failure is silently converted to a smaller turn contract.

### Runtime audit

For every turn, record:

- catalog release, profile revision, snapshot/standing hashes;
- applied recipe key, version, and content digest;
- effective capability keys and implementation versions;
- action grants;
- action-confirmation ID/status and sanitized target class when one exists;
- external-research requested/used state;
- search/read/success/failure/cap-hit counts;
- provider latency, returned byte/unit counts, and estimated cost;
- emergency revocation and stable warning/error codes.

Ordinary logs contain turn ID, capability/tool semantic action, status, count,
latency, and sanitized target domain. They do not contain full prompts, custom
instructions, generated queries, page content, exact source excerpts,
credentials, or provider secrets.

Add metrics for catalog skew, plugin readiness, capability enable/disable
rates, session contract resets, external-research conversion, provider errors,
cost, and cap hits.

`GET /api/ready` checks local configuration/asset integrity without consuming a
paid provider request. A separate cached operational probe/metric can test the
provider. The Capability Library reports configuration readiness, while a
transient runtime outage becomes a warning/tool failure.

## Delivery plan

### Phase 0: baseline truth and mechanism canary

- Capture the actual CLI arguments and hook events for every currently offered
  tool.
- Replace the mutable local SDK `replace` in release builds with a pinned or
  vendored audited revision and record the exact compatible CLI version.
- Verify native `WebSearch` against the configured gateway, only to establish
  the baseline; it is still removed from the target design.
- Load one throwaway prompt-only plugin with `--setting-sources=` still empty.
- Reject startup if Writer Agent setting sources are non-empty, rather than
  relying on development/production YAML values.
- Verify exact qualified skill allowlisting, omitted-skill denial, direct slash
  behavior, and host settings isolation.
- Prove plugin hooks/settings/other components cannot enter the approved
  package.
- Reconcile the runtime, roadmap, and this document's current-tool claims.

Exit: the team has evidence for packaged/offered/authorized/operational state,
and a disabled skill cannot enter a turn through either model or user
invocation.

### Phase 1: catalog, resolver, snapshot, and session fence

- Add the typed Writer Agent catalog and restrictive package validator.
- Add one no-tool-dependency prompt-only platform skill.
- Add profile/selection persistence and optimistic API.
- Resolve and persist one immutable turn snapshot.
- Drive spawn, hooks, prompt projection, idempotency, events, and API projection
  from it.
- Add `agent_session_contract_hash` and race tests.
- Add readiness and monotonic emergency deny.

Exit: an asset mismatch fails before billing; spawn, policy, audit, and UI cannot
disagree about the turn contract.

### Phase 2: Capability Library, presets, and Scenario Recipes

- Build the account-scoped first-party catalog/toggle UI.
- Add official and private instruction presets.
- Add thread preset provenance and modified state.
- Add the code-owned recipe catalog/API, outcome-first discovery, dependency
  checks, review-before-run, and composer run-contract summary.
- Persist recipe key/version/digest in request fingerprint, turn snapshot,
  audit, and historical message projection.
- Complete BFF, localization, accessibility, and concurrency handling.

Exit: a user can manage first-party behavior without any third-party execution
surface, apply a recipe without granting access or auto-running it, and keep
changes limited to future turns.

### Phase 3: external-research MVP

- Remove native `WebSearch`/`WebFetch` and static prompt claims.
- Add `research.search`/`research.read` provider adapters and MCP tools.
- Add the one-turn composer grant, immutable request capture, limits,
  cancellation, SSRF/redirect controls, privacy disclosure, cost telemetry,
  warnings, and account/day abuse limit.

Exit: flag off means zero external-research tools/egress; flag on makes every
call attributable, bounded, cancellable, and visible.

### Phase 4: immutable web sources and citation evidence

- Add web-source capture rows/files and lifecycle handling.
- Bridge successful page reads to `SourceFileID`.
- Add citation evidence v2, URL/capture provenance, stale/superseded behavior,
  source UI, and deletion tombstones.

Exit: search snippets cannot become citations, and every web-backed claim
evidence item points to an immutable captured page and exact document revision.

### Phase 5: first-party WriteGo tools

- Adapt AI detection, writing-integrity review, grammar/style, scholarly search,
  citation, and export operations through typed Writer Agent tools.
- Reuse existing `server/service/platform` behavior where appropriate rather
  than duplicating business logic.
- Define action grants and cost/idempotency contracts per tool.

### Phase 6: project policy and collaboration

- Add project sources/standing instructions and a real ACL boundary.
- Define actor, policy owner, billing owner, and attribution for shared tasks.
- Design separate task/project scoped policy inheritance if product evidence
  justifies it.
- Add explicit organization controls for structured writer preferences before
  enabling that later personalization feature.

### Later, separately approved: sidecars and third-party extensions

Third-party extensions require all of these before a new product design begins:

- per-tenant unprivileged process/container isolation;
- read-only content-addressed mounts;
- default-deny egress and secret isolation;
- CPU, memory, filesystem, process, and wall-time quotas;
- signed, reviewed, version-pinned artifacts;
- declared permissions, user consent, audit, revocation, kill switch, rollback;
- no implicit update to an already-running or resumed session.

Until then, there is no third-party submission, installation, or marketplace.
Generic connectors and remote messaging also require a separate approved
design for credential ownership, operation scopes, confirmation, ingress
identity, replay defense, and organization audit. Start any messaging work with
notifications, approvals, and controlled result links rather than arbitrary
remote file operations.

## Verification matrix

### Catalog and package

- duplicate key, invalid namespace, dependency cycle, unknown tool/grant;
- missing/mismatched skill, digest/version mismatch, symlink;
- every forbidden plugin directory/frontmatter/dynamic-context form;
- multi-node old/new catalog skew;
- `writerAgentPlugin` readiness and pre-admission refusal.
- non-empty Writer Agent setting sources fail startup in every environment.

### Resolver and persistence

- default, explicit on/off, locked entitlement, deployment deny, retired key;
- per-turn external research omitted/false/true/unavailable;
- deterministic ordering and snapshot hashes;
- unknown snapshot version, malformed JSON, missing field, and hash mismatch
  all fail closed;
- request fingerprint covers prompt, resolved sources, document tuple, and
  per-turn flags plus recipe key/version/digest while replay reuses the
  persisted policy snapshot;
- same-value update idempotency and selection-version conflict;
- update/admission lock ordering and concurrent toggle vs running turn;
- user/account deletion and turn/thread retention split.

### Spawn and authorization

- exact plugin-qualified skill only;
- direct slash invocation cannot bypass selection;
- no plugin when no skill is effective;
- external research absent means no native or MCP web tool;
- unknown skill/tool/MCP alias denied by default;
- the system prompt stays capability-agnostic, any standing projection and
  `RunRequest` match the snapshot;
- emergency deny narrows but never expands access.

### Session and idempotency

- skill on/off, content digest, tier/entitlement, preset, and runtime prompt
  version changes cause a fresh session;
- per-turn external research does not;
- an older in-flight turn cannot make its session resumable under a newer
  selection;
- provider session-not-found retry keeps the same snapshot;
- parallel calls and every SDK/session retry share one turn budget, and an
  exhausted budget produces no provider request;
- same idempotency key plus different external-research flag conflicts.

### Recipes, confirmations, preferences, and future connectors

- recipe key/version/digest validation, deterministic catalog digest, and
  rolling-node skew;
- applying a recipe does not run, enable a capability, save a thread setting,
  add a tool, or grant external research;
- unavailable required inputs/capabilities block submission with a stable
  reason, and a changed reviewed recipe requires review again;
- recipe provenance survives history while later catalog updates affect only
  new turns;
- confirmations reject wrong actor/tenant/target/revision/snapshot/preview,
  expiry, replay, and idempotency mismatch;
- a confirmation never overrides entitlement, organization/deployment deny,
  unoffered tools, or unsupported operations;
- package scans and readiness/health probes produce no grant;
- a connected/healthy Connector is not offered or authorized automatically,
  and read/write/send/overwrite/delete remain independent;
- connector secrets never enter workspace, snapshot, prompt, events, or logs;
- writer preferences are opt-in, field-allowlisted, individually removable,
  content-excluding, and version/hash bound when used.

### External research security

- query/result/read/byte/call/time limits;
- cancellation, deadline, fencing, retry, rate limit, and circuit breaker;
- public URL checks, every redirect hop, DNS rebinding, credentials in URL,
  loopback/private/link-local targets, decompression bomb, invalid content type;
- untrusted page-instruction enclosure;
- secret-like query refusal and absence of raw query/page text in logs;
- account/day cap and provider-cost accounting on failed/cancelled turns.

### Web-source evidence

- search result alone cannot be cited;
- capture creation/dedup/change, quota, cleanup, reconciliation, deletion;
- exact quote/read/source hash/revision binding;
- stale/superseded and deleted-source tombstone behavior;
- deterministic truncation below the 56 KiB message metadata bound.

### Frontend and BFF

- catalog loading, retry, empty, locked, saving, failure, and version conflict;
- no optimistic false-success state;
- account capability management remains available in a read-only project;
- Web state is thread-scoped before send, captured before async work, restored
  before admission failure, consumed after admission, and off on refresh;
- historical "granted" vs "used" distinction and limit warning;
- capability changes do not change Session Rail ordering;
- preset replacement confirmation, modified state, deletion independence;
- recipe browse/review/apply/edit/cancel, run-contract preview, dependency
  failure, changed-version review, and no auto-run;
- Integrity Report, Evidence, Changes, and Preview/Export expose one accountable
  result while semantic stages do not leak internal multi-agent chatter;
- keyboard, focus, touch target, live-region, localization, and reduced-motion
  behavior;
- route/body/key validation and no-store/auth behavior in the Next BFF.

## Release acceptance criteria

1. With external research off, the CLI receives and the hook authorizes no web
   search or page-read tool, including the current native tools.
2. A selected skill missing from the local release refuses the turn before any
   credit reservation.
3. The exact same resolved snapshot drives spawn, hooks, prompt projection,
   idempotency, durable audit, SSE, and message projection.
4. A platform skill cannot add a tool or action grant through frontmatter,
   plugin components, dynamic context, model invocation, or direct slash input.
5. A capability toggle affects future turns across the user's tasks while
   preserving historical turn snapshots and Session Rail dates.
6. An in-flight turn may finish under its admitted snapshot but cannot make an
   incompatible provider session resumable afterward.
7. External research is explicit, one-turn, default-off, non-sticky after
   admission, and never leaks between task composers.
8. A pre-admission failure restores the draft and external-research choice.
9. Every research call is bounded, cancellable, attributable, sanitized in
   logs, and covered by per-turn and account-level limits.
10. The UI distinguishes access granted, searches performed, pages read, limits
    reached, and incomplete research.
11. No search snippet or live URL is presented as verified citation evidence.
12. A web citation binds an immutable capture, exact excerpt, and exact document
    revision and remains explainable after source deletion.
13. Presets remain text-only, snapshot into the thread, and cannot declare
    permissions or executable behavior.
14. Third-party plugin submission or installation remains unavailable.
15. Applying a Scenario Recipe only creates a reviewed draft; it cannot enable
    capabilities, grant external research, mutate standing settings without a
    separate action, or start a run.
16. A historical recipe-backed turn preserves recipe key/version/digest, and a
    changed recipe cannot run under a previously reviewed reference.
17. There is no `Full Access` path. Package scan, readiness, health,
    capability selection, and confirmation cannot override a stronger deny.
18. A connected future service is not offered or authorized automatically, and
    write/send/overwrite/delete require exact independent policy and
    confirmation.
19. The result experience has one accountable revision/evidence chain even if
    implementation later uses multiple internal workers.
20. Long-term personalization, if enabled in a later release, is opt-in,
    structured, excludes document/student/evidence content, and records the
    exact preference version/hash used by a turn.

## Remaining bounded product choices

The architecture no longer depends on the earlier open questions. The choices
left for implementation review are bounded:

- which no-tool pilot skill best tests the platform package;
- which two Scenario Recipes best test review-before-run without introducing a
  new tool dependency;
- which search/reader provider wins a measured quality/privacy/cost spike;
- the initial account/day external-research allowance;
- the retention period for immutable web captures after a task is deleted.

The defaults in this proposal are: capability entitlements are represented from
day one even if the pilot is available to everyone; platform skills are
per-user; external research is per-turn and included in the normal flat price;
Scenario Recipes are first-party and inert; implicit memory, generic
connectors, task/project capability overrides, and third-party plugins are not
scheduled.

## References

- [WorkBuddy competitive analysis](./workbuddy-competitive-analysis.md)
- [WorkBuddy overview](https://www.workbuddy.ai/docs/zh/workbuddy/Overview)
- [WorkBuddy results](https://www.workbuddy.ai/docs/workbuddy/Results)
- [WorkBuddy Skill Marketplace](https://www.workbuddy.ai/docs/workbuddy/From-Beginner-to-Expert-Guide/Function-Description/Skills-Market)
- [WorkBuddy Explore](https://www.workbuddy.ai/docs/zh/workbuddy/From-Beginner-to-Expert-Guide/Function-Description/Explore)
- [WorkBuddy connectors](https://www.workbuddy.ai/docs/workbuddy/From-Beginner-to-Expert-Guide/Function-Description/Connector)
- [WorkBuddy permission modes](https://www.workbuddy.ai/docs/workbuddy/From-Beginner-to-Expert-Guide/Function-Description/Permission-Modes)
- [WorkBuddy memory](https://www.workbuddy.ai/docs/zh/workbuddy/From-Beginner-to-Expert-Guide/Function-Description/Memory)
- [WorkBuddy privacy policy](https://www.workbuddy.ai/document/privacy-policy)
- [WorkBuddy pricing](https://www.workbuddy.ai/pricing)
- [Claude Code plugins](https://code.claude.com/docs/en/plugins)
- [Claude Code plugin reference](https://code.claude.com/docs/en/plugins-reference)
- [Claude Code skills](https://code.claude.com/docs/en/slash-commands)
- [Agent Skills in the SDK](https://code.claude.com/docs/en/agent-sdk/skills)
- [Claude Code hooks](https://code.claude.com/docs/en/hooks)
- [Agent-Reach](https://github.com/Panniantong/Agent-Reach)
- [Exa search API](https://exa.ai/docs/reference/search)
- [Jina Reader API](https://jina.ai/reader/)
