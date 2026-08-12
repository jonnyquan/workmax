#!/usr/bin/env bash
#
# End-to-end smoke for the L2 tool loop and its approval flow, on the real
# thing: a real claude CLI subprocess, a real sidecar, real SQLite, real SSE.
#
# The one piece that is not real is the model — a scripted Anthropic endpoint
# (l2-approval-upstream.mjs) stands in for it, so the harness decides exactly
# which tool the loop calls and when. Nothing leaves the machine.
#
# Everything below was verified by hand before it was written down; two of the
# assertions exist because the hand run failed them (see FINDINGS at the end).
#
# Usage:
#   WORKMAX_TEST_CLAUDE_CLI=$HOME/.local/share/claude/versions/<ver> \
#     ./desktop/scripts/smoke-l2-approvals.sh
#
#   ./desktop/scripts/smoke-l2-approvals.sh --cli <path> [--binary <path>]
#                                           [--skip-timeout] [--keep]
#
# Skips (exit 0) when no CLI is named, the same env gate as
# TestIntegration_CLIEndpointInventory: CI has no claude binary.
#
# It is safe to run on a machine you use. The data dir is a throwaway one and so
# is the Keychain namespace: the run sets WORKMAX_KEYCHAIN_SERVICE, deletes what
# it created on the way out, and asserts the real service's entries are
# unchanged. Until that variable existed, the local-model key was keyed by uid
# under one fixed service name, and every fresh data dir hands out the same
# first uid — so a run overwrote whatever key the user had stored.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
UPSTREAM="$REPO_ROOT/desktop/scripts/l2-approval-upstream.mjs"

cli="${WORKMAX_TEST_CLAUDE_CLI:-}"
binary="$REPO_ROOT/desktop/wails/bin/workmax-desktop"
skip_timeout=0
keep=0

usage() {
  cat >&2 <<'USAGE'
Usage:
  smoke-l2-approvals.sh [--cli <claude binary>] [--binary <workmax-desktop>]
                        [--skip-timeout] [--keep]

Options:
  --cli           Real claude CLI binary. Defaults to $WORKMAX_TEST_CLAUDE_CLI.
                  Without one the smoke skips (exit 0), so CI stays green.
  --binary        workmax-desktop to drive. Defaults to
                  desktop/wails/bin/workmax-desktop; built if missing.
  --skip-timeout  Skip the unanswered-approval case, which costs the sidecar's
                  full 120s approval timeout.
  --keep          Keep the temporary data dir and logs, and print the path.
USAGE
}

while [ $# -gt 0 ]; do
  case "$1" in
    --cli) cli="${2:-}"; shift 2 ;;
    --binary) binary="${2:-}"; shift 2 ;;
    --skip-timeout) skip_timeout=1; shift ;;
    --keep) keep=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "smoke-l2-approvals.sh: unknown argument $1" >&2; usage; exit 2 ;;
  esac
done

if [ -z "$cli" ]; then
  echo "smoke-l2-approvals.sh: no claude CLI named (--cli or WORKMAX_TEST_CLAUDE_CLI); skipping"
  exit 0
fi
if [ ! -x "$cli" ]; then
  echo "smoke-l2-approvals.sh: --cli $cli is not an executable file" >&2
  exit 2
fi
for tool in node curl uuidgen; do
  command -v "$tool" >/dev/null 2>&1 || { echo "smoke-l2-approvals.sh: $tool is required" >&2; exit 2; }
done
if [ ! -f "$UPSTREAM" ]; then
  echo "smoke-l2-approvals.sh: missing $UPSTREAM" >&2
  exit 2
fi
if [ ! -x "$binary" ]; then
  echo "==> building $binary"
  ( cd "$REPO_ROOT/desktop/wails" \
    && GOOS="$(go env GOHOSTOS)" GOARCH="$(go env GOHOSTARCH)" CGO_ENABLED=1 \
       go build -tags desktop -o "$binary" . ) || {
    echo "smoke-l2-approvals.sh: build failed" >&2; exit 2; }
fi

work="$(mktemp -d "${TMPDIR:-/tmp}/workmax-l2-approvals.XXXXXX")"
token="l2smoke$(uuidgen | tr -d '-' | tr 'A-Z' 'a-z')"
# The Keychain namespace this run owns, per-run so two runs cannot collide
# either. Shape matches the sidecar's validator: [A-Za-z0-9][A-Za-z0-9._-]*.
keychain_service="ai.workmax.desktop.smoke-l2.$(uuidgen | tr -d '-' | tr 'A-Z' 'a-z' | cut -c1-12)"
real_keychain_service="ai.workmax.desktop"
real_keychain_accounts=("session" "local-model-api-key:4611686018427387904")
upstream_pid=""
sidecar_pid=""
base=""
failures=0
checks=0

# Attributes only — never `-w`. The modification date proves an entry was not
# rewritten, and a smoke test has no business reading the user's real secret.
keychain_snapshot() {
  local out=""
  command -v security >/dev/null 2>&1 || { printf 'no-security'; return 0; }
  for account in "${real_keychain_accounts[@]}"; do
    out="$out$(security find-generic-password -s "$real_keychain_service" -a "$account" 2>&1 | grep -E '"(acct|mdat|cdat|svce)"|could not be found' | tr -d ' ')"
  done
  printf '%s' "$out"
}

# Delete every entry this run created, one at a time: delete-generic-password
# removes a single match and exits 44 once nothing is left.
purge_run_keychain() {
  command -v security >/dev/null 2>&1 || return 0
  local n=0
  while [ "$n" -lt 64 ]; do
    security delete-generic-password -s "$keychain_service" >/dev/null 2>&1 || break
    n=$((n + 1))
  done
  [ "$n" -gt 0 ] && echo "==> removed $n Keychain entries under $keychain_service"
  return 0
}

cleanup() {
  [ -n "$sidecar_pid" ] && kill "$sidecar_pid" 2>/dev/null
  [ -n "$upstream_pid" ] && kill "$upstream_pid" 2>/dev/null
  wait 2>/dev/null
  purge_run_keychain
  if [ "$keep" = "1" ]; then
    echo "kept: $work"
  else
    rm -rf "$work"
  fi
}
trap cleanup EXIT

ok() { checks=$((checks + 1)); printf 'ok   - %s\n' "$1"; }
fail() {
  checks=$((checks + 1)); failures=$((failures + 1))
  printf 'FAIL - %s\n' "$1" >&2
  [ -n "${2:-}" ] && printf '       %s\n' "$2" >&2
  return 0
}

# --- harness ---------------------------------------------------------------

start_upstream() {
  node "$UPSTREAM" --log "$work/upstream.log" > "$work/upstream.out" 2>&1 &
  upstream_pid=$!
  for _ in $(seq 1 50); do
    if grep -q '^upstream: ' "$work/upstream.out" 2>/dev/null; then
      upstream_url="$(sed 's/^upstream: //' "$work/upstream.out" | tr -d '\r\n')"
      return 0
    fi
    sleep 0.2
  done
  echo "smoke-l2-approvals.sh: the scripted upstream never came up" >&2
  exit 1
}

# start_sidecar <approvals:0|1> — one sidecar at a time (SQLite pid lock).
start_sidecar() {
  local approvals="$1" log="$work/sidecar-$1.log" env_approvals=""
  [ "$approvals" = "1" ] && env_approvals="WORKMAX_L2_APPROVALS=1"
  env WORKMAX_DESKTOP_DATA_DIR="$work/data" \
      WORKMAX_KEYCHAIN_SERVICE="$keychain_service" \
      WORKMAX_LOCAL_TOKEN="$token" \
      WORKMAX_CLAUDE_CLI_PATH="$cli" \
      ${env_approvals:+"$env_approvals"} \
      "$binary" --serve-only > "$log" 2>&1 &
  sidecar_pid=$!
  local port=""
  for _ in $(seq 1 100); do
    port="$(grep -o 'bound to 127\.0\.0\.1:[0-9]*' "$log" 2>/dev/null | grep -o '[0-9]*$' | head -1)"
    [ -n "$port" ] && break
    sleep 0.2
  done
  if [ -z "$port" ]; then
    echo "smoke-l2-approvals.sh: the sidecar never bound a port; see $log" >&2
    exit 1
  fi
  base="http://127.0.0.1:$port"
  # The local profile: the tool loop only runs for preferred_route=local with
  # protocol=anthropic_compatible, pointed at the scripted upstream.
  api PUT /settings/model-route \
    "{\"preferred_route\":\"local\",\"local\":{\"protocol\":\"anthropic_compatible\",\"base_url\":\"$upstream_url\",\"model_id\":\"l2-approval-smoke\",\"api_key\":\"sk-smoke\"}}" \
    > "$work/model-route.json"
}

stop_sidecar() {
  [ -n "$sidecar_pid" ] || return 0
  kill "$sidecar_pid" 2>/dev/null
  for _ in $(seq 1 50); do
    kill -0 "$sidecar_pid" 2>/dev/null || break
    sleep 0.2
  done
  kill -9 "$sidecar_pid" 2>/dev/null
  sidecar_pid=""
}

# api <method> <path> [body] — prints the response body.
api() {
  local method="$1" path="$2" body="${3:-}"
  if [ -n "$body" ]; then
    curl -sS -X "$method" "$base$path" -H "X-Local-Token: $token" \
      -H 'Content-Type: application/json' -d "$body"
  else
    curl -sS -X "$method" "$base$path" -H "X-Local-Token: $token"
  fi
}

new_thread() {
  local uuid
  uuid="$(uuidgen | tr 'A-Z' 'a-z')"
  api PUT "/agent/threads/$uuid" '{"name":"L2 approval smoke","agent_mode":"ppt"}' > /dev/null
  printf '%s' "$uuid"
}

workspace_of() { printf '%s' "$work/data/agent_workspace/thread_$1"; }

# run_turn <label> <thread> <text> <decision|none>
#
# Streams the turn into $work/<label>.sse. With a decision, a watcher answers
# the first approval_request that appears on the stream — which is the whole
# point: the answer travels the renderer's route (POST the approve endpoint
# with the id the frame carried), not a test hook.
run_turn() {
  local label="$1" thread="$2" text="$3" decision="$4"
  local turn out watcher=""
  turn="$(uuidgen | tr 'A-Z' 'a-z')"
  out="$work/$label.sse"
  : > "$out"
  : > "$work/$label.approve"

  if [ "$decision" != "none" ]; then
    (
      id=""
      for _ in $(seq 1 900); do
        id="$(grep -o '"id":"ap-[0-9]*"' "$out" 2>/dev/null | head -1 | sed 's/.*"\(ap-[0-9]*\)".*/\1/')"
        [ -n "$id" ] && break
        sleep 0.2
      done
      [ -n "$id" ] || exit 0
      # Build the payload in a variable, never inline inside "$( … )": a
      # command substitution nested in a double-quoted string re-parses the
      # \" escapes and ships a mangled body, which the endpoint correctly
      # refuses as invalid_approval_body. Cost an hour once; not again.
      payload="{\"approval_id\":\"$id\",\"decision\":\"$decision\"}"
      code="$(curl -sS -o "$work/$label.approve.body" -w '%{http_code}' \
        -X POST "$base/agent/turns/$turn/approve" \
        -H "X-Local-Token: $token" -H 'Content-Type: application/json' \
        -d "$payload")"
      printf '%s %s\n' "$id" "$code" > "$work/$label.approve"
    ) &
    watcher=$!
  fi

  curl -sS -N -X POST "$base/agent/chat" \
    -H "X-Local-Token: $token" -H 'Content-Type: application/json' \
    -d "{\"turn_uuid\":\"$turn\",\"thread_uuid\":\"$thread\",\"user_text\":\"$text\",\"chat_mode\":\"ppt\",\"payload\":{\"stream\":true}}" \
    >> "$out" 2>&1
  [ -n "$watcher" ] && wait "$watcher" 2>/dev/null
  return 0
}

# --- assertions ------------------------------------------------------------

frame_present() { grep -q "^event: $2$" "$work/$1.sse" 2>/dev/null; }
sse_dump() { sed -n '1,40p' "$work/$1.sse" 2>/dev/null | tr '\n' '|'; }

assert_frame() {
  if frame_present "$1" "$2"; then ok "$3"; else fail "$3" "frames: $(sse_dump "$1")"; fi
}
assert_no_frame() {
  if frame_present "$1" "$2"; then fail "$3" "frames: $(sse_dump "$1")"; else ok "$3"; fi
}
assert_file() {
  if [ -f "$1" ]; then ok "$2"; else fail "$2" "missing: $1"; fi
}
assert_no_file() {
  if [ -f "$1" ]; then fail "$2" "present but must not be: $1"; else ok "$2"; fi
}
assert_grep() {
  if grep -q -- "$2" "$1" 2>/dev/null; then ok "$3"; else fail "$3" "not found in $1: $2"; fi
}

# --- the run ---------------------------------------------------------------

echo "==> claude CLI: $cli"
echo "==> workdir:    $work"
echo "==> keychain:   $keychain_service"
keychain_before="$(keychain_snapshot)"
start_upstream
echo "==> upstream:   $upstream_url"
start_sidecar 1
echo "==> sidecar:    $base (approvals ON)"

# 1-2. A write asks, and the answer runs the tool.
thread="$(new_thread)"; ws="$(workspace_of "$thread")"
run_turn once "$thread" "Do it. TOOLPLAN Write $ws/once.txt" allow_once
assert_frame once approval_request "a write tool call raises approval_request"
assert_grep "$work/once.sse" '"name":"Write","target":"once.txt"' \
  "the approval frame names the tool and its target"
assert_grep "$work/once.approve" ' 200' "the approve endpoint accepts the decision"
assert_file "$ws/once.txt" "allow_once runs the tool"
assert_frame once done "an approved turn still reaches done"
# A call has two ends. The CLI reports the second one as a user message
# carrying only the tool_use id, so the pump has to correlate it back to the
# announcement to name it — a result frame without the target would leave the
# work log unable to say WHICH open step just closed.
assert_grep "$work/once.sse" '"name":"Write","target":"once.txt"' \
  "the result frame names the call it closes"
assert_frame once tool_result "an approved call reports back when it finishes"

# 3. Deny: the tool must not run, and the turn must survive the refusal.
thread="$(new_thread)"; ws="$(workspace_of "$thread")"
run_turn deny "$thread" "Do it. TOOLPLAN Write $ws/denied.txt" deny
assert_no_file "$ws/denied.txt" "deny keeps the tool from running"
assert_frame deny tool_denied "deny is narrated on the stream"
assert_frame deny done "a denied turn still reaches done"
# FINDING (fixed): the denial frame carried no target, so the renderer could
# not fold it into the step it settles and every refused file call drew two
# work-log rows for a tool that ran zero times. The renderer-side fold had
# been written and tested against the frame it wished for.
assert_grep "$work/deny.sse" 'tool_denied' "the denial frame exists"
if grep -A1 '^event: tool_denied$' "$work/deny.sse" | grep -q '"target":"denied.txt"'; then
  ok "the denial names the call it refuses"
else
  fail "the denial names the call it refuses" "frames: $(sse_dump deny)"
fi
# FINDING: a refusal comes back through the CLI a SECOND time, as an errored
# tool result. The renderer must treat the already-settled step as settled —
# a bare "failed" would replace the reason the user needs.
if grep -A1 '^event: tool_result$' "$work/deny.sse" | grep -q '"is_error":"true"'; then
  ok "a refused call also reports back as an errored result"
else
  fail "a refused call also reports back as an errored result" "frames: $(sse_dump deny)"
fi

# 4. Session grant: the second turn of the SAME thread must not ask again.
thread="$(new_thread)"; ws="$(workspace_of "$thread")"
run_turn session1 "$thread" "Do it. TOOLPLAN Write $ws/sess1.txt" allow_session
assert_frame session1 approval_request "the first write of a session asks"
run_turn session2 "$thread" "Again. TOOLPLAN Write $ws/sess2.txt" none
assert_no_frame session2 approval_request "allow_session silences the second call"
assert_file "$ws/sess2.txt" "the session-granted call still runs"

# 5. Durable grant: a rule row, and no card on a brand-new thread.
thread="$(new_thread)"; ws="$(workspace_of "$thread")"
mkdir -p "$ws"; printf 'seed\n' > "$ws/always1.txt"
run_turn always1 "$thread" "Do it. TOOLPLAN Edit $ws/always1.txt" allow_always
assert_frame always1 approval_request "the first Edit asks"
thread2="$(new_thread)"; ws2="$(workspace_of "$thread2")"
mkdir -p "$ws2"; printf 'seed\n' > "$ws2/always2.txt"
run_turn always2 "$thread2" "Do it. TOOLPLAN Edit $ws2/always2.txt" none
assert_no_frame always2 approval_request "allow_always silences a different thread"
if command -v sqlite3 >/dev/null 2>&1; then
  # uid = 2^62 + (id - 1), the local-account partition (see localAccountUID).
  uid="$(sqlite3 "$work/data/workagent.db" \
    "SELECT 4611686018427387904 + id - 1 FROM w_desktop_local_account WHERE is_active = 1 ORDER BY id LIMIT 1" 2>/dev/null)"
  rows="$(sqlite3 "$work/data/workagent.db" \
    "SELECT uid || ':' || tool || ':' || decision FROM w_desktop_agent_permission_rule" 2>/dev/null)"
  if [ "$rows" = "$uid:Edit:allow" ]; then
    ok "allow_always stores exactly one rule, for the active uid ($rows)"
  else
    fail "allow_always stores exactly one rule, for the active uid" "want $uid:Edit:allow, got: $rows"
  fi
else
  echo "skip - permission rule row (no sqlite3)"
fi

# 6. Read-only tools never ask.
thread="$(new_thread)"; ws="$(workspace_of "$thread")"
mkdir -p "$ws"; printf 'seed content\n' > "$ws/readme.txt"
run_turn readonly "$thread" "Read it. TOOLPLAN Read $ws/readme.txt" none
assert_no_frame readonly approval_request "a Read never asks"
assert_frame readonly tool_use "the Read still runs"
assert_frame readonly tool_result "and reports back — both ends of every call, not only approved ones"

# 7. FINDING (fixed): the tool SURFACE is enforced by the PreToolUse hook, not
# by WithAllowedTools. Before the fix this Bash call executed and the file
# landed — in pre-approved mode, which is the shipping default.
thread="$(new_thread)"
run_turn surface "$thread" "Run it. TOOLPLAN Bash $work/bash-escape.txt" none
assert_no_file "$work/bash-escape.txt" "a tool outside the surface does not execute"
assert_frame surface tool_denied "an out-of-surface tool is narrated as denied"

# 8. The workspace boundary still holds under approvals.
thread="$(new_thread)"
run_turn escape "$thread" "Write it. TOOLPLAN Write $work/escape-proof.txt" none
assert_no_file "$work/escape-proof.txt" "a write outside the workspace does not land"
assert_frame escape tool_denied "the escape is narrated as denied"
assert_no_frame escape approval_request "an escape is refused outright, never offered for approval"

# 9. An unanswered card denies on the sidecar's timeout.
if [ "$skip_timeout" = "1" ]; then
  echo "skip - unanswered approval times out (--skip-timeout)"
else
  thread="$(new_thread)"; ws="$(workspace_of "$thread")"
  echo "==> waiting out the 120s approval timeout"
  started="$(date +%s)"
  run_turn timeout "$thread" "Do it. TOOLPLAN Write $ws/timeout.txt" none
  elapsed=$(( $(date +%s) - started ))
  assert_frame timeout approval_request "the unanswered call asks"
  assert_no_file "$ws/timeout.txt" "nothing ran while the card waited"
  assert_frame timeout tool_denied "an unanswered card is denied"
  assert_frame timeout done "the turn survives the timeout"
  if [ "$elapsed" -ge 110 ] && [ "$elapsed" -le 200 ]; then
    ok "the timeout took the sidecar's 120s (${elapsed}s)"
  else
    fail "the timeout took the sidecar's 120s" "elapsed ${elapsed}s"
  fi
  assert_grep "$work/timeout.sse" ': keepalive' "the stream is kept alive while the card waits"
fi

# 10. With the flag off, the loop is back to pre-approved mode.
stop_sidecar
start_sidecar 0
echo "==> sidecar:    $base (approvals OFF)"
thread="$(new_thread)"; ws="$(workspace_of "$thread")"
run_turn bypass "$thread" "Do it. TOOLPLAN Write $ws/bypass.txt" none
assert_no_frame bypass approval_request "no approvals flag means no card"
assert_file "$ws/bypass.txt" "pre-approved mode still runs the tool"
code="$(curl -sS -o /dev/null -w '%{http_code}' -X POST \
  "$base/agent/turns/$(uuidgen | tr 'A-Z' 'a-z')/approve" \
  -H "X-Local-Token: $token" -H 'Content-Type: application/json' \
  -d '{"approval_id":"ap-1","decision":"allow_once"}')"
if [ "$code" = "503" ]; then
  ok "the approve endpoint reports 503 when approvals are off"
else
  fail "the approve endpoint reports 503 when approvals are off" "got HTTP $code"
fi
# The surface holds in pre-approved mode too — that is where the hole was.
thread="$(new_thread)"
run_turn surface_off "$thread" "Run it. TOOLPLAN Bash $work/bash-escape-off.txt" none
assert_no_file "$work/bash-escape-off.txt" "the surface holds in pre-approved mode"

# 11. The run stored a real local-model key, so something WAS written to the
#     Keychain — the question is only where. Under the run's own service, and
#     nowhere else: the real service's entries carry the same attributes,
#     modification date included, that they did before the sidecar started.
if command -v security >/dev/null 2>&1; then
  if [ "$(security find-generic-password -s "$keychain_service" -a 'local-model-api-key:4611686018427387904' 2>&1 | grep -c 'svce')" = "1" ]; then
    ok "the run's key landed under its own Keychain service"
  else
    fail "the run's key landed under its own Keychain service" "nothing found under $keychain_service"
  fi
  if [ "$(keychain_snapshot)" = "$keychain_before" ]; then
    ok "the real Keychain service is byte-identical to before the run"
  else
    fail "the real Keychain service is byte-identical to before the run" \
      "$real_keychain_service changed; before/after differ"
  fi
else
  echo "skip - Keychain isolation checks (no security(1))"
fi

echo
if [ "$failures" -eq 0 ]; then
  echo "smoke-l2-approvals: $checks checks passed"
  exit 0
fi
echo "smoke-l2-approvals: $failures of $checks checks FAILED" >&2
exit 1
