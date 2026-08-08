#!/usr/bin/env bash
#
# Smoke-test a running workagent-desktop sidecar over its loopback API.
#
# Usage:
#   WORKMAX_LOCAL_TOKEN=<token> ./desktop/scripts/smoke-local.sh --port <port>
#   ./desktop/scripts/smoke-local.sh --base http://127.0.0.1:<port> --token <token>
#
# This intentionally checks only local sidecar readiness. It does not
# complete OAuth or send a real cloud chat turn; use it before/after the
# manual real-cloud smoke in P1_COMPLETION_REPORT.md.
set -euo pipefail

base=""
port=""
token="${WORKMAX_LOCAL_TOKEN:-}"
trigger_sync=0
with_userinfo=0
with_server_version=0
with_skills_catalog=0
check_token_rejection=0
check_pid_lock=0
sidecar_binary=""
expect_version=""
diagnostics_samples=1
diagnostics_interval=5
request_timeout=10
pid_lock_timeout=5

usage() {
  cat >&2 <<'USAGE'
Usage:
  smoke-local.sh --port <port> [--token <token>] [--expect-version <version>] [--check-token-rejection] [--check-pid-lock --sidecar-binary <path>] [--trigger-sync] [--with-userinfo] [--with-server-version] [--with-skills-catalog] [--diagnostics-samples <n>] [--diagnostics-interval <seconds>] [--request-timeout <seconds>] [--pid-lock-timeout <seconds>]
  smoke-local.sh --base http://127.0.0.1:<port> [--token <token>] [--expect-version <version>] [--check-token-rejection] [--check-pid-lock --sidecar-binary <path>] [--trigger-sync] [--with-userinfo] [--with-server-version] [--with-skills-catalog] [--diagnostics-samples <n>] [--diagnostics-interval <seconds>] [--request-timeout <seconds>] [--pid-lock-timeout <seconds>]

Options:
  --port          Sidecar loopback port from the startup handshake.
  --base          Full sidecar base URL. Overrides --port. Must be a loopback
                  origin; credentials, paths, query strings, and fragments are rejected.
  --token         X-Local-Token. Defaults to $WORKMAX_LOCAL_TOKEN.
  --expect-version
                  Assert /system/diagnostics sidecar.version equals this value.
  --check-token-rejection
                  Verify missing and wrong X-Local-Token requests are rejected.
  --check-pid-lock
                  Launch a second sidecar against the same diagnostics data_dir
                  and verify it exits before opening SQLite.
  --sidecar-binary
                  Path to workagent-desktop for --check-pid-lock.
  --trigger-sync  POST /system/trigger-sync as part of the smoke. Requires an
                  active desktop session when the sidecar has TokenStore wired.
  --with-userinfo GET /auth/userinfo. Requires an authenticated sidecar.
  --with-server-version
                  GET /system/server-version. Requires cloud client/network.
  --with-skills-catalog
                  GET /agent/skills/catalog. Requires an authenticated sidecar
                  and cloud client/network; validates Desktop skill filtering path.
  --diagnostics-samples
                  Number of /system/diagnostics snapshots to capture. Default: 1.
  --diagnostics-interval
                  Seconds between diagnostics snapshots when samples > 1. Default: 5.
  --request-timeout
                  Max seconds for each curl request. Default: 10.
  --pid-lock-timeout
                  Max seconds to wait for the second sidecar lock probe.
                  Default: 5.
USAGE
}

require_value() {
  local opt="$1"
  local value="${2:-}"
  if [ -z "$value" ] || [[ "$value" == --* ]]; then
    echo "smoke-local.sh: $opt requires a value" >&2
    usage
    exit 2
  fi
}

normalize_loopback_base() {
  node -e '
    const raw = process.argv[1];
    let url;
    try {
      url = new URL(raw);
    } catch {
      process.exit(1);
    }
    const loopbackHost = url.hostname === "127.0.0.1" || url.hostname === "localhost";
    const isOriginOnly = url.pathname === "/" && url.search === "" && url.hash === "";
    if (
      url.protocol !== "http:" ||
      !loopbackHost ||
      url.username !== "" ||
      url.password !== "" ||
      url.port === "" ||
      !isOriginOnly
    ) {
      process.exit(1);
    }
    process.stdout.write(url.origin);
  ' "$1"
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --base)
      require_value "$1" "${2:-}"
      base="${2:-}"
      shift 2
      ;;
    --port)
      require_value "$1" "${2:-}"
      port="${2:-}"
      shift 2
      ;;
    --token)
      require_value "$1" "${2:-}"
      token="${2:-}"
      shift 2
      ;;
    --expect-version)
      require_value "$1" "${2:-}"
      expect_version="${2:-}"
      shift 2
      ;;
    --trigger-sync)
      trigger_sync=1
      shift
      ;;
    --with-userinfo)
      with_userinfo=1
      shift
      ;;
    --with-server-version)
      with_server_version=1
      shift
      ;;
    --with-skills-catalog)
      with_skills_catalog=1
      shift
      ;;
    --check-token-rejection)
      check_token_rejection=1
      shift
      ;;
    --check-pid-lock)
      check_pid_lock=1
      shift
      ;;
    --sidecar-binary)
      require_value "$1" "${2:-}"
      sidecar_binary="${2:-}"
      shift 2
      ;;
    --diagnostics-samples)
      require_value "$1" "${2:-}"
      diagnostics_samples="${2:-}"
      shift 2
      ;;
    --diagnostics-interval)
      require_value "$1" "${2:-}"
      diagnostics_interval="${2:-}"
      shift 2
      ;;
    --request-timeout)
      require_value "$1" "${2:-}"
      request_timeout="${2:-}"
      shift 2
      ;;
    --pid-lock-timeout)
      require_value "$1" "${2:-}"
      pid_lock_timeout="${2:-}"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "smoke-local.sh: unknown argument: $1" >&2
      usage
      exit 2
      ;;
  esac
done

if [ -z "$base" ]; then
  if [ -z "$port" ]; then
    echo "smoke-local.sh: --port or --base is required" >&2
    usage
    exit 2
  fi
  base="http://127.0.0.1:${port}"
fi

if [ -z "$token" ]; then
  echo "smoke-local.sh: --token or WORKMAX_LOCAL_TOKEN is required" >&2
  usage
  exit 2
fi

if ! [[ "$diagnostics_samples" =~ ^[0-9]+$ ]] || [ "$diagnostics_samples" -lt 1 ]; then
  echo "smoke-local.sh: --diagnostics-samples must be a positive integer" >&2
  usage
  exit 2
fi

if ! [[ "$diagnostics_interval" =~ ^[0-9]+([.][0-9]+)?$ ]]; then
  echo "smoke-local.sh: --diagnostics-interval must be a non-negative number" >&2
  usage
  exit 2
fi

if ! [[ "$request_timeout" =~ ^[0-9]+([.][0-9]+)?$ ]] ||
   ! node -e 'const n = Number(process.argv[1]); if (!Number.isFinite(n) || n <= 0) process.exit(1);' "$request_timeout"; then
  echo "smoke-local.sh: --request-timeout must be a positive number" >&2
  usage
  exit 2
fi

if ! [[ "$pid_lock_timeout" =~ ^[0-9]+$ ]] || [ "$pid_lock_timeout" -lt 1 ]; then
  echo "smoke-local.sh: --pid-lock-timeout must be a positive integer" >&2
  usage
  exit 2
fi

if [ "$check_pid_lock" -eq 1 ]; then
  if [ -z "$sidecar_binary" ]; then
    echo "smoke-local.sh: --check-pid-lock requires --sidecar-binary <path>" >&2
    usage
    exit 2
  fi
  if [ ! -x "$sidecar_binary" ]; then
    echo "smoke-local.sh: --sidecar-binary must be executable: $sidecar_binary" >&2
    exit 2
  fi
fi

if ! base="$(normalize_loopback_base "$base")"; then
  echo "smoke-local.sh: --base must be a loopback origin like http://127.0.0.1:<port> or http://localhost:<port>" >&2
  echo "                credentials, paths, query strings, and fragments are rejected" >&2
  usage
  exit 2
fi

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

request() {
  local method="$1"
  local path="$2"
  local outfile="$3"
  local status
  if ! status="$(curl -sS -o "$outfile" -w "%{http_code}" \
    --connect-timeout "$request_timeout" \
    --max-time "$request_timeout" \
    -X "$method" \
    -H "X-Local-Token: $token" \
    -H "Accept: application/json" \
    "$base$path")"; then
    echo "x $method $path -> curl failed" >&2
    exit 1
  fi
  if [ "$status" -lt 200 ] || [ "$status" -ge 300 ]; then
    echo "x $method $path -> HTTP $status" >&2
    sed -n '1,20p' "$outfile" >&2 || true
    exit 1
  fi
  echo "ok $method $path -> HTTP $status"
}

expect_status() {
  local expected_status="$1"
  local method="$2"
  local path="$3"
  local outfile="$4"
  shift 4
  local status
  if ! status="$(curl -sS -o "$outfile" -w "%{http_code}" \
    --connect-timeout "$request_timeout" \
    --max-time "$request_timeout" \
    -X "$method" \
    "$@" \
    "$base$path")"; then
    echo "x $method $path -> curl failed" >&2
    exit 1
  fi
  if [ "$status" != "$expected_status" ]; then
    echo "x $method $path -> HTTP $status, want $expected_status" >&2
    sed -n '1,20p' "$outfile" >&2 || true
    exit 1
  fi
  echo "ok $method $path -> HTTP $status"
}

validate_diagnostics_snapshot() {
  local file="$1"
  local label="$2"
  node -e '
    const fs = require("fs");
    const file = process.argv[1];
    const label = process.argv[2];
    const d = JSON.parse(fs.readFileSync(file, "utf8"));
    const sidecar = d && d.sidecar;
    function fail(message) {
      console.error(`x diagnostics ${label}: ${message}`);
      process.exit(1);
    }
    function requiredString(name) {
      const value = sidecar && sidecar[name];
      if (typeof value !== "string" || value.length === 0) {
        fail(`sidecar.${name} missing or empty`);
      }
      return value;
    }
    function requiredNonNegativeNumber(name) {
      const value = sidecar && sidecar[name];
      if (typeof value !== "number" || !Number.isFinite(value) || value < 0) {
        fail(`sidecar.${name} must be a non-negative number`);
      }
      return value;
    }
    if (!sidecar || typeof sidecar !== "object") {
      fail("sidecar block missing");
    }
    const dataDir = requiredString("data_dir");
    const dbPath = requiredString("db_path");
    const backupPath = requiredString("backup_path");
    const integrityCheck = requiredString("integrity_check");
    if (integrityCheck !== "ok") {
      fail(`sidecar.integrity_check=${integrityCheck}, want ok`);
    }
    const migrations = sidecar && sidecar.applied_migrations;
    if (!Array.isArray(migrations) || migrations.length === 0 || migrations.some((item) => typeof item !== "string" || item.length === 0)) {
      fail("sidecar.applied_migrations must be a non-empty string array");
    }
    const heapAlloc = requiredNonNegativeNumber("heap_alloc_bytes");
    const heapSys = requiredNonNegativeNumber("heap_sys_bytes");
    const goroutines = requiredNonNegativeNumber("num_goroutine");
    if (goroutines < 1) {
      fail("sidecar.num_goroutine must be at least 1");
    }
    for (const [pathLabel, path] of [["data_dir", dataDir], ["db_path", dbPath], ["backup_path", backupPath]]) {
      let stat;
      try {
        stat = fs.statSync(path);
      } catch (err) {
        fail(`sidecar.${pathLabel} path is not readable: ${path}`);
      }
      if (pathLabel === "data_dir" && !stat.isDirectory()) {
        fail(`sidecar.${pathLabel} is not a directory: ${path}`);
      }
      if (pathLabel !== "data_dir" && !stat.isFile()) {
        fail(`sidecar.${pathLabel} is not a file: ${path}`);
      }
    }
    console.log(`ok diagnostics ${label} sqlite paths/integrity/runtime -> db=${dbPath}, backup=${backupPath}, migrations=${migrations.join(",")}, heap_alloc=${heapAlloc}, heap_sys=${heapSys}, goroutines=${goroutines}`);
  ' "$file" "$label"
}

echo "==> smoke sidecar at $base"
request GET /health "$tmpdir/health.json"
request GET /auth/status "$tmpdir/auth-status.json"
request GET /system/diagnostics "$tmpdir/diagnostics-1.json"
request GET '/agent/threads?include_paused=false' "$tmpdir/threads.json"

diagnostics_version="$(node -e "const fs=require('fs'); const d=JSON.parse(fs.readFileSync(process.argv[1], 'utf8')); const v=d && d.sidecar && d.sidecar.version; if (!v) process.exit(1); process.stdout.write(v);" "$tmpdir/diagnostics-1.json")" || {
  echo "x could not read sidecar.version from /system/diagnostics" >&2
  exit 1
}
if [ -n "$expect_version" ] && [ "$diagnostics_version" != "$expect_version" ]; then
  echo "x diagnostics sidecar.version=$diagnostics_version, want $expect_version" >&2
  exit 1
fi
if [ -n "$expect_version" ]; then
  echo "ok diagnostics sidecar.version -> $diagnostics_version"
else
  echo "ok diagnostics sidecar.version present -> $diagnostics_version"
fi

validate_diagnostics_snapshot "$tmpdir/diagnostics-1.json" "sample 1"

if [ "$check_token_rejection" -eq 1 ]; then
  echo "==> checking local token rejection"
  expect_status 403 GET /health "$tmpdir/health-missing-token.json" \
    -H "Accept: application/json"
  expect_status 403 GET /health "$tmpdir/health-wrong-token.json" \
    -H "X-Local-Token: smoke-wrong-token" \
    -H "Accept: application/json"
  expect_status 403 GET /health "$tmpdir/health-duplicate-token.json" \
    -H "X-Local-Token: $token" \
    -H "X-Local-Token: smoke-wrong-token" \
    -H "Accept: application/json"
  expect_status 403 GET /auth/status "$tmpdir/auth-status-missing-token.json" \
    -H "Accept: application/json"
  expect_status 403 GET /system/diagnostics "$tmpdir/diagnostics-wrong-token.json" \
    -H "X-Local-Token: smoke-wrong-token" \
    -H "Accept: application/json"
  expect_status 403 GET '/agent/threads?include_paused=false' "$tmpdir/threads-missing-token.json" \
    -H "Accept: application/json"
  expect_status 403 POST /system/log "$tmpdir/renderer-log-wrong-token.json" \
    -H "X-Local-Token: smoke-wrong-token" \
    -H "Accept: application/json" \
    -H "Content-Type: application/json" \
    --data '{"level":"info","message":"token rejection smoke"}'
  expect_status 403 POST /system/trigger-sync "$tmpdir/trigger-sync-missing-token.json" \
    -H "Accept: application/json"
fi

if [ "$check_pid_lock" -eq 1 ]; then
  echo "==> checking sidecar pid lock"
  data_dir="$(node -e "const fs=require('fs'); const d=JSON.parse(fs.readFileSync(process.argv[1], 'utf8')); const v=d && d.sidecar && d.sidecar.data_dir; if (!v) process.exit(1); process.stdout.write(v);" "$tmpdir/diagnostics-1.json")" || {
    echo "x could not read sidecar.data_dir from /system/diagnostics" >&2
    exit 1
  }
  (
    WORKMAX_DESKTOP_DATA_DIR="$data_dir" \
      WORKMAX_LOCAL_TOKEN="smoke-pid-lock-token" \
      WORKMAX_DESKTOP_SKIP_STDIN_WATCHER=1 \
      "$sidecar_binary"
  ) >"$tmpdir/pid-lock-second.stdout" 2>"$tmpdir/pid-lock-second.stderr" &
  probe_pid=$!
  deadline=$((SECONDS + pid_lock_timeout))
  while kill -0 "$probe_pid" 2>/dev/null; do
    if [ "$SECONDS" -ge "$deadline" ]; then
      kill "$probe_pid" 2>/dev/null || true
      wait "$probe_pid" 2>/dev/null || true
      echo "x second sidecar launch did not exit within ${pid_lock_timeout}s; pid lock may have failed for data dir: $data_dir" >&2
      sed -n '1,40p' "$tmpdir/pid-lock-second.stdout" >&2 || true
      sed -n '1,40p' "$tmpdir/pid-lock-second.stderr" >&2 || true
      exit 1
    fi
    sleep 0.1
  done
  pid_lock_status=0
  wait "$probe_pid" || pid_lock_status=$?

  if [ "$pid_lock_status" -ne 0 ]; then
    if grep -Fq "another sidecar instance is already running" "$tmpdir/pid-lock-second.stderr"; then
      echo "ok second sidecar launch rejected for data dir: $data_dir"
    else
      echo "x second sidecar launch failed, but not with the expected pid-lock message" >&2
      sed -n '1,40p' "$tmpdir/pid-lock-second.stderr" >&2 || true
      exit 1
    fi
  else
    echo "x second sidecar launch unexpectedly succeeded for data dir: $data_dir" >&2
    exit 1
  fi
fi

if [ "$with_server_version" -eq 1 ]; then
  request GET /system/server-version "$tmpdir/server-version.json"
fi

if [ "$with_userinfo" -eq 1 ]; then
  request GET /auth/userinfo "$tmpdir/userinfo.json"
fi

if [ "$with_skills_catalog" -eq 1 ]; then
  request GET /agent/skills/catalog "$tmpdir/skills-catalog.json"
  node -e '
    const fs = require("fs");
    const d = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
    if (!Array.isArray(d.allowed_modes) || !d.allowed_modes.includes("ppt")) {
      console.error("x /agent/skills/catalog missing allowed_modes including ppt");
      process.exit(1);
    }
    if (!Array.isArray(d.items)) {
      console.error("x /agent/skills/catalog items is not an array");
      process.exit(1);
    }
    if (d.count !== d.items.length) {
      console.error(`x /agent/skills/catalog count=${d.count}, items.length=${d.items.length}`);
      process.exit(1);
    }
    for (const item of d.items) {
      if (!d.allowed_modes.includes(item.agentMode)) {
        console.error(`x /agent/skills/catalog returned non-allowlisted mode ${item.agentMode}`);
        process.exit(1);
      }
    }
  ' "$tmpdir/skills-catalog.json"
  echo "ok /agent/skills/catalog allowed_modes/items shape"
fi

if [ "$trigger_sync" -eq 1 ]; then
  if ! status="$(curl -sS -o "$tmpdir/trigger-sync.json" -w "%{http_code}" \
    --connect-timeout "$request_timeout" \
    --max-time "$request_timeout" \
    -X POST \
    -H "X-Local-Token: $token" \
    -H "Accept: application/json" \
    "$base/system/trigger-sync")"; then
    echo "x POST /system/trigger-sync -> curl failed" >&2
    exit 1
  fi
  if [ "$status" = "401" ]; then
    echo "x POST /system/trigger-sync -> HTTP 401 (login required before manual sync)" >&2
    sed -n '1,20p' "$tmpdir/trigger-sync.json" >&2 || true
    exit 1
  fi
  if [ "$status" -lt 200 ] || [ "$status" -ge 300 ]; then
    echo "x POST /system/trigger-sync -> HTTP $status" >&2
    sed -n '1,20p' "$tmpdir/trigger-sync.json" >&2 || true
    exit 1
  fi
  echo "ok POST /system/trigger-sync -> HTTP $status"
fi

if [ "$diagnostics_samples" -gt 1 ]; then
  i=2
  while [ "$i" -le "$diagnostics_samples" ]; do
    sleep "$diagnostics_interval"
    request GET /system/diagnostics "$tmpdir/diagnostics-${i}.json"
    validate_diagnostics_snapshot "$tmpdir/diagnostics-${i}.json" "sample ${i}"
    i=$((i + 1))
  done
fi

echo "==> diagnostics excerpt"
for file in "$tmpdir"/diagnostics-*.json; do
  echo "--- ${file##*/} ---"
  sed -n '1,40p' "$file"
  echo
done
echo
echo "==> smoke complete"
