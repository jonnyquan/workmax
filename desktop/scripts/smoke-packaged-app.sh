#!/usr/bin/env bash
#
# Launch a packaged macOS .app once and verify that Electron actually loads the
# bundled file renderer with the preload bridge present. This complements the
# structural package inspector, which can prove files exist but cannot prove the
# packaged runtime chose them.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && cd .. && pwd)"

timeout_seconds=30
renderer_url_override=""
expect_failure_text=""
check_token_rotation=0
check_cached_history=0
keep_temp_on_failure=0
data_dir_override=""
expect_body_text=""
expect_thread_text=""
expect_message_text=""

usage() {
  cat >&2 <<'USAGE'
Usage:
  desktop/scripts/smoke-packaged-app.sh [--timeout seconds] <path-to-WorkMax Desktop.app>
  desktop/scripts/smoke-packaged-app.sh --check-cached-history [--timeout seconds] <path-to-WorkMax Desktop.app>
  desktop/scripts/smoke-packaged-app.sh --data-dir <existing-data-dir> --expect-body-text <text> [--timeout seconds] <path-to-WorkMax Desktop.app>
  desktop/scripts/smoke-packaged-app.sh --data-dir <existing-data-dir> --expect-thread-text <text> [--expect-message-text <text>] [--timeout seconds] <path-to-WorkMax Desktop.app>
  desktop/scripts/smoke-packaged-app.sh --check-token-rotation [--timeout seconds] <path-to-WorkMax Desktop.app>
  desktop/scripts/smoke-packaged-app.sh --renderer-url <url> --expect-failure <text> [--timeout seconds] <path-to-WorkMax Desktop.app>

Requires macOS. The app must already be built. The smoke launches the app with
a temporary data directory and an explicit Electron smoke env var, then asserts:
  - Electron reports app.isPackaged=true
  - the loaded renderer is the bundled file:// Resources/renderer entry
  - window.workmaxLocal is exposed without leaking the local token
  - the packaged sidecar rejects missing, wrong, and duplicate local tokens
  - /system/diagnostics reports readable SQLite db/backup paths, integrity ok,
    and Go runtime counters
  - the rendered body is nonblank

Token rotation mode launches the packaged app twice with the same temporary
data directory and verifies the sidecar token fingerprint changes.

Cached-history mode seeds one authenticated local thread/message in the
temporary data directory, points the sidecar cloud base at an unreachable
loopback origin, and verifies the bundled renderer displays both from cache.

Real-cache mode uses --data-dir to launch against an existing Desktop data
directory, points the sidecar cloud base at an unreachable loopback origin, and
asserts operator-provided cached thread/message text. Use this for the
public-release offline, previously-authenticated cache gate; it does not seed
cache rows or replace the user's Keychain-backed auth state.

Use --keep-temp-on-failure while developing a new smoke check to inspect logs.

Negative mode proves packaged startup fails before opening a bridged renderer
window when WORKMAX_DESKTOP_RENDERER_URL is unsafe.
USAGE
}

require_value() {
  local opt="$1"
  local value="${2:-}"
  if [ -z "$value" ] || [[ "$value" == --* ]]; then
    echo "smoke-packaged-app.sh: $opt requires a value" >&2
    usage
    exit 1
  fi
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --timeout)
      require_value "$1" "${2:-}"
      timeout_seconds="${2:-}"
      shift 2
      ;;
    --renderer-url)
      require_value "$1" "${2:-}"
      renderer_url_override="${2:-}"
      shift 2
      ;;
    --expect-failure)
      require_value "$1" "${2:-}"
      expect_failure_text="${2:-}"
      shift 2
      ;;
    --check-token-rotation)
      check_token_rotation=1
      shift
      ;;
    --check-cached-history)
      check_cached_history=1
      shift
      ;;
    --data-dir)
      require_value "$1" "${2:-}"
      data_dir_override="${2:-}"
      shift 2
      ;;
    --expect-body-text)
      require_value "$1" "${2:-}"
      expect_body_text="${2:-}"
      shift 2
      ;;
    --expect-thread-text)
      require_value "$1" "${2:-}"
      expect_thread_text="${2:-}"
      shift 2
      ;;
    --expect-message-text)
      require_value "$1" "${2:-}"
      expect_message_text="${2:-}"
      shift 2
      ;;
    --keep-temp-on-failure)
      keep_temp_on_failure=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    --)
      shift
      break
      ;;
    -*)
      echo "smoke-packaged-app.sh: unknown option '$1'" >&2
      usage
      exit 1
      ;;
    *)
      break
      ;;
  esac
done

if [ "$#" -ne 1 ]; then
  usage
  exit 1
fi

case "$(uname -s)" in
  Darwin) ;;
  *)
    echo "smoke-packaged-app.sh: packaged .app smoke requires macOS" >&2
    exit 1
    ;;
esac

if ! [[ "$timeout_seconds" =~ ^[0-9]+$ ]] || [ "$timeout_seconds" -lt 5 ]; then
  echo "smoke-packaged-app.sh: --timeout must be an integer >= 5" >&2
  exit 1
fi
if { [ -n "$renderer_url_override" ] && [ -z "$expect_failure_text" ]; } ||
   { [ -z "$renderer_url_override" ] && [ -n "$expect_failure_text" ]; }; then
  echo "smoke-packaged-app.sh: --renderer-url and --expect-failure must be used together" >&2
  exit 1
fi
if [ "$check_token_rotation" -eq 1 ] && [ -n "$expect_failure_text" ]; then
  echo "smoke-packaged-app.sh: --check-token-rotation cannot be combined with negative mode" >&2
  exit 1
fi
if [ "$check_cached_history" -eq 1 ] && [ -n "$expect_failure_text" ]; then
  echo "smoke-packaged-app.sh: --check-cached-history cannot be combined with negative mode" >&2
  exit 1
fi
if [ "$check_cached_history" -eq 1 ] && [ -n "$data_dir_override" ]; then
  echo "smoke-packaged-app.sh: --data-dir cannot be combined with --check-cached-history" >&2
  exit 1
fi
if [ -n "$expect_message_text" ] && [ -z "$expect_thread_text" ]; then
  echo "smoke-packaged-app.sh: --expect-message-text requires --expect-thread-text" >&2
  exit 1
fi
if { [ -n "$expect_body_text" ] || [ -n "$expect_thread_text" ] || [ -n "$expect_message_text" ]; } && [ -n "$expect_failure_text" ]; then
  echo "smoke-packaged-app.sh: body/cache text assertions cannot be combined with negative mode" >&2
  exit 1
fi
if { [ -n "$expect_body_text" ] || [ -n "$expect_thread_text" ] || [ -n "$expect_message_text" ]; } && [ -z "$data_dir_override" ]; then
  echo "smoke-packaged-app.sh: body/cache text assertions require --data-dir <existing-data-dir>" >&2
  exit 1
fi
if [ -n "$data_dir_override" ] &&
   [ -z "$expect_body_text" ] &&
   [ -z "$expect_thread_text" ] &&
   [ -z "$expect_message_text" ]; then
  echo "smoke-packaged-app.sh: --data-dir requires --expect-body-text or --expect-thread-text" >&2
  exit 1
fi
if [ -n "$data_dir_override" ] && [ ! -d "$data_dir_override" ]; then
  echo "smoke-packaged-app.sh: --data-dir must point to an existing directory: $data_dir_override" >&2
  exit 1
fi

app_path="$1"
if [ ! -d "$app_path/Contents/MacOS" ]; then
  echo "smoke-packaged-app.sh: not a macOS .app bundle: $app_path" >&2
  exit 1
fi
info_plist="$app_path/Contents/Info.plist"
if [ ! -f "$info_plist" ]; then
  echo "smoke-packaged-app.sh: missing Info.plist: $info_plist" >&2
  exit 1
fi

executable_name="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleExecutable' "$info_plist" 2>/dev/null || true)"
if [ -z "$executable_name" ]; then
  echo "smoke-packaged-app.sh: CFBundleExecutable missing from Info.plist" >&2
  exit 1
fi
executable_path="$app_path/Contents/MacOS/$executable_name"
if [ ! -x "$executable_path" ]; then
  echo "smoke-packaged-app.sh: app executable missing or not executable: $executable_path" >&2
  exit 1
fi

ensure_launchable_signature() {
  local app="$1"
  local verify_output signing_output

  if verify_output="$(codesign --verify --deep --strict --verbose=2 "$app" 2>&1)"; then
    return 0
  fi

  signing_output="$(codesign -dv --verbose=4 "$app" 2>&1 || true)"
  if printf '%s\n' "$signing_output" | grep -Eq 'Signature=adhoc|TeamIdentifier=not set'; then
    echo "smoke-packaged-app.sh: repairing unsigned/ad-hoc app signature for local runtime smoke" >&2
    codesign --force --deep --sign - "$app" >/dev/null
    codesign --verify --deep --strict --verbose=2 "$app" >/dev/null
    return 0
  fi

  echo "smoke-packaged-app.sh: app signature verification failed and will not be auto-repaired" >&2
  printf '%s\n' "$verify_output" >&2
  exit 1
}

ensure_launchable_signature "$app_path"

expected_version="$(cd "$REPO_ROOT/desktop/electron" && node -p "require('./package.json').version")"
tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/workmax-packaged-app-smoke.XXXXXX")"
cleanup() {
  local status="$?"
  if [ -n "${app_pid:-}" ] && kill -0 "$app_pid" 2>/dev/null; then
    kill "$app_pid" 2>/dev/null || true
  fi
  if [ "$status" -ne 0 ] && [ "$keep_temp_on_failure" -eq 1 ]; then
    echo "smoke-packaged-app.sh: keeping temp dir for debugging: $tmp_dir" >&2
    exit "$status"
  fi
  rm -rf "$tmp_dir"
  exit "$status"
}
trap cleanup EXIT

result_json="$tmp_dir/renderer-info.json"
if [ -n "$data_dir_override" ]; then
  data_dir="$data_dir_override"
else
  data_dir="$tmp_dir/data"
fi
stdout_log="$tmp_dir/app.stdout.log"
stderr_log="$tmp_dir/app.stderr.log"

launch_once() {
  local result_path="$1"
  local data_path="$2"
  local stdout_path="$3"
  local stderr_path="$4"
  local cloud_base_override=""

  if [ "$check_cached_history" -eq 1 ] ||
     [ -n "$expect_body_text" ] ||
     [ -n "$expect_thread_text" ] ||
     [ -n "$expect_message_text" ]; then
    cloud_base_override="http://127.0.0.1:9"
  fi

  echo "==> launching packaged app smoke: $app_path"
  local launch_env=(
    "WORKMAX_DESKTOP_SMOKE_RENDERER_INFO=$result_path"
    "WORKMAX_DESKTOP_DATA_DIR=$data_path"
    "WORKMAX_DESKTOP_SMOKE_SEED_CACHE=$check_cached_history"
    "WORKMAX_DESKTOP_SMOKE_EXPECT_CACHED_HISTORY=$check_cached_history"
    "WORKMAX_DESKTOP_SMOKE_EXPECT_BODY_TEXT=$expect_body_text"
    "WORKMAX_DESKTOP_SMOKE_EXPECT_THREAD_TEXT=$expect_thread_text"
    "WORKMAX_DESKTOP_SMOKE_EXPECT_MESSAGE_TEXT=$expect_message_text"
  )
  if [ -n "$cloud_base_override" ]; then
    launch_env+=("WORKMAX_CLOUD_BASE=$cloud_base_override")
  fi
  if [ -n "$renderer_url_override" ]; then
    launch_env+=("WORKMAX_DESKTOP_RENDERER_URL=$renderer_url_override")
  fi
  env "${launch_env[@]}" "$executable_path" >"$stdout_path" 2>"$stderr_path" &
  app_pid="$!"

  local deadline=$((SECONDS + timeout_seconds))
  local timed_out=0
  while [ "$SECONDS" -lt "$deadline" ]; do
    if [ -s "$result_path" ]; then
      break
    fi
    if ! kill -0 "$app_pid" 2>/dev/null; then
      break
    fi
    sleep 1
  done
  if [ ! -s "$result_path" ]; then
    timed_out=1
  fi

  if kill -0 "$app_pid" 2>/dev/null; then
    if [ "$timed_out" -eq 1 ]; then
      kill "$app_pid" 2>/dev/null || true
    fi
  fi
  set +e
  wait "$app_pid"
  local status="$?"
  set -e
  app_pid=""
  return "$status"
}

app_exit_status=0
launch_once "$result_json" "$data_dir" "$stdout_log" "$stderr_log" || app_exit_status="$?"

if [ -n "$expect_failure_text" ]; then
  if [ -s "$result_json" ]; then
    echo "smoke-packaged-app.sh: expected startup failure, but renderer smoke result was written" >&2
    cat "$result_json" >&2
    exit 1
  fi

  main_log="$data_dir/logs/sidecar-main.log"
  combined_log="$tmp_dir/combined.log"
  cat "$stdout_log" "$stderr_log" > "$combined_log"
  if [ -f "$main_log" ]; then
    cat "$main_log" >> "$combined_log"
  fi
  if ! grep -Fq "$expect_failure_text" "$combined_log"; then
    echo "smoke-packaged-app.sh: expected failure text not found: $expect_failure_text" >&2
    echo "stdout: $stdout_log" >&2
    echo "stderr: $stderr_log" >&2
    echo "main log: $main_log" >&2
    exit 1
  fi
  if [ "$app_exit_status" -eq 0 ]; then
    echo "smoke-packaged-app.sh: expected non-zero startup exit for renderer rejection" >&2
    exit 1
  fi
  echo "ok packaged app startup rejection: $expect_failure_text"
  exit 0
fi

if [ ! -s "$result_json" ]; then
  echo "smoke-packaged-app.sh: timed out waiting for renderer smoke result" >&2
  echo "stdout: $stdout_log" >&2
  echo "stderr: $stderr_log" >&2
  exit 1
fi
if [ "$app_exit_status" -ne 0 ]; then
  echo "smoke-packaged-app.sh: packaged app exited unexpectedly with status $app_exit_status" >&2
  echo "stdout: $stdout_log" >&2
  echo "stderr: $stderr_log" >&2
  exit 1
fi

validate_positive_result() {
  local path="$1"
  local expect_cached_history="$2"
  node - "$path" "$expected_version" "$expect_cached_history" "$expect_body_text" "$expect_thread_text" "$expect_message_text" <<'NODE'
const fs = require("node:fs");
const [path, expectedVersion, expectCachedHistoryRaw, expectedBodyText, expectedThreadText, expectedMessageText] = process.argv.slice(2);
const expectCachedHistory = expectCachedHistoryRaw === "1";
const result = JSON.parse(fs.readFileSync(path, "utf8"));

function assert(condition, message) {
  if (!condition) {
    throw new Error(message);
  }
}

assert(result.ok === true, `renderer did not load: ${result.error || result.status}`);
assert(result.smokeLocalTokenLeakDetected === false, "renderer observation leaked local token");
assert(result.smokeSensitiveLeakDetected === false, "renderer observation leaked sensitive text");
assert(result.appIsPackaged === true, "Electron did not report app.isPackaged=true");
assert(
  typeof result.expectedRendererUrl === "string" &&
    result.expectedRendererUrl.startsWith("file://") &&
    result.expectedRendererUrl.endsWith("/renderer/en/desktop/index.html"),
  `expected renderer was not bundled file entry: ${result.expectedRendererUrl}`
);
assert(
  typeof result.loadedUrl === "string" &&
    result.loadedUrl === result.expectedRendererUrl,
  `loaded URL mismatch: ${result.loadedUrl} !== ${result.expectedRendererUrl}`
);

const observation = result.rendererObservation || {};
assert(observation.locationHref === result.loadedUrl, "renderer location did not match loaded URL");
assert(observation.readyState === "complete", `renderer not complete: ${observation.readyState}`);
assert(observation.hasBridge === true, "window.workmaxLocal bridge was not exposed");
assert(Number.isInteger(observation.bridgePort), "bridge port missing");
assert(observation.appVersion === expectedVersion, `app version mismatch: ${observation.appVersion}`);
assert(
  observation.sidecarVersion === expectedVersion,
  `sidecar version mismatch: ${observation.sidecarVersion}`
);
assert(
  typeof observation.bodyTextLength === "number" && observation.bodyTextLength > 0,
  "bundled renderer body is blank"
);
if (expectedBodyText) {
  assert(
    typeof observation.bodyText === "string" &&
      observation.bodyText.includes(expectedBodyText),
    `expected body text was not visible: ${expectedBodyText}`
  );
}
if (expectCachedHistory) {
  assert(
    result.cloudBase === "http://127.0.0.1:9",
    `cached-history smoke did not force unreachable cloud base: ${result.cloudBase}`
  );
  assert(observation.cachedHistoryVisible === true, "smoke cached thread was not visible");
  assert(observation.cachedMessagesVisible === true, "smoke cached messages were not visible");
}
if (expectedThreadText) {
  assert(
    result.cloudBase === "http://127.0.0.1:9",
    `real-cache smoke did not force unreachable cloud base: ${result.cloudBase}`
  );
  assert(
    observation.expectedThreadVisible === true,
    `expected cached thread text was not visible: ${expectedThreadText}`
  );
}
if (expectedMessageText) {
  assert(
    observation.expectedMessagesVisible === true,
    `expected cached message text was not visible: ${expectedMessageText}`
  );
}
if (expectedBodyText) {
  assert(
    result.cloudBase === "http://127.0.0.1:9",
    `body-text smoke did not force unreachable cloud base: ${result.cloudBase}`
  );
}

const tokenChecks = result.localTokenRejectionChecks || [];
assert(Array.isArray(tokenChecks), "local-token rejection checks missing");
const requiredTokenCheckNames = [
  "missing",
  "wrong",
  "duplicate",
  "auth-status-missing",
  "diagnostics-wrong",
  "threads-missing",
  "renderer-log-wrong",
  "trigger-sync-missing",
];
assert(
  tokenChecks.length === requiredTokenCheckNames.length,
  `local-token rejection check count mismatch: ${tokenChecks.length}`
);
const statuses = new Map();
for (const check of tokenChecks) {
  assert(check && typeof check.name === "string", "local-token rejection check has invalid name");
  assert(!statuses.has(check.name), `duplicate local-token rejection check: ${check.name}`);
  assert(
    requiredTokenCheckNames.includes(check.name),
    `unexpected local-token rejection check: ${check.name}`
  );
  statuses.set(check.name, check.status);
}
for (const name of requiredTokenCheckNames) {
  assert(statuses.get(name) === 403, `local-token rejection failed for ${name}: ${statuses.get(name)}`);
}

const diagnostics = result.diagnosticsCheck || {};
assert(diagnostics.status === 200, `diagnostics check failed with HTTP ${diagnostics.status}`);
assert(
  diagnostics.version === expectedVersion,
  `diagnostics sidecar version mismatch: ${diagnostics.version}`
);
assert(diagnostics.integrityCheck === "ok", `diagnostics integrity_check=${diagnostics.integrityCheck}`);
assert(
  Array.isArray(diagnostics.appliedMigrations) &&
    diagnostics.appliedMigrations.length > 0 &&
    diagnostics.appliedMigrations.every((item) => typeof item === "string" && item.length > 0),
  `diagnostics applied_migrations invalid: ${JSON.stringify(diagnostics.appliedMigrations)}`
);
assert(diagnostics.dataDirReadable === true, `diagnostics data_dir not readable: ${diagnostics.dataDir}`);
assert(diagnostics.dbPathReadable === true, `diagnostics db_path not readable: ${diagnostics.dbPath}`);
assert(
  diagnostics.backupPathReadable === true,
  `diagnostics backup_path not readable: ${diagnostics.backupPath}`
);
assert(
  typeof diagnostics.heapAllocBytes === "number" && diagnostics.heapAllocBytes >= 0,
  `diagnostics heap_alloc_bytes invalid: ${diagnostics.heapAllocBytes}`
);
assert(
  typeof diagnostics.heapSysBytes === "number" && diagnostics.heapSysBytes >= 0,
  `diagnostics heap_sys_bytes invalid: ${diagnostics.heapSysBytes}`
);
assert(
  typeof diagnostics.numGoroutine === "number" && diagnostics.numGoroutine >= 1,
  `diagnostics num_goroutine invalid: ${diagnostics.numGoroutine}`
);
assert(
  typeof result.localTokenFingerprint === "string" &&
    /^[0-9a-f]{16}$/.test(result.localTokenFingerprint),
  "local-token fingerprint missing or malformed"
);

const serialized = JSON.stringify(result);
assert(!serialized.includes("X-Local-Token"), "smoke result leaked local-token header name");
assert(!serialized.includes("WORKMAX_LOCAL_TOKEN"), "smoke result leaked local-token env name");
assert(!serialized.includes("smoke-wrong-token"), "smoke result leaked wrong-token probe value");
assert(!serialized.includes("[REDACTED_LOCAL_TOKEN]"), "smoke result redacted a local-token leak");
assert(!serialized.includes("[REDACTED_SMOKE_SECRET]"), "smoke result redacted sensitive renderer text");
assert(!serialized.includes("[REDACTED_SMOKE_SECRET_KEY]"), "smoke result redacted sensitive renderer keys");

console.log(`ok packaged app smoke: ${result.loadedUrl}`);
NODE
}

validate_positive_result "$result_json" "$check_cached_history"

if [ "$check_token_rotation" -eq 1 ]; then
  second_result_json="$tmp_dir/renderer-info-2.json"
  second_stdout_log="$tmp_dir/app-2.stdout.log"
  second_stderr_log="$tmp_dir/app-2.stderr.log"
  second_exit_status=0
  launch_once "$second_result_json" "$data_dir" "$second_stdout_log" "$second_stderr_log" || second_exit_status="$?"
  if [ "$second_exit_status" -ne 0 ]; then
    echo "smoke-packaged-app.sh: second launch exited unexpectedly with status $second_exit_status" >&2
    echo "stdout: $second_stdout_log" >&2
    echo "stderr: $second_stderr_log" >&2
    exit 1
  fi
  if [ ! -s "$second_result_json" ]; then
    echo "smoke-packaged-app.sh: timed out waiting for second renderer smoke result" >&2
    echo "stdout: $second_stdout_log" >&2
    echo "stderr: $second_stderr_log" >&2
    exit 1
  fi
  validate_positive_result "$second_result_json" "$check_cached_history"
  node - "$result_json" "$second_result_json" <<'NODE'
const fs = require("node:fs");
const [firstPath, secondPath] = process.argv.slice(2);
const first = JSON.parse(fs.readFileSync(firstPath, "utf8"));
const second = JSON.parse(fs.readFileSync(secondPath, "utf8"));
if (first.localTokenFingerprint === second.localTokenFingerprint) {
  throw new Error("local-token fingerprint did not rotate across packaged launches");
}
console.log("ok packaged local-token rotation");
NODE
fi
