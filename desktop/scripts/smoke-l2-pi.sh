#!/usr/bin/env bash
#
# End-to-end smoke for the pi L2 tool loop — the second runtime — on the real
# thing: a real `pi --mode rpc` subprocess, a real sidecar, real SQLite, real
# SSE, real approval round trips over HTTP.
#
# The one piece that is not real is the model: a scripted OpenAI-compatible
# endpoint (pi-smoke-upstream.mjs) stands in for it, so the harness decides
# exactly which tool the loop calls and when. Nothing leaves the machine.
#
# Everything below was verified by hand before it was written down, and three
# assertions exist because the hand run failed them (see FINDINGS at the end).
#
# Usage:
#   WORKMAX_TEST_PI_BIN=/path/to/pi ./desktop/scripts/smoke-l2-pi.sh
#
#   ./desktop/scripts/smoke-l2-pi.sh --pi <path> [--binary <path>]
#                                    [--skip-timeout] [--keep]
#
# Skips (exit 0) when no pi binary is named: CI has none, and a red build on
# every machine without one is how a gated smoke gets deleted.
#
# It is safe to run on a machine you use. The data dir, the sidecar and pi's
# config live in a throwaway directory, and so do the Keychain entries: the run
# sets WORKMAX_KEYCHAIN_SERVICE to a per-run namespace, deletes what it created
# on the way out, and asserts that the real service's entries are byte-identical
# before and after. That was not always true — the account is derived from the
# uid, uids restart at the same number in every fresh data dir, and until the
# service name became overridable a run overwrote whatever local-model key and
# OAuth session the user had.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
UPSTREAM="$REPO_ROOT/desktop/scripts/pi-smoke-upstream.mjs"

pi_bin="${WORKMAX_TEST_PI_BIN:-}"
binary="$REPO_ROOT/desktop/wails/bin/workmax-desktop"
skip_timeout=0
keep=0

usage() {
  cat >&2 <<'USAGE'
Usage:
  smoke-l2-pi.sh [--pi <pi binary>] [--binary <workmax-desktop>]
                 [--skip-timeout] [--keep]

Options:
  --pi            Real pi binary (pi --mode rpc). Defaults to
                  $WORKMAX_TEST_PI_BIN. Without one the smoke skips (exit 0).
  --binary        workmax-desktop to drive. Defaults to
                  desktop/wails/bin/workmax-desktop; built if missing.
  --skip-timeout  Skip the unanswered-approval case, which costs the sidecar's
                  full 120s approval timeout.
  --keep          Keep the temporary data dir and logs, and print the path.
USAGE
}

while [ $# -gt 0 ]; do
  case "$1" in
    --pi) pi_bin="${2:-}"; shift 2 ;;
    --binary) binary="${2:-}"; shift 2 ;;
    --skip-timeout) skip_timeout=1; shift ;;
    --keep) keep=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "smoke-l2-pi.sh: unknown argument $1" >&2; usage; exit 2 ;;
  esac
done

if [ -z "$pi_bin" ]; then
  echo "smoke-l2-pi.sh: no pi binary named (--pi or WORKMAX_TEST_PI_BIN); skipping"
  exit 0
fi
if [ ! -x "$pi_bin" ]; then
  echo "smoke-l2-pi.sh: --pi $pi_bin is not an executable file" >&2
  exit 2
fi
for tool in node curl uuidgen; do
  command -v "$tool" >/dev/null 2>&1 || { echo "smoke-l2-pi.sh: $tool is required" >&2; exit 2; }
done
if [ ! -f "$UPSTREAM" ]; then
  echo "smoke-l2-pi.sh: missing $UPSTREAM" >&2
  exit 2
fi
if [ ! -x "$binary" ]; then
  echo "==> building $binary"
  ( cd "$REPO_ROOT/desktop/wails" \
    && GOOS="$(go env GOHOSTOS)" GOARCH="$(go env GOHOSTARCH)" CGO_ENABLED=1 \
       go build -tags desktop -o "$binary" . ) || {
    echo "smoke-l2-pi.sh: build failed" >&2; exit 2; }
fi

work="$(mktemp -d "${TMPDIR:-/tmp}/workmax-l2-pi.XXXXXX")"
token="pismoke$(uuidgen | tr -d '-' | tr 'A-Z' 'a-z')"
# The Keychain namespace this run owns. Per-run so two runs (or a run and a dev
# instance) cannot collide either, and matching the sidecar's validator:
# [A-Za-z0-9][A-Za-z0-9._-]*, at most 96 bytes.
keychain_service="ai.workmax.desktop.smoke-pi.$(uuidgen | tr -d '-' | tr 'A-Z' 'a-z' | cut -c1-12)"
# The service the real app uses, and the accounts a run of this shape would
# have clobbered: the OAuth session, and the local-model key of the first local
# account (uid = 2^62, which every fresh data dir hands out first).
real_keychain_service="ai.workmax.desktop"
real_keychain_accounts=("session" "local-model-api-key:4611686018427387904")
upstream_pid=""
sidecar_pid=""
base=""
upstream_url=""
failures=0
checks=0

# Attributes only — never `-w`. The modification date is enough to prove an
# entry was not rewritten, and reading the user's actual secret into a shell
# variable is not something a smoke test should do.
keychain_snapshot() {
  local out=""
  command -v security >/dev/null 2>&1 || { printf 'no-security'; return 0; }
  for account in "${real_keychain_accounts[@]}"; do
    out="$out$(security find-generic-password -s "$real_keychain_service" -a "$account" 2>&1 | grep -E '"(acct|mdat|cdat|svce)"|could not be found' | tr -d ' ')"
  done
  printf '%s' "$out"
}

# Delete every entry this run created. One at a time, because delete-generic-
# password removes a single match; the loop ends when nothing is left (exit 44).
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

# Three stand-ins for pi, each a wrapper this script writes. They are how the
# launch contract and the failure paths become observable: a real pi cannot be
# asked to die on cue, and its argv cannot be read back any other way.
write_pi_wrappers() {
  cat > "$work/pi-logged.sh" <<EOF
#!/usr/bin/env bash
{
  echo "ARGV: \$*"
  # Only the variables under assertion. The sidecar also passes its local
  # token down through the inherited environment, and a log file is not a
  # place to copy a credential to.
  env | grep -E '^(PI_|WORKMAX_PI_WORKSPACE=)' | sort
  echo "CWD: \$PWD"
} >> "$work/launch.log"
exec "$pi_bin" "\$@"
EOF
  # Junk before the first real frame: a non-JSON line (an extension's
  # console.log lands exactly here on the real pi) and a frame type this
  # sidecar has never heard of.
  cat > "$work/pi-noisy.sh" <<EOF
#!/usr/bin/env bash
echo "pi: starting up, this line is not JSON"
echo '{"type":"a_frame_from_the_future","payload":{"n":1}}'
exec "$pi_bin" "\$@"
EOF
  cat > "$work/pi-dead.sh" <<'EOF'
#!/usr/bin/env bash
echo "pi: cannot start: something broke" >&2
exit 3
EOF
  cat > "$work/pi-refuse.sh" <<'EOF'
#!/usr/bin/env bash
read -r _line
echo '{"id":"workmax-turn","type":"response","command":"prompt","success":false,"error":"still streaming"}'
sleep 30
EOF
  chmod +x "$work/pi-logged.sh" "$work/pi-noisy.sh" "$work/pi-dead.sh" "$work/pi-refuse.sh"
}

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
  echo "smoke-l2-pi.sh: the scripted upstream never came up" >&2
  exit 1
}

# start_sidecar <approvals:0|1> [pi binary] [base_url]
# One sidecar at a time (SQLite pid lock). The data dir persists across
# restarts on purpose: durable grants and session refs have to survive one.
start_sidecar() {
  local approvals="$1" pi_path="${2:-$pi_bin}" url="${3:-$upstream_url}"
  local log="$work/sidecar-$approvals-$RANDOM.log" env_approvals=""
  [ "$approvals" = "1" ] && env_approvals="WORKMAX_L2_APPROVALS=1"
  env WORKMAX_DESKTOP_DATA_DIR="$work/data" \
      WORKMAX_KEYCHAIN_SERVICE="$keychain_service" \
      WORKMAX_LOCAL_TOKEN="$token" \
      WORKMAX_PI_PATH="$pi_path" \
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
    echo "smoke-l2-pi.sh: the sidecar never bound a port; see $log" >&2
    exit 1
  fi
  base="http://127.0.0.1:$port"
  # The pi loop only runs for preferred_route=local with
  # protocol=openai_compatible, pointed at the scripted upstream.
  api PUT /settings/model-route \
    "{\"preferred_route\":\"local\",\"local\":{\"protocol\":\"openai_compatible\",\"base_url\":\"$url\",\"model_id\":\"pi-smoke\",\"api_key\":\"sk-pismoke-secret\"}}" \
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
  api PUT "/agent/threads/$uuid" '{"name":"pi L2 smoke","agent_mode":"ppt"}' > /dev/null
  printf '%s' "$uuid"
}

workspace_of() { printf '%s' "$work/data/agent_workspace/thread_$1"; }

# run_turn <label> <thread> <text> <decision|none>
#
# Streams the turn into $work/<label>.sse. With a decision, a watcher answers
# EVERY approval_request that appears on the stream — plural because pi raises
# one ui request per tool call and a single assistant message may carry two.
# The answer travels the renderer's route (POST the approve endpoint with the
# id the frame carried), not a test hook.
run_turn() {
  local label="$1" thread="$2" text="$3" decision="$4"
  local turn out watcher=""
  turn="$(uuidgen | tr 'A-Z' 'a-z')"
  out="$work/$label.sse"
  : > "$out"
  : > "$work/$label.approve"

  if [ "$decision" != "none" ]; then
    (
      answered=""
      for _ in $(seq 1 900); do
        for id in $(grep -o '"id":"ap-[0-9]*"' "$out" 2>/dev/null | sed 's/.*"\(ap-[0-9]*\)".*/\1/'); do
          case " $answered " in *" $id "*) continue ;; esac
          # Build the payload in a variable, never inline inside "$( … )": a
          # command substitution nested in a double-quoted string re-parses the
          # \" escapes and ships a mangled body, which the endpoint correctly
          # refuses as invalid_approval_body. Cost an hour once; not again.
          payload="{\"approval_id\":\"$id\",\"decision\":\"$decision\"}"
          code="$(curl -sS -o /dev/null -w '%{http_code}' \
            -X POST "$base/agent/turns/$turn/approve" \
            -H "X-Local-Token: $token" -H 'Content-Type: application/json' \
            -d "$payload")"
          answered="$answered $id"
          printf '%s %s\n' "$id" "$code" >> "$work/$label.approve"
        done
        sleep 0.2
      done
    ) &
    watcher=$!
  fi

  curl -sS -N -X POST "$base/agent/chat" \
    -H "X-Local-Token: $token" -H 'Content-Type: application/json' \
    -d "{\"turn_uuid\":\"$turn\",\"thread_uuid\":\"$thread\",\"user_text\":\"$text\",\"chat_mode\":\"ppt\",\"payload\":{\"stream\":true}}" \
    >> "$out" 2>&1
  if [ -n "$watcher" ]; then
    kill "$watcher" 2>/dev/null
    wait "$watcher" 2>/dev/null
  fi
  return 0
}

# --- assertions ------------------------------------------------------------

frame_present() { grep -q "^event: $2$" "$work/$1.sse" 2>/dev/null; }
frame_count() { grep -c "^event: $2$" "$work/$1.sse" 2>/dev/null; }
sse_dump() { sed -n '1,40p' "$work/$1.sse" 2>/dev/null | tr '\n' '|'; }

assert_frame() {
  if frame_present "$1" "$2"; then ok "$3"; else fail "$3" "frames: $(sse_dump "$1")"; fi
}
assert_no_frame() {
  if frame_present "$1" "$2"; then fail "$3" "frames: $(sse_dump "$1")"; else ok "$3"; fi
}
assert_frame_count() {
  local got; got="$(frame_count "$1" "$2")"
  if [ "$got" = "$3" ]; then ok "$4"; else fail "$4" "$2 x$got, want x$3: $(sse_dump "$1")"; fi
}
assert_file() {
  if [ -f "$1" ]; then ok "$2"; else fail "$2" "missing: $1"; fi
}
assert_no_file() {
  if [ -f "$1" ]; then fail "$2" "present but must not be: $1"; else ok "$2"; fi
}
# -e is not decoration: half the patterns here start with a dash ("-na", "-e "),
# and passed positionally grep reads them as its own options. `--` does not
# help — it ends assert_grep's arguments, not grep's, and a `--` handed in by a
# caller silently BECOMES the pattern, turning the check into a search for two
# hyphens that every log satisfies. Twelve launch-contract assertions passed
# that way before this was noticed.
assert_grep() {
  if grep -q -e "$2" "$1" 2>/dev/null; then ok "$3"; else fail "$3" "not found in $1: $2"; fi
}
assert_not_grep() {
  if grep -q -e "$2" "$1" 2>/dev/null; then fail "$3" "present in $1: $2"; else ok "$3"; fi
}

# --- the run ---------------------------------------------------------------

echo "==> pi binary:  $pi_bin ($("$pi_bin" --version 2>/dev/null | head -1))"
echo "==> workdir:    $work"
echo "==> keychain:   $keychain_service"
keychain_before="$(keychain_snapshot)"
write_pi_wrappers
start_upstream
echo "==> upstream:   $upstream_url"

# 1. The launch contract, read off a wrapper that records what it was handed.
#    Everything the runtime claims to pass has to actually be on the argv.
start_sidecar 1 "$work/pi-logged.sh"
echo "==> sidecar:    $base (approvals ON, launch-logging pi)"
thread="$(new_thread)"
run_turn launch "$thread" "Hello. TOOLPLAN none" none
for fragment in \
  "--mode rpc" \
  "--session " \
  "--session-dir " \
  "--provider workmax" \
  "--model pi-smoke" \
  "--tools read,grep,find,ls,write,edit" \
  "-na" "-nc" "-e " \
  "PI_OFFLINE=1" "PI_TELEMETRY=0" "PI_SKIP_VERSION_CHECK=1" "PI_CODING_AGENT_DIR=" \
  "WORKMAX_PI_WORKSPACE="
do
  assert_grep "$work/launch.log" "$fragment" "the subprocess is launched with $fragment"
done
# The cwd is matched by its tail, not the whole string: the subprocess reports
# the path with symlinks resolved (on macOS $TMPDIR is /var/… here and
# /private/var/… there) and that difference is a fact about the platform, not
# a mismatch. It is also the fact that broke the path guard — see case 11.
assert_grep "$work/launch.log" "CWD: .*/agent_workspace/thread_$thread\$" \
  "the subprocess runs in the thread's workspace"
# Isolation: pi's config dir is ours, so the user's ~/.pi is never read or
# written. The provider file is a projection of the settings, rewritten per
# turn — including the user's key, which has to survive the trip.
assert_file "$work/data/pi_home/models.json" "the provider file is written under our own pi_home"
assert_grep "$work/data/pi_home/models.json" '"baseUrl"' "the provider file names the endpoint"
# FINDING (fixed): the key never arrived. DarwinKeychain.Write fed security(1)'s
# prompt once where it reads twice, so every stored secret became the empty
# string — silently, exit 0 — and the loop authenticated with the placeholder.
assert_grep "$work/upstream.log" 'auth=Bearer sk-pismoke-secret' \
  "the user's configured API key reaches the endpoint"
assert_not_grep "$work/upstream.log" 'auth=Bearer workmax-local-no-key' \
  "the placeholder key never stands in for a configured one"

# 2. Text and reasoning both stream.
assert_frame launch text_delta "an answer streams as text_delta"
assert_grep "$work/launch.sse" 'PI-SMOKE-COMPLETE' "the answer text arrives"
assert_frame launch reasoning_delta "thinking streams as reasoning_delta"
assert_grep "$work/launch.sse" 'PI-SMOKE-REASONING' "the reasoning text arrives"
assert_frame launch done "a plain turn reaches done"

# 3. Junk on stdout is tolerated, not fatal. pi's own extensions can put a
#    console.log on the JSONL stream, and its event vocabulary outgrows any
#    sidecar; neither may kill a turn.
stop_sidecar
start_sidecar 1 "$work/pi-noisy.sh"
thread="$(new_thread)"
run_turn noisy "$thread" "Hello. TOOLPLAN none" none
assert_frame noisy done "a non-JSON line and an unknown frame do not kill the turn"
assert_grep "$work/noisy.sse" 'PI-SMOKE-COMPLETE' "the answer still arrives after the junk"

stop_sidecar
start_sidecar 1
echo "==> sidecar:    $base (approvals ON)"

# 4. A write asks, and the answer runs the tool.
thread="$(new_thread)"; ws="$(workspace_of "$thread")"
run_turn once "$thread" "Do it. TOOLPLAN write $ws/once.txt" allow_once
assert_frame once approval_request "a write tool call raises approval_request"
assert_grep "$work/once.approve" ' 200' "the approve endpoint accepts the decision"
assert_file "$ws/once.txt" "allow_once runs the tool"
assert_frame once done "an approved turn still reaches done"
assert_frame once tool_result "an approved call reports back when it finishes"
# FINDING (fixed): the announcement carried pi's own lowercase name while the
# question and the denial carried the shared one, so the renderer's fold — keyed
# on (name, target) — could never match. The question drew a card of its own
# instead of asking on the row it was about, and a denial drew a second row for
# a call that ran zero times. One call, one name, on every frame about it.
if grep -A1 '^event: tool_use$' "$work/once.sse" | grep -q '"name":"Write","target":"once.txt"'; then
  ok "the announcement uses the shared tool vocabulary and names its target"
else
  fail "the announcement uses the shared tool vocabulary and names its target" "frames: $(sse_dump once)"
fi
if grep -A1 '^event: approval_request$' "$work/once.sse" | grep -q '"name":"Write","target":"once.txt"'; then
  ok "the question names the same tool and target as the announcement"
else
  fail "the question names the same tool and target as the announcement" "frames: $(sse_dump once)"
fi
if grep -A1 '^event: tool_result$' "$work/once.sse" | grep -q '"name":"Write"'; then
  ok "the result names the same tool, so the work log can close the step"
else
  fail "the result names the same tool, so the work log can close the step" "frames: $(sse_dump once)"
fi

# 5. Deny: the tool must not run, and the turn must survive the refusal.
thread="$(new_thread)"; ws="$(workspace_of "$thread")"
run_turn deny "$thread" "Do it. TOOLPLAN write $ws/denied.txt" deny
assert_no_file "$ws/denied.txt" "deny keeps the tool from running"
assert_frame deny tool_denied "deny is narrated on the stream"
assert_frame deny done "a denied turn still reaches done"
if grep -A1 '^event: tool_denied$' "$work/deny.sse" | grep -q '"target":"denied.txt"'; then
  ok "the denial names the call it refuses"
else
  fail "the denial names the call it refuses" "frames: $(sse_dump deny)"
fi
# A refusal comes back a SECOND time, as pi's errored tool_execution_end. The
# renderer must treat the already-settled step as settled — a bare "failed"
# would replace the reason the user needs.
if grep -A1 '^event: tool_result$' "$work/deny.sse" | grep -q '"is_error":"true"'; then
  ok "a refused call also reports back as an errored result"
else
  fail "a refused call also reports back as an errored result" "frames: $(sse_dump deny)"
fi

# 6. Session grant: the second turn of the SAME thread must not ask again.
thread="$(new_thread)"; ws="$(workspace_of "$thread")"
run_turn session1 "$thread" "Do it. TOOLPLAN write $ws/sess1.txt" allow_session
assert_frame session1 approval_request "the first write of a session asks"
run_turn session2 "$thread" "Again. TOOLPLAN write $ws/sess2.txt" none
assert_no_frame session2 approval_request "allow_session silences the second call"
assert_file "$ws/sess2.txt" "the session-granted call still runs"

# 7. Session continuity: one session file for the thread, reused, and pi
#    really resumes into it rather than starting over.
ref=""
if command -v sqlite3 >/dev/null 2>&1; then
  ref="$(sqlite3 "$work/data/workagent.db" \
    "SELECT session_ref FROM w_desktop_agent_session WHERE thread_uuid = '$thread' AND runtime = 'pi'" 2>/dev/null)"
  if [ -n "$ref" ] && [ -f "$ref" ]; then
    ok "the session file path is stored on the thread and exists"
  else
    fail "the session file path is stored on the thread and exists" "ref=$ref"
  fi
  # Both turns live in ONE file: the second run passed --session <ref> and pi
  # appended to it. Two user messages in one session is resume, not restart.
  if [ -n "$ref" ] && [ "$(grep -c '"role":"user"' "$ref" 2>/dev/null)" -ge 2 ]; then
    ok "the second turn resumed the same session instead of starting a new one"
  else
    fail "the second turn resumed the same session instead of starting a new one" \
      "user messages: $(grep -c '"role":"user"' "$ref" 2>/dev/null)"
  fi
else
  echo "skip - session ref checks (no sqlite3)"
fi

# 8. Durable grant: a rule row under the SHARED name, and no card on a
#    brand-new thread. The row is what the claude engine reads too — a grant
#    given on one engine has to silence the other, and it can only do that if
#    both spell the tool the same way.
thread="$(new_thread)"; ws="$(workspace_of "$thread")"
mkdir -p "$ws"; printf 'seed\n' > "$ws/always1.txt"
run_turn always1 "$thread" "Do it. TOOLPLAN edit $ws/always1.txt" allow_always
assert_frame always1 approval_request "the first edit asks"
thread2="$(new_thread)"; ws2="$(workspace_of "$thread2")"
mkdir -p "$ws2"; printf 'seed\n' > "$ws2/always2.txt"
run_turn always2 "$thread2" "Do it. TOOLPLAN edit $ws2/always2.txt" none
assert_no_frame always2 approval_request "allow_always silences a different thread"
assert_grep "$ws2/always2.txt" 'edited by the pi L2 smoke' "the always-granted call still runs"
if command -v sqlite3 >/dev/null 2>&1; then
  # uid = 2^62 + (id - 1), the local-account partition (see localAccountUID).
  uid="$(sqlite3 "$work/data/workagent.db" \
    "SELECT 4611686018427387904 + id - 1 FROM w_desktop_local_account WHERE is_active = 1 ORDER BY id LIMIT 1" 2>/dev/null)"
  rows="$(sqlite3 "$work/data/workagent.db" \
    "SELECT uid || ':' || tool || ':' || decision FROM w_desktop_agent_permission_rule" 2>/dev/null)"
  if [ "$rows" = "$uid:Edit:allow" ]; then
    ok "allow_always stores one rule under the shared name, for the active uid ($rows)"
  else
    fail "allow_always stores one rule under the shared name, for the active uid" \
      "want $uid:Edit:allow, got: $rows"
  fi
else
  echo "skip - permission rule row (no sqlite3)"
fi

# 9. Read-only tools never ask, and still report both ends.
thread="$(new_thread)"; ws="$(workspace_of "$thread")"
mkdir -p "$ws"; printf 'seed content\n' > "$ws/readme.txt"
run_turn readonly "$thread" "Read it. TOOLPLAN read $ws/readme.txt" none
assert_no_frame readonly approval_request "a read never asks"
assert_frame readonly tool_use "the read still runs"
assert_frame readonly tool_result "and reports back — both ends of every call"

# 10. A tool outside the declared profile does not execute. pi's --tools is a
#     real restriction (it answers "Tool bash not found"), which is exactly the
#     property the claude engine's WithAllowedTools did NOT have.
thread="$(new_thread)"
run_turn surface "$thread" "Run it. TOOLPLAN bash $work/bash-escape.txt" none
assert_no_file "$work/bash-escape.txt" "a tool outside the profile does not execute"
assert_frame surface done "the turn survives a tool it may not call"
# ...and it reads as a refusal, not as a failure. pi answers such a call with
# "Tool bash not found" and isError, which used to reach the user as the same
# red row a disk error produces while the claude engine explained itself.
assert_frame surface tool_denied "a tool outside the profile is narrated as denied, not failed"
assert_grep "$work/surface.sse" '工具不在本地循环的许可面内' \
  "the refusal carries the same sentence the claude engine uses"
assert_no_frame surface tool_result "a call that never ran does not report a result"

# 11. The workspace boundary holds, and the refusal is explained.
thread="$(new_thread)"
run_turn escape "$thread" "Write it. TOOLPLAN write $work/escape-proof.txt" none
assert_no_file "$work/escape-proof.txt" "a write outside the workspace does not land"
assert_no_frame escape approval_request "an escape is refused outright, never offered for approval"
# FINDING (fixed): the path guard lives inside the pi extension, and its verdict
# never crossed back to Go — the user saw a bare failed result, indistinguishable
# from a disk error, where the claude engine says why.
assert_frame escape tool_denied "the escape is narrated as denied"
# Note the run itself is the regression test for the other half of this: the
# work dir lives under $TMPDIR, which macOS reaches through a symlink, so every
# in-workspace write above went through a guard whose two spellings of the
# workspace root differ. Before the fix they all failed here.
if grep -A1 '^event: tool_denied$' "$work/escape.sse" | grep -q '"target":"escape-proof.txt"'; then
  ok "the guard denial names the call it refuses"
else
  fail "the guard denial names the call it refuses" "frames: $(sse_dump escape)"
fi
assert_grep "$work/escape.sse" '路径在工作区之外' "the guard denial carries the reason"

# 12. Two calls in one assistant message: two announcements, two questions,
#     two results. The results carry no target — pi's tool_execution_end cannot
#     name a file — so the renderer settles the oldest open call of that tool,
#     and the count is what makes that pairing possible at all.
thread="$(new_thread)"; ws="$(workspace_of "$thread")"
run_turn two "$thread" "PISMOKE_TWOCALLS Do it. TOOLPLAN write $ws/two.txt" allow_once
assert_frame_count two tool_use 2 "both calls are announced"
assert_frame_count two approval_request 2 "both calls ask"
assert_frame_count two tool_result 2 "both calls report back"
assert_file "$ws/two.txt.a" "the first approved call runs"
assert_file "$ws/two.txt.b" "the second approved call runs"

# 13. An unanswered card denies on the sidecar's timeout, and the turn lives.
if [ "$skip_timeout" = "1" ]; then
  echo "skip - unanswered approval times out (--skip-timeout)"
else
  thread="$(new_thread)"; ws="$(workspace_of "$thread")"
  echo "==> waiting out the 120s approval timeout"
  started="$(date +%s)"
  run_turn timeout "$thread" "Do it. TOOLPLAN write $ws/timeout.txt" none
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

# 14. Failure paths. Each is a different layer giving up, and each has to reach
#     the renderer as one typed proxy_error rather than a hang or a silence.
thread="$(new_thread)"
run_turn upfail "$thread" "PISMOKE_REJECT now. TOOLPLAN none" none
assert_frame upfail proxy_error "an endpoint that refuses the request ends as proxy_error"
assert_grep "$work/upfail.sse" 'upstream refused' "the endpoint's own words reach the user"
# Typed, not shrugged at. Everything pi reported used to arrive as
# kind:unknown/retryable:true — one sentence covering a bad model id, a dead
# endpoint and a throttled account.
assert_grep "$work/upfail.sse" 'bad_request' "a refused request is a bad_request, not an unknown"
assert_not_grep "$work/upfail.sse" '"kind":"unknown"' "the upstream failure is classified, not shrugged at"
# The ref self-heals: a turn that tried to resume and failed drops the ref, so
# the next turn takes the always-works flatten path.
if command -v sqlite3 >/dev/null 2>&1; then
  run_turn refok "$thread" "Hello. TOOLPLAN none" none
  assert_frame refok done "the thread recovers on the next turn"
  run_turn refail "$thread" "PISMOKE_REJECT again. TOOLPLAN none" none
  rows="$(sqlite3 "$work/data/workagent.db" \
    "SELECT count(*) FROM w_desktop_agent_session WHERE thread_uuid = '$thread'" 2>/dev/null)"
  if [ "$rows" = "0" ]; then
    ok "a failed turn clears the session ref it tried to resume"
  else
    fail "a failed turn clears the session ref it tried to resume" "rows=$rows"
  fi
fi

stop_sidecar
start_sidecar 1 "$work/pi-dead.sh"
thread="$(new_thread)"
run_turn dead "$thread" "Hello. TOOLPLAN none" none
assert_frame dead proxy_error "a subprocess that dies before settling ends as proxy_error"
assert_grep "$work/dead.sse" 'service_unavailable' "the dead subprocess is a service_unavailable"
assert_grep "$work/dead.sse" 'something broke' "its stderr tail is carried for diagnosis"

stop_sidecar
start_sidecar 1 "$work/pi-refuse.sh"
thread="$(new_thread)"
run_turn refuse "$thread" "Hello. TOOLPLAN none" none
assert_frame refuse proxy_error "a rejected prompt ends as proxy_error"
assert_grep "$work/refuse.sse" 'bad_request' "a rejected prompt is a bad_request, not a retryable outage"

stop_sidecar
start_sidecar 1 "$pi_bin" "http://127.0.0.1:1/v1"
thread="$(new_thread)"
run_turn unreachable "$thread" "Hello. TOOLPLAN none" none
assert_frame unreachable proxy_error "an unreachable endpoint ends as proxy_error"
# A different cause deserves a different sentence: pi reports a refused
# connection as a synthetic 502, and "is it running?" is the question that
# helps — not "the request was invalid".
assert_grep "$work/unreachable.sse" 'network_unavailable' \
  "an unreachable endpoint is a network failure, not a bad request"
assert_grep "$work/unreachable.sse" '请确认它正在运行' "the network failure says what to check"

# 15. With the flag off — the shipping default — the loop runs on the read-only
#     profile. This is where the claude engine had its hole: a tool outside the
#     surface executed anyway. pi's --tools genuinely refuses.
stop_sidecar
start_sidecar 0
echo "==> sidecar:    $base (approvals OFF)"
thread="$(new_thread)"; ws="$(workspace_of "$thread")"
run_turn bypass "$thread" "Do it. TOOLPLAN write $ws/bypass.txt" none
assert_no_frame bypass approval_request "no approvals flag means no card"
assert_no_file "$ws/bypass.txt" "the read-only default does not let a write through"
assert_frame bypass done "the turn survives the refused write"
thread="$(new_thread)"; ws="$(workspace_of "$thread")"
mkdir -p "$ws"; printf 'seed content\n' > "$ws/readable.txt"
run_turn bypass_read "$thread" "Read it. TOOLPLAN read $ws/readable.txt" none
assert_frame bypass_read tool_use "reads still run in the default mode"
code="$(curl -sS -o /dev/null -w '%{http_code}' -X POST \
  "$base/agent/turns/$(uuidgen | tr 'A-Z' 'a-z')/approve" \
  -H "X-Local-Token: $token" -H 'Content-Type: application/json' \
  -d '{"approval_id":"ap-1","decision":"allow_once"}')"
if [ "$code" = "503" ]; then
  ok "the approve endpoint reports 503 when approvals are off"
else
  fail "the approve endpoint reports 503 when approvals are off" "got HTTP $code"
fi

# The user's own ~/.pi is never touched: every pi file this run produced lives
# under our data dir, which is the whole point of PI_CODING_AGENT_DIR.
assert_file "$work/data/pi_home/models.json" "pi's config stayed inside the sidecar data dir"

# 16. And neither is the user's own Keychain. The run stored a real key (case 1
#     proved it reached the endpoint), so something WAS written — the question
#     is only where. Under the run's own service, and nowhere else: the real
#     service's entries carry the same attributes, modification date included,
#     that they did before the sidecar started.
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
  echo "smoke-l2-pi: $checks checks passed"
  exit 0
fi
echo "smoke-l2-pi: $failures of $checks checks FAILED" >&2
exit 1

# FINDINGS from the first real run (all three fixed in the same change):
#
#  1. The user's API key never reached the endpoint. DarwinKeychain.Write feeds
#     `security add-generic-password -w` over stdin, and that prompt reads the
#     value TWICE. Sending it once made the two disagree; security(1) then
#     created the item with an EMPTY password and exited 0. Every secret the
#     sidecar stores went in empty — the local model key here, and OAuth tokens
#     by the same path — while api_key_configured still answered true.
#
#  2. One call carried two names. tool_use/tool_result used pi's lowercase
#     ("write"), approval_request/tool_denied used the shared Claude name
#     ("Write"). The renderer folds a question and a denial into the step they
#     belong to by matching (name, target), so it matched nothing: the approval
#     drew a separate card instead of asking on its row, and every denial drew a
#     second row for a call that ran zero times.
#
#  3. The path guard's verdict never left the subprocess. The guard lives in the
#     embedded pi extension; it blocked correctly, but Go only saw an errored
#     tool result, so a write outside the workspace reached the user as "failed"
#     — indistinguishable from a disk error — where the claude engine explains.
