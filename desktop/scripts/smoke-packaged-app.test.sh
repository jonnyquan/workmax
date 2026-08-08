#!/usr/bin/env bash
#
# Regression tests for smoke-packaged-app.sh validation paths that fail before
# the helper launches Electron.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SMOKE="$SCRIPT_DIR/smoke-packaged-app.sh"

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/workmax-smoke-packaged-app-test.XXXXXX")"
trap 'rm -rf "$tmp_dir"' EXIT

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

expect_pass_contains() {
  local name="$1"
  local want="$2"
  shift 2
  local output
  if ! output="$("$SMOKE" "$@" 2>&1)"; then
    printf 'not ok - %s\n%s\n' "$name" "$output" >&2
    exit 1
  fi
  if ! grep -Fq -- "$want" <<<"$output"; then
    printf 'not ok - %s: missing expected text: %s\n%s\n' "$name" "$want" "$output" >&2
    exit 1
  fi
  printf 'ok - %s\n' "$name"
}

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

fake_app="$tmp_dir/WorkMax Desktop.app"
fake_macos="$fake_app/Contents/MacOS"
mkdir -p "$fake_macos"

expect_pass "help exits before validation" --help

expect_fail "requires app path" "Usage:" 
expect_fail "rejects missing timeout value" "--timeout requires a value" \
  --timeout
expect_fail "rejects option-looking timeout value" "--timeout requires a value" \
  --timeout --renderer-url "$fake_app"
expect_fail "rejects missing renderer-url value" "--renderer-url requires a value" \
  --renderer-url
expect_fail "rejects missing data-dir value" "--data-dir requires a value" \
  --data-dir
expect_fail "rejects missing body text value" "--expect-body-text requires a value" \
  --expect-body-text
expect_fail "rejects unknown option" "unknown option" --bogus "$fake_app"
expect_fail "rejects low timeout" "--timeout must be an integer >= 5" \
  --timeout 4 "$fake_app"
expect_fail "rejects non-numeric timeout" "--timeout must be an integer >= 5" \
  --timeout soon "$fake_app"
expect_fail "requires renderer-url pair" "--renderer-url and --expect-failure must be used together" \
  --renderer-url https://workmax.app/ "$fake_app"
expect_fail "requires expect-failure pair" "--renderer-url and --expect-failure must be used together" \
  --expect-failure "desktop renderer URL must point" "$fake_app"
expect_fail "rejects token rotation in negative mode" "--check-token-rotation cannot be combined with negative mode" \
  --check-token-rotation --renderer-url https://workmax.app/ --expect-failure "bad route" "$fake_app"
expect_fail "rejects cached history in negative mode" "--check-cached-history cannot be combined with negative mode" \
  --check-cached-history --renderer-url https://workmax.app/ --expect-failure "bad route" "$fake_app"
expect_fail "requires thread text before message text" "--expect-message-text requires --expect-thread-text" \
  --expect-message-text "cached answer" "$fake_app"
expect_fail "rejects cached text assertions in negative mode" "body/cache text assertions cannot be combined with negative mode" \
  --expect-thread-text "Cached Thread" --renderer-url https://workmax.app/ --expect-failure "bad route" "$fake_app"
expect_fail "rejects body text assertions in negative mode" "body/cache text assertions cannot be combined with negative mode" \
  --expect-body-text "Auth state: unauthenticated" --renderer-url https://workmax.app/ --expect-failure "bad route" "$fake_app"
expect_fail "requires explicit data dir for cached text assertions" "body/cache text assertions require --data-dir" \
  --expect-thread-text "Cached Thread" "$fake_app"
expect_fail "requires explicit data dir for body text assertions" "body/cache text assertions require --data-dir" \
  --expect-body-text "Auth state: unauthenticated" "$fake_app"
existing_data_dir="$tmp_dir/existing-data-dir"
mkdir -p "$existing_data_dir"
expect_fail "rejects explicit data dir without text assertion" "--data-dir requires --expect-body-text or --expect-thread-text" \
  --data-dir "$existing_data_dir" "$fake_app"
expect_fail "rejects missing explicit data dir" "--data-dir must point to an existing directory" \
  --data-dir "$tmp_dir/missing-data-dir" --expect-body-text "Auth state: unauthenticated" "$fake_app"
expect_fail "rejects non-app bundle" "not a macOS .app bundle" \
  "$tmp_dir/not-an-app"
expect_fail "rejects missing Info.plist" "missing Info.plist" \
  "$fake_app"

cat >"$fake_app/Contents/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
</dict>
</plist>
PLIST

expect_fail "rejects missing CFBundleExecutable" "CFBundleExecutable missing" \
  "$fake_app"

make_launchable_fake_app() {
  local app="$1"
  rm -rf "$app"
  mkdir -p "$app/Contents/MacOS"
  cat >"$app/Contents/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleExecutable</key>
  <string>FakeWorkMax</string>
</dict>
</plist>
PLIST
  cat >"$app/Contents/MacOS/FakeWorkMax" <<'SH'
#!/usr/bin/env bash
echo "desktop renderer URL must point to /desktop" >&2
exit 1
SH
  chmod +x "$app/Contents/MacOS/FakeWorkMax"
}

make_positive_result_fake_app() {
  local app="$1"
  local expected_version
  expected_version="$(cd "$SCRIPT_DIR/../electron" && node -p "require('./package.json').version")"
  rm -rf "$app"
  mkdir -p "$app/Contents/MacOS"
  cat >"$app/Contents/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleExecutable</key>
  <string>FakeWorkMax</string>
</dict>
</plist>
PLIST
  cat >"$app/Contents/MacOS/FakeWorkMax" <<SH
#!/usr/bin/env bash
set -euo pipefail
: "\${WORKMAX_DESKTOP_SMOKE_RENDERER_INFO:?}"
expected_version="$expected_version"
renderer_url="file:///tmp/workmax-smoke/renderer/en/desktop/index.html"
leak_flag="\${WORKMAX_TEST_FAKE_LEAK_FLAG:-false}"
sensitive_leak_flag="\${WORKMAX_TEST_FAKE_SENSITIVE_LEAK_FLAG:-false}"
omit_sensitive_leak_flag="\${WORKMAX_TEST_FAKE_OMIT_SENSITIVE_LEAK_FLAG:-0}"
sensitive_leak_field="\"smokeSensitiveLeakDetected\": \$sensitive_leak_flag,"
if [ "\$omit_sensitive_leak_flag" = "1" ]; then
  sensitive_leak_field=""
fi
body_text="\${WORKMAX_TEST_FAKE_BODY_TEXT:-Smoke ready}"
expected_thread_visible="\${WORKMAX_TEST_FAKE_EXPECTED_THREAD_VISIBLE:-false}"
expected_messages_visible="\${WORKMAX_TEST_FAKE_EXPECTED_MESSAGES_VISIBLE:-false}"
extra_token_check=""
if [ "\${WORKMAX_TEST_FAKE_DUPLICATE_TOKEN_CHECK:-0}" = "1" ]; then
  extra_token_check=',{"name":"missing","status":403}'
fi
if [ "\${WORKMAX_TEST_FAKE_UNKNOWN_TOKEN_CHECK:-0}" = "1" ]; then
  extra_token_check=',{"name":"unexpected","status":403}'
fi
if [ "\${WORKMAX_TEST_EXPECT_NO_EMPTY_OVERRIDES:-0}" = "1" ]; then
  if [ "\${WORKMAX_CLOUD_BASE+x}" = "x" ] && [ -z "\${WORKMAX_CLOUD_BASE}" ]; then
    echo "WORKMAX_CLOUD_BASE was injected as an empty override" >&2
    exit 9
  fi
  if [ "\${WORKMAX_DESKTOP_RENDERER_URL+x}" = "x" ] && [ -z "\${WORKMAX_DESKTOP_RENDERER_URL}" ]; then
    echo "WORKMAX_DESKTOP_RENDERER_URL was injected as an empty override" >&2
    exit 9
  fi
fi
cat >"\$WORKMAX_DESKTOP_SMOKE_RENDERER_INFO" <<JSON
{
  "ok": true,
  "appIsPackaged": true,
  "expectedRendererUrl": "\$renderer_url",
  "loadedUrl": "\$renderer_url",
  "cloudBase": "\${WORKMAX_CLOUD_BASE:-}",
  "smokeLocalTokenLeakDetected": \$leak_flag,
  \$sensitive_leak_field
  "rendererObservation": {
    "locationHref": "\$renderer_url",
    "readyState": "complete",
    "hasBridge": true,
    "bridgePort": 49152,
    "appVersion": "\$expected_version",
    "sidecarVersion": "\$expected_version",
    "bodyTextLength": 16,
    "bodyText": "\$body_text",
    "expectedThreadVisible": \$expected_thread_visible,
    "expectedMessagesVisible": \$expected_messages_visible
  },
  "localTokenRejectionChecks": [
    {"name": "missing", "status": 403},
    {"name": "wrong", "status": 403},
    {"name": "duplicate", "status": 403},
    {"name": "auth-status-missing", "status": 403},
    {"name": "diagnostics-wrong", "status": 403},
    {"name": "threads-missing", "status": 403},
    {"name": "renderer-log-wrong", "status": 403},
    {"name": "trigger-sync-missing", "status": 403}\$extra_token_check
  ],
  "diagnosticsCheck": {
    "status": 200,
    "version": "\$expected_version",
    "integrityCheck": "ok",
    "dataDir": "/tmp",
    "dbPath": "/bin/sh",
    "backupPath": "/bin/sh",
    "dataDirReadable": true,
    "dbPathReadable": true,
    "backupPathReadable": true,
    "appliedMigrations": ["0001_init_workagent_tables"],
    "heapAllocBytes": 1,
    "heapSysBytes": 1,
    "numGoroutine": 1
  },
  "localTokenFingerprint": "0123456789abcdef"
}
JSON
exit "\${WORKMAX_TEST_FAKE_EXIT_STATUS:?}"
SH
  chmod +x "$app/Contents/MacOS/FakeWorkMax"
}

make_codesign_stub() {
  local mode="$1"
  local dir="$tmp_dir/codesign-$mode"
  mkdir -p "$dir"
  cat >"$dir/codesign" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
mode="${WORKMAX_TEST_CODESIGN_MODE:?}"
log="${WORKMAX_TEST_CODESIGN_LOG:?}"
printf '%s\n' "$*" >>"$log"

if [ "${1:-}" = "--verify" ]; then
  count_file="${log}.verify-count"
  count=0
  if [ -f "$count_file" ]; then
    count="$(cat "$count_file")"
  fi
  count=$((count + 1))
  printf '%s' "$count" >"$count_file"

  case "$mode" in
    valid)
      exit 0
      ;;
    adhoc)
      if [ "$count" -eq 1 ]; then
        echo "stub verify failed" >&2
        exit 1
      fi
      exit 0
      ;;
    invalid)
      echo "stub verify failed" >&2
      exit 1
      ;;
  esac
fi

if [ "${1:-}" = "-dv" ]; then
  case "$mode" in
    adhoc)
      echo "Signature=adhoc" >&2
      echo "TeamIdentifier=not set" >&2
      ;;
    invalid)
      echo "Signature size=1234" >&2
      echo "TeamIdentifier=ABCDE12345" >&2
      ;;
  esac
  exit 0
fi

if [ "${1:-}" = "--force" ]; then
  exit 0
fi

echo "unexpected codesign invocation: $*" >&2
exit 2
SH
  chmod +x "$dir/codesign"
  printf '%s\n' "$dir"
}

signature_app="$tmp_dir/signature-test.app"
make_launchable_fake_app "$signature_app"

codesign_stub_dir="$(make_codesign_stub valid)"
WORKMAX_TEST_CODESIGN_MODE=valid \
WORKMAX_TEST_CODESIGN_LOG="$tmp_dir/codesign-valid.log" \
PATH="$codesign_stub_dir:$PATH" \
  expect_pass "accepts already valid app signature before negative launch" \
  --renderer-url "https://workmax.app/" \
  --expect-failure "desktop renderer URL must point" \
  "$signature_app"
grep -Fq -- "--verify --deep --strict" "$tmp_dir/codesign-valid.log" || {
  printf 'not ok - valid signature path did not run strict verification\n' >&2
  exit 1
}

codesign_stub_dir="$(make_codesign_stub adhoc)"
WORKMAX_TEST_CODESIGN_MODE=adhoc \
WORKMAX_TEST_CODESIGN_LOG="$tmp_dir/codesign-adhoc.log" \
PATH="$codesign_stub_dir:$PATH" \
  expect_pass_contains "repairs ad-hoc app signature before controlled smoke" \
  "repairing unsigned/ad-hoc app signature" \
  --renderer-url "https://workmax.app/" \
  --expect-failure "desktop renderer URL must point" \
  "$signature_app"
grep -Fq -- "--force --deep --sign -" "$tmp_dir/codesign-adhoc.log" || {
  printf 'not ok - ad-hoc signature path did not request local repair\n' >&2
  exit 1
}

codesign_stub_dir="$(make_codesign_stub invalid)"
WORKMAX_TEST_CODESIGN_MODE=invalid \
WORKMAX_TEST_CODESIGN_LOG="$tmp_dir/codesign-invalid.log" \
PATH="$codesign_stub_dir:$PATH" \
  expect_fail "rejects non-ad-hoc signature verification failures" \
  "app signature verification failed and will not be auto-repaired" \
  --renderer-url "https://workmax.app/" \
  --expect-failure "desktop renderer URL must point" \
  "$signature_app"

positive_exit_app="$tmp_dir/positive-exit-test.app"
make_positive_result_fake_app "$positive_exit_app"
codesign_stub_dir="$(make_codesign_stub valid)"
WORKMAX_TEST_CODESIGN_MODE=valid \
WORKMAX_TEST_CODESIGN_LOG="$tmp_dir/codesign-positive-exit.log" \
WORKMAX_TEST_FAKE_EXIT_STATUS=7 \
PATH="$codesign_stub_dir:$PATH" \
  expect_fail "rejects non-zero positive app exit after renderer result" \
  "packaged app exited unexpectedly with status 7" \
  "$positive_exit_app"

positive_default_env_app="$tmp_dir/positive-default-env-test.app"
make_positive_result_fake_app "$positive_default_env_app"
codesign_stub_dir="$(make_codesign_stub valid)"
WORKMAX_TEST_CODESIGN_MODE=valid \
WORKMAX_TEST_CODESIGN_LOG="$tmp_dir/codesign-positive-default-env.log" \
WORKMAX_TEST_FAKE_EXIT_STATUS=0 \
WORKMAX_TEST_EXPECT_NO_EMPTY_OVERRIDES=1 \
PATH="$codesign_stub_dir:$PATH" \
  expect_pass "does not inject empty cloud or renderer overrides by default" \
  "$positive_default_env_app"

positive_real_cache_app="$tmp_dir/positive-real-cache-test.app"
positive_real_cache_data_dir="$tmp_dir/real-cache-data"
mkdir -p "$positive_real_cache_data_dir"
make_positive_result_fake_app "$positive_real_cache_app"
codesign_stub_dir="$(make_codesign_stub valid)"
WORKMAX_TEST_CODESIGN_MODE=valid \
WORKMAX_TEST_CODESIGN_LOG="$tmp_dir/codesign-positive-real-cache.log" \
WORKMAX_TEST_FAKE_EXIT_STATUS=0 \
WORKMAX_TEST_FAKE_EXPECTED_THREAD_VISIBLE=true \
WORKMAX_TEST_FAKE_EXPECTED_MESSAGES_VISIBLE=true \
PATH="$codesign_stub_dir:$PATH" \
  expect_pass "accepts real-cache text assertions with explicit data dir" \
  --data-dir "$positive_real_cache_data_dir" \
  --expect-thread-text "Cached Thread" \
  --expect-message-text "Cached answer" \
  "$positive_real_cache_app"

positive_body_text_app="$tmp_dir/positive-body-text-test.app"
positive_body_text_data_dir="$tmp_dir/body-text-data"
mkdir -p "$positive_body_text_data_dir"
make_positive_result_fake_app "$positive_body_text_app"
codesign_stub_dir="$(make_codesign_stub valid)"
WORKMAX_TEST_CODESIGN_MODE=valid \
WORKMAX_TEST_CODESIGN_LOG="$tmp_dir/codesign-positive-body-text.log" \
WORKMAX_TEST_FAKE_EXIT_STATUS=0 \
WORKMAX_TEST_FAKE_BODY_TEXT="Auth state: unauthenticated. Sign in to sync cloud history. No cached threads yet." \
PATH="$codesign_stub_dir:$PATH" \
  expect_pass "accepts expected body text assertion with explicit data dir" \
  --data-dir "$positive_body_text_data_dir" \
  --expect-body-text "Auth state: unauthenticated" \
  "$positive_body_text_app"

positive_leak_flag_app="$tmp_dir/positive-leak-flag-test.app"
make_positive_result_fake_app "$positive_leak_flag_app"
codesign_stub_dir="$(make_codesign_stub valid)"
WORKMAX_TEST_CODESIGN_MODE=valid \
WORKMAX_TEST_CODESIGN_LOG="$tmp_dir/codesign-positive-leak-flag.log" \
WORKMAX_TEST_FAKE_EXIT_STATUS=0 \
WORKMAX_TEST_FAKE_LEAK_FLAG=true \
PATH="$codesign_stub_dir:$PATH" \
  expect_fail "rejects positive smoke result with local-token leak flag" \
  "renderer observation leaked local token" \
  "$positive_leak_flag_app"

positive_redaction_marker_app="$tmp_dir/positive-redaction-marker-test.app"
make_positive_result_fake_app "$positive_redaction_marker_app"
codesign_stub_dir="$(make_codesign_stub valid)"
WORKMAX_TEST_CODESIGN_MODE=valid \
WORKMAX_TEST_CODESIGN_LOG="$tmp_dir/codesign-positive-redaction-marker.log" \
WORKMAX_TEST_FAKE_EXIT_STATUS=0 \
WORKMAX_TEST_FAKE_BODY_TEXT="[REDACTED_LOCAL_TOKEN]" \
PATH="$codesign_stub_dir:$PATH" \
  expect_fail "rejects positive smoke result containing redaction marker" \
  "smoke result redacted a local-token leak" \
  "$positive_redaction_marker_app"

positive_sensitive_leak_flag_app="$tmp_dir/positive-sensitive-leak-flag-test.app"
make_positive_result_fake_app "$positive_sensitive_leak_flag_app"
codesign_stub_dir="$(make_codesign_stub valid)"
WORKMAX_TEST_CODESIGN_MODE=valid \
WORKMAX_TEST_CODESIGN_LOG="$tmp_dir/codesign-positive-sensitive-leak-flag.log" \
WORKMAX_TEST_FAKE_EXIT_STATUS=0 \
WORKMAX_TEST_FAKE_SENSITIVE_LEAK_FLAG=true \
PATH="$codesign_stub_dir:$PATH" \
  expect_fail "rejects positive smoke result with sensitive leak flag" \
  "renderer observation leaked sensitive text" \
  "$positive_sensitive_leak_flag_app"

positive_missing_sensitive_flag_app="$tmp_dir/positive-missing-sensitive-flag-test.app"
make_positive_result_fake_app "$positive_missing_sensitive_flag_app"
codesign_stub_dir="$(make_codesign_stub valid)"
WORKMAX_TEST_CODESIGN_MODE=valid \
WORKMAX_TEST_CODESIGN_LOG="$tmp_dir/codesign-positive-missing-sensitive-flag.log" \
WORKMAX_TEST_FAKE_EXIT_STATUS=0 \
WORKMAX_TEST_FAKE_OMIT_SENSITIVE_LEAK_FLAG=1 \
PATH="$codesign_stub_dir:$PATH" \
  expect_fail "rejects positive smoke result missing sensitive leak flag" \
  "renderer observation leaked sensitive text" \
  "$positive_missing_sensitive_flag_app"

positive_sensitive_redaction_marker_app="$tmp_dir/positive-sensitive-redaction-marker-test.app"
make_positive_result_fake_app "$positive_sensitive_redaction_marker_app"
codesign_stub_dir="$(make_codesign_stub valid)"
WORKMAX_TEST_CODESIGN_MODE=valid \
WORKMAX_TEST_CODESIGN_LOG="$tmp_dir/codesign-positive-sensitive-redaction-marker.log" \
WORKMAX_TEST_FAKE_EXIT_STATUS=0 \
WORKMAX_TEST_FAKE_BODY_TEXT="[REDACTED_SMOKE_SECRET]" \
PATH="$codesign_stub_dir:$PATH" \
  expect_fail "rejects positive smoke result containing sensitive redaction marker" \
  "smoke result redacted sensitive renderer text" \
  "$positive_sensitive_redaction_marker_app"

positive_duplicate_token_check_app="$tmp_dir/positive-duplicate-token-check-test.app"
make_positive_result_fake_app "$positive_duplicate_token_check_app"
codesign_stub_dir="$(make_codesign_stub valid)"
WORKMAX_TEST_CODESIGN_MODE=valid \
WORKMAX_TEST_CODESIGN_LOG="$tmp_dir/codesign-positive-duplicate-token-check.log" \
WORKMAX_TEST_FAKE_EXIT_STATUS=0 \
WORKMAX_TEST_FAKE_DUPLICATE_TOKEN_CHECK=1 \
PATH="$codesign_stub_dir:$PATH" \
  expect_fail "rejects duplicate local-token rejection evidence" \
  "local-token rejection check count mismatch" \
  "$positive_duplicate_token_check_app"

positive_unknown_token_check_app="$tmp_dir/positive-unknown-token-check-test.app"
make_positive_result_fake_app "$positive_unknown_token_check_app"
codesign_stub_dir="$(make_codesign_stub valid)"
WORKMAX_TEST_CODESIGN_MODE=valid \
WORKMAX_TEST_CODESIGN_LOG="$tmp_dir/codesign-positive-unknown-token-check.log" \
WORKMAX_TEST_FAKE_EXIT_STATUS=0 \
WORKMAX_TEST_FAKE_UNKNOWN_TOKEN_CHECK=1 \
PATH="$codesign_stub_dir:$PATH" \
  expect_fail "rejects unexpected local-token rejection evidence" \
  "local-token rejection check count mismatch" \
  "$positive_unknown_token_check_app"
