#!/usr/bin/env bash
#
# Tests for smoke-l2-pi.sh that need neither a pi binary nor a sidecar: the
# preflight, the skip gate, the scripted upstream's own contract, and the two
# shell constructs whose breakage made the smoke lie rather than fail.
#
# The smoke itself is the real check; this is the check on the checker, so a
# machine with no pi binary can still tell whether the harness is sane.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SMOKE="$SCRIPT_DIR/smoke-l2-pi.sh"
UPSTREAM="$SCRIPT_DIR/pi-smoke-upstream.mjs"
tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/workmax-smoke-pi-test.XXXXXX")"
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

pass() { printf 'ok - %s\n' "$1"; }
die() { printf 'not ok - %s\n' "$1" >&2; exit 1; }

# --- preflight -------------------------------------------------------------

expect_pass_containing "help exits before anything is started" "Usage:" --help

# The CI contract: no pi binary named means skip, not fail. A red build on
# every machine without one is how a gated smoke gets deleted.
( unset WORKMAX_TEST_PI_BIN
  expect_pass_containing "no pi binary means skip, exit 0" "skipping" )

expect_fail "rejects a pi path that is not executable" "is not an executable file" \
  --pi "$tmp_dir/definitely-not-here"

expect_fail "rejects an unknown argument" "unknown argument" --pi /bin/echo --bogus

# --- the harness's own shell -------------------------------------------------

# The approve payload must survive the shell. Nested inside "$( … )" it used to
# arrive mangled and the sidecar answered invalid_approval_body.
id="ap-1"
decision="allow_once"
payload="{\"approval_id\":\"$id\",\"decision\":\"$decision\"}"
[ "$payload" = '{"approval_id":"ap-1","decision":"allow_once"}' ] \
  || die "the approve payload survives shell quoting: $payload"
pass "the approve payload survives shell quoting"

# assert_grep's patterns start with a dash half the time ("-na", "-e "). Passed
# positionally, grep reads them as options; passed with a caller-supplied "--",
# the "--" silently becomes the pattern and every check passes against any
# file. Twelve launch-contract assertions were green that way. The smoke uses
# `grep -q -e "$pattern"`, and this is that behaviour, isolated.
printf 'ARGV: --mode rpc -na -nc\n' > "$tmp_dir/launch.log"
grep -q -e "-na" "$tmp_dir/launch.log" || die "a dash-leading pattern is searched for, not parsed as an option"
grep -q -e "-nx" "$tmp_dir/launch.log" && die "a pattern absent from the file must not match"
pass "a dash-leading pattern is searched for, not parsed as an option"

# --- the scripted upstream ---------------------------------------------------

if command -v node >/dev/null 2>&1; then
  [ -f "$UPSTREAM" ] || die "the scripted upstream exists"
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
  [ -n "$url" ] || die "the scripted upstream announces its URL: $(cat "$port_file")"
  pass "the scripted upstream announces its URL"

  ask='{"messages":[{"role":"user","content":"go TOOLPLAN write /tmp/x.txt"}]}'
  out="$(curl -sS -X POST "$url/chat/completions" -H 'Content-Type: application/json' -d "$ask")"
  grep -q '"name":"write"' <<<"$out" || die "a directive produces its tool call: $out"
  grep -q '"path\\":\\"/tmp/x.txt' <<<"$out" || die "the tool call carries the directive's path: $out"
  pass "a directive produces its tool call, with the path it named"

  # The tool call arrives split across deltas, because a client that assumes
  # one whole JSON argument blob per call is a client that works only here.
  args_chunks="$(grep -c 'function":{"arguments' <<<"$out" || true)"
  [ "${args_chunks:-0}" -ge 2 ] || die "the arguments stream in more than one delta (got $args_chunks)"
  pass "the arguments stream in more than one delta"

  # Identity, not position, decides "already dispatched": a resumed pi session
  # replays every earlier message, and a position rule re-issues the same call
  # until the loop is killed.
  done_ask='{"messages":[{"role":"user","content":"go TOOLPLAN write /tmp/x.txt"},{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"write","arguments":"{\"path\":\"/tmp/x.txt\"}"}}]},{"role":"tool","tool_call_id":"c1","content":"ok"}]}'
  out="$(curl -sS -X POST "$url/chat/completions" -H 'Content-Type: application/json' -d "$done_ask")"
  grep -q 'PI-SMOKE-COMPLETE' <<<"$out" || die "a satisfied directive ends the turn instead of repeating: $out"
  pass "a satisfied directive ends the turn instead of repeating"

  # Reasoning rides its own field; the smoke asserts it becomes reasoning_delta.
  grep -q 'reasoning_content' <<<"$out" || die "the final answer carries reasoning_content: $out"
  pass "the final answer carries reasoning_content"

  two='{"messages":[{"role":"user","content":"PISMOKE_TWOCALLS go TOOLPLAN write /tmp/x.txt"}]}'
  out="$(curl -sS -X POST "$url/chat/completions" -H 'Content-Type: application/json' -d "$two")"
  grep -q '/tmp/x.txt.a' <<<"$out" && grep -q '/tmp/x.txt.b' <<<"$out" \
    || die "PISMOKE_TWOCALLS emits two distinct calls: $out"
  pass "PISMOKE_TWOCALLS emits two distinct calls in one message"

  code="$(curl -sS -o /dev/null -w '%{http_code}' -X POST "$url/chat/completions" \
    -H 'Content-Type: application/json' -d '{"messages":[{"role":"user","content":"PISMOKE_REJECT"}]}')"
  [ "$code" = "400" ] || die "PISMOKE_REJECT makes the endpoint refuse (got $code)"
  pass "PISMOKE_REJECT makes the endpoint refuse"
else
  printf 'skip - scripted upstream checks (no node)\n'
fi

# --- Keychain isolation -------------------------------------------------------

# The Keychain is the one thing WORKMAX_DESKTOP_DATA_DIR cannot isolate: the
# account is derived from the uid and every fresh data dir hands out the same
# first uid, so only the SERVICE name separates this run from the user's real
# key. Static checks, because the property has to hold on a machine that cannot
# run the smoke at all — and because the day it silently stops holding is the
# day a run eats somebody's configured key.
grep -q 'WORKMAX_KEYCHAIN_SERVICE="\$keychain_service"' "$SMOKE" \
  || die "the sidecar is launched with an isolated Keychain service"
pass "the sidecar is launched with an isolated Keychain service"

grep -q 'purge_run_keychain' "$SMOKE" \
  || die "the run deletes the Keychain entries it created"
grep -q 'purge_run_keychain' <(sed -n '/^cleanup()/,/^}/p' "$SMOKE") \
  || die "the purge runs from the exit trap, not only on the happy path"
pass "the run deletes the Keychain entries it created, from the exit trap"

# Never `-w` against the real service: the snapshot exists to prove the entries
# were not touched, and reading the user's actual secret to do that would be a
# worse trade than the bug.
if sed -n '/^keychain_snapshot()/,/^}/p' "$SMOKE" | grep -q -e '-w'; then
  die "the real-service snapshot reads attributes only, never the secret"
fi
pass "the real-service snapshot reads attributes only, never the secret"

# The generated name must satisfy the sidecar's validator, or Bootstrap refuses
# to start and the whole smoke turns into a launch failure.
svc="ai.workmax.desktop.smoke.$(printf '%s' "0123456789ab")"
printf '%s' "$svc" | grep -qE '^[A-Za-z0-9][A-Za-z0-9._-]*$' \
  || die "the generated service name matches the sidecar's validator"
[ "${#svc}" -le 96 ] || die "the generated service name fits the sidecar's length bound"
pass "the generated service name matches the sidecar's validator"

printf 'smoke-l2-pi.test.sh: all checks passed\n'
