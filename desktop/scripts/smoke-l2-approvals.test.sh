#!/usr/bin/env bash
#
# Tests for smoke-l2-approvals.sh that need neither a claude CLI nor a
# sidecar: the preflight, the skip gate, and the one shell construct whose
# breakage silently turned a passing approval into a 400 (a JSON body built
# inside a nested command substitution).
#
# The smoke itself is the real check; this is the check on the checker, so a
# machine with no CLI can still tell whether the harness is sane.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SMOKE="$SCRIPT_DIR/smoke-l2-approvals.sh"
UPSTREAM="$SCRIPT_DIR/l2-approval-upstream.mjs"
tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/workmax-smoke-l2-test.XXXXXX")"
trap 'rm -rf "$tmp_dir"' EXIT

expect_fail() {
  local name="$1" want="$2"
  shift 2
  local output
  if output="$("$SMOKE" "$@" 2>&1)"; then
    printf 'not ok - %s: unexpectedly succeeded\n%s\n' "$name" "$output" >&2
    exit 1
  fi
  if ! grep -Fq -- "$want" <<<"$output"; then
    printf 'not ok - %s: missing expected text: %s\n%s\n' "$name" "$want" "$output" >&2
    exit 1
  fi
  printf 'ok - %s\n' "$name"
}

expect_pass_containing() {
  local name="$1" want="$2"
  shift 2
  local output
  if ! output="$("$SMOKE" "$@" 2>&1)"; then
    printf 'not ok - %s: exited non-zero\n%s\n' "$name" "$output" >&2
    exit 1
  fi
  if ! grep -Fq -- "$want" <<<"$output"; then
    printf 'not ok - %s: missing expected text: %s\n%s\n' "$name" "$want" "$output" >&2
    exit 1
  fi
  printf 'ok - %s\n' "$name"
}

# --- preflight -------------------------------------------------------------

expect_pass_containing "help exits before anything is started" "Usage:" --help

# The CI contract: no CLI named means skip, not fail. A red build on every
# machine without a claude binary is how a gated smoke gets deleted.
( unset WORKMAX_TEST_CLAUDE_CLI
  expect_pass_containing "no CLI means skip, exit 0" "skipping" )

expect_fail "rejects a CLI path that is not executable" "is not an executable file" \
  --cli "$tmp_dir/definitely-not-here"

expect_fail "rejects an unknown argument" "unknown argument" --cli /bin/echo --bogus

# --- the harness's own JSON --------------------------------------------------

# The approve payload must survive the shell. This is the exact shape the
# watcher builds; nested inside "$( … )" it used to arrive mangled and the
# sidecar answered invalid_approval_body, which read like a product bug for
# an hour. Assert the built string is byte-exact JSON.
id="ap-1"
decision="allow_once"
payload="{\"approval_id\":\"$id\",\"decision\":\"$decision\"}"
if [ "$payload" = '{"approval_id":"ap-1","decision":"allow_once"}' ]; then
  printf 'ok - the approve payload survives shell quoting\n'
else
  printf 'not ok - the approve payload survives shell quoting: %s\n' "$payload" >&2
  exit 1
fi

# --- the scripted upstream ---------------------------------------------------

if command -v node >/dev/null 2>&1; then
  port_file="$tmp_dir/upstream.out"
  node "$UPSTREAM" > "$port_file" 2>&1 &
  upstream_pid=$!
  # Off the job table, or bash prints "Terminated" over the last ok line when
  # the trap kills it.
  disown "$upstream_pid" 2>/dev/null || true
  # shellcheck disable=SC2064
  trap "kill $upstream_pid 2>/dev/null; rm -rf '$tmp_dir'" EXIT
  url=""
  for _ in $(seq 1 50); do
    if grep -q '^upstream: ' "$port_file" 2>/dev/null; then
      url="$(sed 's/^upstream: //' "$port_file" | tr -d '\r\n')"
      break
    fi
    sleep 0.2
  done
  if [ -z "$url" ]; then
    printf 'not ok - the scripted upstream announces its URL\n%s\n' "$(cat "$port_file")" >&2
    exit 1
  fi
  printf 'ok - the scripted upstream announces its URL\n'

  # A directive in the conversation must produce that tool_use; the same
  # directive with its tool_use already in the transcript must not repeat it.
  # This identity rule (not position) is what keeps a resumed session from
  # looping the same call until maxTurns — measured against the real CLI.
  ask='{"messages":[{"role":"user","content":[{"type":"text","text":"go TOOLPLAN Write /tmp/x.txt"}]}]}'
  if curl -sS -X POST "$url/v1/messages" -H 'Content-Type: application/json' -d "$ask" \
      | grep -q '"name":"Write"'; then
    printf 'ok - a directive produces its tool_use\n'
  else
    printf 'not ok - a directive produces its tool_use\n' >&2
    exit 1
  fi

  done_ask='{"messages":[{"role":"user","content":[{"type":"text","text":"go TOOLPLAN Write /tmp/x.txt"}]},{"role":"assistant","content":[{"type":"tool_use","name":"Write","input":{"file_path":"/tmp/x.txt"}}]},{"role":"user","content":[{"type":"tool_result","content":"ok"},{"type":"text","text":"anything else"}]}]}'
  if curl -sS -X POST "$url/v1/messages" -H 'Content-Type: application/json' -d "$done_ask" \
      | grep -q 'L2-APPROVAL-SMOKE-COMPLETE'; then
    printf 'ok - a satisfied directive ends the turn instead of repeating\n'
  else
    printf 'not ok - a satisfied directive ends the turn instead of repeating\n' >&2
    exit 1
  fi

  if curl -sS -X POST "$url/v1/messages/count_tokens" -H 'Content-Type: application/json' -d '{}' \
      | grep -q 'input_tokens'; then
    printf 'ok - count_tokens is answered (the CLI calls it on large results)\n'
  else
    printf 'not ok - count_tokens is answered\n' >&2
    exit 1
  fi
else
  printf 'skip - scripted upstream checks (no node)\n'
fi

printf 'smoke-l2-approvals.test.sh: all checks passed\n'
