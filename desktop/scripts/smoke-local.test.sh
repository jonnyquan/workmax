#!/usr/bin/env bash
#
# Regression tests for smoke-local.sh validation paths that must fail before
# any token-bearing curl request is attempted.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SMOKE="$SCRIPT_DIR/smoke-local.sh"
tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/workmax-smoke-local-test.XXXXXX")"
trap 'rm -rf "$tmp_dir"' EXIT

expect_fail() {
  local name="$1"
  local want="$2"
  shift 2
  local output
  if output="$("$SMOKE" "$@" 2>&1)"; then
    printf 'not ok - %s: smoke unexpectedly passed\n%s\n' "$name" "$output" >&2
    exit 1
  fi
  if ! grep -Fq -- "$want" <<<"$output"; then
    printf 'not ok - %s: missing expected failure text: %s\n%s\n' "$name" "$want" "$output" >&2
    exit 1
  fi
  printf 'ok - %s\n' "$name"
}

expect_fail_env() {
  local name="$1"
  local want="$2"
  shift 2
  local output
  if output="$(WORKMAX_LOCAL_TOKEN=tok "$SMOKE" "$@" 2>&1)"; then
    printf 'not ok - %s: smoke unexpectedly passed\n%s\n' "$name" "$output" >&2
    exit 1
  fi
  if ! grep -Fq -- "$want" <<<"$output"; then
    printf 'not ok - %s: missing expected failure text: %s\n%s\n' "$name" "$want" "$output" >&2
    exit 1
  fi
  printf 'ok - %s\n' "$name"
}

expect_pass() {
  local name="$1"
  shift
  local output
  if ! output="$("$SMOKE" "$@" 2>&1)"; then
    printf 'not ok - %s\n%s\n' "$name" "$output" >&2
    exit 1
  fi
  printf 'ok - %s\n' "$name"
}

expect_pass_env() {
  local name="$1"
  shift
  local output
  if ! output="$(WORKMAX_LOCAL_TOKEN=tok "$SMOKE" "$@" 2>&1)"; then
    printf 'not ok - %s\n%s\n' "$name" "$output" >&2
    exit 1
  fi
  printf 'ok - %s\n' "$name"
}

expect_pass "help exits before validation" --help

expect_fail "requires port or base" "--port or --base is required"
expect_fail "requires token before curl" "--token or WORKMAX_LOCAL_TOKEN is required" \
  --base http://127.0.0.1:49152
expect_fail "rejects missing option value" "--port requires a value" --port
expect_fail "rejects unknown argument" "unknown argument" --bogus

for base in \
  "http://evil.example:49152" \
  "https://127.0.0.1:49152" \
  "http://127.0.0.1" \
  "http://127.0.0.1:49152/path" \
  "http://127.0.0.1:49152?token=leak" \
  "http://127.0.0.1:49152#token" \
  "http://user:pass@127.0.0.1:49152" \
  "http://127.0.0.1:49152@evil.example"; do
  expect_fail_env "rejects unsafe base: $base" "--base must be a loopback origin" \
    --base "$base"
done

expect_fail_env "rejects zero diagnostics samples" "--diagnostics-samples must be a positive integer" \
  --base http://127.0.0.1:49152 --diagnostics-samples 0
expect_fail_env "rejects non-numeric diagnostics interval" "--diagnostics-interval must be a non-negative number" \
  --base http://127.0.0.1:49152 --diagnostics-interval soon
expect_fail_env "rejects zero request timeout" "--request-timeout must be a positive number" \
  --base http://127.0.0.1:49152 --request-timeout 0
expect_fail_env "rejects decimal zero request timeout" "--request-timeout must be a positive number" \
  --base http://127.0.0.1:49152 --request-timeout 0.00
expect_fail_env "check-pid-lock requires binary path" "--check-pid-lock requires --sidecar-binary" \
  --base http://127.0.0.1:49152 --check-pid-lock

fake_curl_dir="$tmp_dir/fake-curl-bin"
mkdir -p "$fake_curl_dir"
cat >"$fake_curl_dir/curl" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
outfile=""
url=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o)
      outfile="$2"
      shift 2
      ;;
    -w|--connect-timeout|--max-time|-X|-H|--data)
      shift 2
      ;;
    -sS)
      shift
      ;;
    http://*)
      url="$1"
      shift
      ;;
    *)
      shift
      ;;
  esac
done
if [ -z "$outfile" ] || [ -z "$url" ]; then
  echo "fake curl missing outfile or url" >&2
  exit 2
fi
path="${url#http://127.0.0.1:49152}"
case "$path" in
  /system/diagnostics)
    count_file="${WORKMAX_TEST_DIAGNOSTICS_COUNT:?}"
    count=0
    if [ -f "$count_file" ]; then
      count="$(cat "$count_file")"
    fi
    count=$((count + 1))
    printf '%s' "$count" >"$count_file"
    if [ "${WORKMAX_TEST_BAD_SECOND_DIAGNOSTICS:-0}" = "1" ] && [ "$count" -eq 2 ]; then
      cat >"$outfile" <<JSON
{"sidecar":{"version":"0.1.0-test"}}
JSON
    else
      cat >"$outfile" <<JSON
{"sidecar":{"version":"0.1.0-test","uptime_seconds":1,"heap_alloc_bytes":1,"heap_sys_bytes":1,"num_goroutine":1,"data_dir":"${WORKMAX_TEST_DATA_DIR:?}","db_path":"${WORKMAX_TEST_DB_PATH:?}","backup_path":"${WORKMAX_TEST_BACKUP_PATH:?}","integrity_check":"ok","applied_migrations":["0001"]}}
JSON
    fi
    ;;
  *)
    printf '{}\n' >"$outfile"
    ;;
esac
printf '200'
SH
chmod +x "$fake_curl_dir/curl"

diagnostics_data_dir="$tmp_dir/diagnostics-data"
mkdir -p "$diagnostics_data_dir/backups"
diagnostics_db_path="$diagnostics_data_dir/workagent.db"
diagnostics_backup_path="$diagnostics_data_dir/backups/workagent-20260521.db"
touch "$diagnostics_db_path" "$diagnostics_backup_path"

WORKMAX_TEST_DIAGNOSTICS_COUNT="$tmp_dir/diagnostics-good-count" \
WORKMAX_TEST_DATA_DIR="$diagnostics_data_dir" \
WORKMAX_TEST_DB_PATH="$diagnostics_db_path" \
WORKMAX_TEST_BACKUP_PATH="$diagnostics_backup_path" \
PATH="$fake_curl_dir:$PATH" \
  expect_pass_env "validates every diagnostics sample" \
  --base http://127.0.0.1:49152 --diagnostics-samples 2 --diagnostics-interval 0

WORKMAX_TEST_DIAGNOSTICS_COUNT="$tmp_dir/diagnostics-bad-count" \
WORKMAX_TEST_DATA_DIR="$diagnostics_data_dir" \
WORKMAX_TEST_DB_PATH="$diagnostics_db_path" \
WORKMAX_TEST_BACKUP_PATH="$diagnostics_backup_path" \
WORKMAX_TEST_BAD_SECOND_DIAGNOSTICS=1 \
PATH="$fake_curl_dir:$PATH" \
  expect_fail_env "rejects malformed later diagnostics sample" "diagnostics sample 2" \
  --base http://127.0.0.1:49152 --diagnostics-samples 2 --diagnostics-interval 0
