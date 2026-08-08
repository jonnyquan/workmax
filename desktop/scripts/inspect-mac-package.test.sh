#!/usr/bin/env bash
#
# Regression tests for inspect-mac-package.sh using disposable fake .app bundles.
# These fixtures are intentionally minimal: enough bundle shape to exercise the
# structural inspector without launching Electron or requiring real package output.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && cd .. && pwd)"
INSPECTOR="$SCRIPT_DIR/inspect-mac-package.sh"

expected_version="$(cd "$REPO_ROOT/desktop/electron" && node -p "require('./package.json').version")"
expected_name="$(cd "$REPO_ROOT/desktop/electron" && node -p "require('./package.json').name")"
expected_main="$(cd "$REPO_ROOT/desktop/electron" && node -p "require('./package.json').main")"

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/workmax-inspect-mac-package-test.XXXXXX")"
trap 'rm -rf "$tmp_dir"' EXIT

runtime_entries=(
  "dist/main.js"
  "dist/main-log.js"
  "dist/desktop-bridge.js"
  "dist/oauth-window.js"
  "dist/preload.js"
  "dist/renderer-loader.js"
  "dist/security-helpers.js"
  "dist/sidecar-manager.js"
  "dist/smoke-diagnostics.js"
)

write_fixture() {
  local app_path="$1"
  mkdir -p \
    "$app_path/Contents/MacOS" \
    "$app_path/Contents/Resources/app" \
    "$app_path/Contents/Resources/third-party-licenses/electron" \
    "$app_path/Contents/Resources/third-party-licenses/github.com/example/dependency"

  cat > "$app_path/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleIdentifier</key>
  <string>ai.workmax.desktop</string>
  <key>CFBundleShortVersionString</key>
  <string>$expected_version</string>
  <key>CFBundleExecutable</key>
  <string>WorkMax Desktop</string>
</dict>
</plist>
PLIST

  printf '#!/usr/bin/env bash\nexit 0\n' > "$app_path/Contents/MacOS/WorkMax Desktop"
  chmod +x "$app_path/Contents/MacOS/WorkMax Desktop"

  cat > "$app_path/Contents/Resources/app/package.json" <<JSON
{
  "name": "$expected_name",
  "version": "$expected_version",
  "main": "$expected_main"
}
JSON

  for entry in "${runtime_entries[@]}"; do
    mkdir -p "$(dirname "$app_path/Contents/Resources/app/$entry")"
    printf 'module.exports = {}\n' > "$app_path/Contents/Resources/app/$entry"
  done

  printf 'fake sidecar version marker %s\n' "$expected_version" > "$app_path/Contents/Resources/workagent-desktop"
  chmod +x "$app_path/Contents/Resources/workagent-desktop"
  cp "$REPO_ROOT/LICENSE" "$app_path/Contents/Resources/LICENSE"
  cp "$REPO_ROOT/THIRD_PARTY_NOTICES.md" "$app_path/Contents/Resources/THIRD_PARTY_NOTICES.md"
  cp "$REPO_ROOT/desktop/electron/node_modules/electron/dist/LICENSE" \
    "$app_path/Contents/Resources/third-party-licenses/electron/LICENSE"
  cp "$REPO_ROOT/desktop/electron/node_modules/electron/dist/LICENSES.chromium.html" \
    "$app_path/Contents/Resources/third-party-licenses/electron/LICENSES.chromium.html"
  printf 'fixture dependency license\n' > \
    "$app_path/Contents/Resources/third-party-licenses/github.com/example/dependency/LICENSE"
}

write_fake_icns() {
  local icon_path="$1"
  printf 'icnsfake icon bytes\n' > "$icon_path"
}

write_renderer_fixture() {
  local renderer_dir="$1"
  mkdir -p "$renderer_dir"
  cat > "$renderer_dir/index.html" <<'HTML'
<!doctype html>
<meta http-equiv="Content-Security-Policy" content="default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src http://127.0.0.1:*; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'">
<link rel="stylesheet" href="./styles.css">
<a id="source-code-link" href="https://github.com/jonnyquan/workmax">Source code · AGPL-3.0</a>
<script src="./renderer.js"></script>
HTML
  printf 'body { background: #0b0b0e; }\n' > "$renderer_dir/styles.css"
  printf 'window.__workmaxBundledRenderer = true;\n' > "$renderer_dir/renderer.js"
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

if [ "${1:-}" = "-dv" ]; then
  case "$mode" in
    developer-id)
      echo "Signature size=9000" >&2
      echo "Authority=Developer ID Application: WorkMax Inc. (ABCDE12345)" >&2
      echo "Authority=Developer ID Certification Authority" >&2
      echo "Authority=Apple Root CA" >&2
      echo "TeamIdentifier=ABCDE12345" >&2
      echo "Runtime Version=14.0.0" >&2
      ;;
    apple-development)
      echo "Signature size=9000" >&2
      echo "Authority=Apple Development: WorkMax Dev (ABCDE12345)" >&2
      echo "TeamIdentifier=ABCDE12345" >&2
      echo "Runtime Version=14.0.0" >&2
      ;;
    adhoc)
      echo "Signature=adhoc" >&2
      echo "TeamIdentifier=not set" >&2
      echo "Runtime Version=14.0.0" >&2
      ;;
    no-team)
      echo "Signature size=9000" >&2
      echo "TeamIdentifier=not set" >&2
      echo "Runtime Version=14.0.0" >&2
      ;;
    no-runtime)
      echo "Signature size=9000" >&2
      echo "TeamIdentifier=ABCDE12345" >&2
      ;;
    bad-verify)
      echo "Signature size=9000" >&2
      echo "Authority=Developer ID Application: WorkMax Inc. (ABCDE12345)" >&2
      echo "TeamIdentifier=ABCDE12345" >&2
      echo "Runtime Version=14.0.0" >&2
      ;;
  esac
  exit 0
fi

if [ "${1:-}" = "--verify" ]; then
  if [ "$mode" = "bad-verify" ]; then
    echo "stub strict verify failed" >&2
    exit 1
  fi
  exit 0
fi

echo "unexpected codesign invocation: $*" >&2
exit 2
SH
  chmod +x "$dir/codesign"
  cat >"$dir/spctl" <<'SH'
#!/usr/bin/env bash
exit 0
SH
  chmod +x "$dir/spctl"
  printf '%s\n' "$dir"
}

pack_fixture_asar() {
  local app_path="$1"
  node -e "const asar=require(process.argv[1]); asar.createPackage(process.argv[2], process.argv[3]).catch((error) => { console.error(error); process.exit(1); })" \
    "$REPO_ROOT/desktop/electron/node_modules/@electron/asar" \
    "$app_path/Contents/Resources/app" \
    "$app_path/Contents/Resources/app.asar"
  rm -rf "$app_path/Contents/Resources/app"
}

expect_pass() {
  local name="$1"
  local app_path="$2"
  shift 2
  local output
  if ! output="$("$INSPECTOR" "$@" "$app_path" 2>&1)"; then
    printf 'not ok - %s\n%s\n' "$name" "$output" >&2
    exit 1
  fi
  printf 'ok - %s\n' "$name"
}

expect_fail() {
  local name="$1"
  local app_path="$2"
  local want="$3"
  shift 3
  local output
  if output="$("$INSPECTOR" "$@" "$app_path" 2>&1)"; then
    printf 'not ok - %s: inspector unexpectedly passed\n%s\n' "$name" "$output" >&2
    exit 1
  fi
  if ! grep -Fq "$want" <<<"$output"; then
    printf 'not ok - %s: missing expected failure text: %s\n%s\n' "$name" "$want" "$output" >&2
    exit 1
  fi
  printf 'ok - %s\n' "$name"
}

valid_app="$tmp_dir/valid/WorkMax Desktop.app"
write_fixture "$valid_app"
expect_pass "valid unpacked payload" "$valid_app"

missing_license_app="$tmp_dir/missing-license/WorkMax Desktop.app"
write_fixture "$missing_license_app"
rm "$missing_license_app/Contents/Resources/LICENSE"
expect_fail "rejects missing WorkMax license" \
  "$missing_license_app" \
  "missing release license material"

drifted_notices_app="$tmp_dir/drifted-notices/WorkMax Desktop.app"
write_fixture "$drifted_notices_app"
printf '\nmodified\n' >> "$drifted_notices_app/Contents/Resources/THIRD_PARTY_NOTICES.md"
expect_fail "rejects drifted third-party notices" \
  "$drifted_notices_app" \
  "packaged THIRD_PARTY_NOTICES.md does not match"

missing_go_licenses_app="$tmp_dir/missing-go-licenses/WorkMax Desktop.app"
write_fixture "$missing_go_licenses_app"
rm -rf "$missing_go_licenses_app/Contents/Resources/third-party-licenses/github.com"
expect_fail "rejects missing Go dependency licenses" \
  "$missing_go_licenses_app" \
  "generated Go dependency license bundle is missing or empty"

drifted_electron_license_app="$tmp_dir/drifted-electron-license/WorkMax Desktop.app"
write_fixture "$drifted_electron_license_app"
printf '\nmodified\n' >> "$drifted_electron_license_app/Contents/Resources/third-party-licenses/electron/LICENSE"
expect_fail "rejects drifted Electron license" \
  "$drifted_electron_license_app" \
  "packaged third-party notice differs from upstream"

require_bundled_renderer_missing_app="$tmp_dir/require-bundled-renderer-missing/WorkMax Desktop.app"
write_fixture "$require_bundled_renderer_missing_app"
expect_fail "require-bundled-renderer rejects missing renderer entry" \
  "$require_bundled_renderer_missing_app" \
  "missing bundled renderer file" \
  --require-bundled-renderer

require_bundled_renderer_app="$tmp_dir/require-bundled-renderer-present/WorkMax Desktop.app"
write_fixture "$require_bundled_renderer_app"
write_renderer_fixture "$require_bundled_renderer_app/Contents/Resources/renderer/en/desktop"
expect_pass "require-bundled-renderer accepts renderer entry" \
  "$require_bundled_renderer_app" \
  --require-bundled-renderer

require_bundled_renderer_missing_asset_app="$tmp_dir/require-bundled-renderer-missing-asset/WorkMax Desktop.app"
write_fixture "$require_bundled_renderer_missing_asset_app"
write_renderer_fixture "$require_bundled_renderer_missing_asset_app/Contents/Resources/renderer/en/desktop"
rm "$require_bundled_renderer_missing_asset_app/Contents/Resources/renderer/en/desktop/renderer.js"
expect_fail "require-bundled-renderer rejects missing renderer asset" \
  "$require_bundled_renderer_missing_asset_app" \
  "missing bundled renderer file" \
  --require-bundled-renderer

require_bundled_renderer_bad_csp_app="$tmp_dir/require-bundled-renderer-bad-csp/WorkMax Desktop.app"
write_fixture "$require_bundled_renderer_bad_csp_app"
write_renderer_fixture "$require_bundled_renderer_bad_csp_app/Contents/Resources/renderer/en/desktop"
perl -0pi -e 's/connect-src http:\/\/127\.0\.0\.1:\*/connect-src https:\/\/workmax.app/' \
  "$require_bundled_renderer_bad_csp_app/Contents/Resources/renderer/en/desktop/index.html"
expect_fail "require-bundled-renderer rejects non-loopback CSP" \
  "$require_bundled_renderer_bad_csp_app" \
  "CSP directive connect-src mismatch" \
  --require-bundled-renderer

require_bundled_renderer_extra_csp_app="$tmp_dir/require-bundled-renderer-extra-csp/WorkMax Desktop.app"
write_fixture "$require_bundled_renderer_extra_csp_app"
write_renderer_fixture "$require_bundled_renderer_extra_csp_app/Contents/Resources/renderer/en/desktop"
perl -0pi -e 's/connect-src http:\/\/127\.0\.0\.1:\*/connect-src http:\/\/127.0.0.1:* https:\/\/workmax.app/' \
  "$require_bundled_renderer_extra_csp_app/Contents/Resources/renderer/en/desktop/index.html"
expect_fail "require-bundled-renderer rejects extra remote CSP connect source" \
  "$require_bundled_renderer_extra_csp_app" \
  "CSP directive connect-src mismatch" \
  --require-bundled-renderer

require_bundled_renderer_secret_app="$tmp_dir/require-bundled-renderer-secret/WorkMax Desktop.app"
write_fixture "$require_bundled_renderer_secret_app"
write_renderer_fixture "$require_bundled_renderer_secret_app/Contents/Resources/renderer/en/desktop"
printf 'const token = "access_token=leak";\n' >> "$require_bundled_renderer_secret_app/Contents/Resources/renderer/en/desktop/renderer.js"
expect_fail "require-bundled-renderer rejects embedded token-like strings" \
  "$require_bundled_renderer_secret_app" \
  "must not embed token-like secrets" \
  --require-bundled-renderer

require_bundled_renderer_extra_file_app="$tmp_dir/require-bundled-renderer-extra-file/WorkMax Desktop.app"
write_fixture "$require_bundled_renderer_extra_file_app"
write_renderer_fixture "$require_bundled_renderer_extra_file_app/Contents/Resources/renderer/en/desktop"
printf 'debug\n' > "$require_bundled_renderer_extra_file_app/Contents/Resources/renderer/en/desktop/renderer.js.map"
expect_fail "require-bundled-renderer rejects unexpected renderer files" \
  "$require_bundled_renderer_extra_file_app" \
  "bundled renderer contains unexpected files" \
  --require-bundled-renderer

require_app_icon_missing_app="$tmp_dir/require-app-icon-missing/WorkMax Desktop.app"
write_fixture "$require_app_icon_missing_app"
expect_fail "require-app-icon rejects missing icon plist entry" \
  "$require_app_icon_missing_app" \
  "CFBundleIconFile mismatch" \
  --require-app-icon

require_app_icon_present_app="$tmp_dir/require-app-icon-present/WorkMax Desktop.app"
write_fixture "$require_app_icon_present_app"
/usr/libexec/PlistBuddy -c "Add :CFBundleIconFile string icon.icns" \
  "$require_app_icon_present_app/Contents/Info.plist"
write_fake_icns "$require_app_icon_present_app/Contents/Resources/icon.icns"
expect_pass "require-app-icon accepts custom icon" \
  "$require_app_icon_present_app" \
  --require-app-icon

require_app_icon_invalid_app="$tmp_dir/require-app-icon-invalid/WorkMax Desktop.app"
write_fixture "$require_app_icon_invalid_app"
/usr/libexec/PlistBuddy -c "Add :CFBundleIconFile string icon.icns" \
  "$require_app_icon_invalid_app/Contents/Info.plist"
printf 'not an icns file\n' > "$require_app_icon_invalid_app/Contents/Resources/icon.icns"
expect_fail "require-app-icon rejects non-icns icon file" \
  "$require_app_icon_invalid_app" \
  "invalid app icon header" \
  --require-app-icon

require_all_release_assets_app="$tmp_dir/require-release-assets/WorkMax Desktop.app"
write_fixture "$require_all_release_assets_app"
/usr/libexec/PlistBuddy -c "Add :CFBundleIconFile string icon.icns" \
  "$require_all_release_assets_app/Contents/Info.plist"
write_fake_icns "$require_all_release_assets_app/Contents/Resources/icon.icns"
write_renderer_fixture "$require_all_release_assets_app/Contents/Resources/renderer/en/desktop"
expect_pass "accepts combined public-release asset requirements" \
  "$require_all_release_assets_app" \
  --require-bundled-renderer \
  --require-app-icon

developer_id_app="$tmp_dir/developer-id-signature/WorkMax Desktop.app"
write_fixture "$developer_id_app"
codesign_stub_dir="$(make_codesign_stub developer-id)"
WORKMAX_TEST_CODESIGN_MODE=developer-id \
WORKMAX_TEST_CODESIGN_LOG="$tmp_dir/codesign-developer-id.log" \
PATH="$codesign_stub_dir:$PATH" \
  expect_pass "require-developer-id-signature accepts Developer ID Application signature" \
  "$developer_id_app" \
  --require-developer-id-signature

apple_development_signature_app="$tmp_dir/apple-development-signature/WorkMax Desktop.app"
write_fixture "$apple_development_signature_app"
codesign_stub_dir="$(make_codesign_stub apple-development)"
WORKMAX_TEST_CODESIGN_MODE=apple-development \
WORKMAX_TEST_CODESIGN_LOG="$tmp_dir/codesign-apple-development.log" \
PATH="$codesign_stub_dir:$PATH" \
  expect_fail "require-developer-id-signature rejects non-Developer-ID authority" \
  "$apple_development_signature_app" \
  "app signature is not a Developer ID Application signature" \
  --require-developer-id-signature

adhoc_signature_app="$tmp_dir/adhoc-signature/WorkMax Desktop.app"
write_fixture "$adhoc_signature_app"
codesign_stub_dir="$(make_codesign_stub adhoc)"
WORKMAX_TEST_CODESIGN_MODE=adhoc \
WORKMAX_TEST_CODESIGN_LOG="$tmp_dir/codesign-adhoc.log" \
PATH="$codesign_stub_dir:$PATH" \
  expect_fail "require-developer-id-signature rejects ad-hoc signatures" \
  "$adhoc_signature_app" \
  "app has an ad-hoc signature" \
  --require-developer-id-signature

no_team_signature_app="$tmp_dir/no-team-signature/WorkMax Desktop.app"
write_fixture "$no_team_signature_app"
codesign_stub_dir="$(make_codesign_stub no-team)"
WORKMAX_TEST_CODESIGN_MODE=no-team \
WORKMAX_TEST_CODESIGN_LOG="$tmp_dir/codesign-no-team.log" \
PATH="$codesign_stub_dir:$PATH" \
  expect_fail "require-developer-id-signature rejects missing TeamIdentifier" \
  "$no_team_signature_app" \
  "app signature has no TeamIdentifier" \
  --require-developer-id-signature

no_runtime_signature_app="$tmp_dir/no-runtime-signature/WorkMax Desktop.app"
write_fixture "$no_runtime_signature_app"
codesign_stub_dir="$(make_codesign_stub no-runtime)"
WORKMAX_TEST_CODESIGN_MODE=no-runtime \
WORKMAX_TEST_CODESIGN_LOG="$tmp_dir/codesign-no-runtime.log" \
PATH="$codesign_stub_dir:$PATH" \
  expect_fail "require-developer-id-signature rejects missing hardened runtime" \
  "$no_runtime_signature_app" \
  "app signature is missing hardened runtime" \
  --require-developer-id-signature

bad_verify_signature_app="$tmp_dir/bad-verify-signature/WorkMax Desktop.app"
write_fixture "$bad_verify_signature_app"
codesign_stub_dir="$(make_codesign_stub bad-verify)"
WORKMAX_TEST_CODESIGN_MODE=bad-verify \
WORKMAX_TEST_CODESIGN_LOG="$tmp_dir/codesign-bad-verify.log" \
PATH="$codesign_stub_dir:$PATH" \
  expect_fail "require-developer-id-signature rejects strict verify failures" \
  "$bad_verify_signature_app" \
  "app signature failed strict verification" \
  --require-developer-id-signature

valid_asar_app="$tmp_dir/valid-asar/WorkMax Desktop.app"
write_fixture "$valid_asar_app"
pack_fixture_asar "$valid_asar_app"
expect_pass "valid app.asar payload" "$valid_asar_app"

ambiguous_app="$tmp_dir/ambiguous/WorkMax Desktop.app"
write_fixture "$ambiguous_app"
printf 'fake asar bytes\n' > "$ambiguous_app/Contents/Resources/app.asar"
expect_fail "rejects ambiguous app.asar plus Resources/app payload" "$ambiguous_app" "ambiguous Electron app payload"

duplicate_sidecar_app="$tmp_dir/duplicate-sidecar/WorkMax Desktop.app"
write_fixture "$duplicate_sidecar_app"
mkdir -p "$duplicate_sidecar_app/Contents/Resources/app/bin"
printf 'sidecar inside payload\n' > "$duplicate_sidecar_app/Contents/Resources/app/bin/workagent-desktop"
expect_fail "rejects sidecar duplicated inside Electron payload" "$duplicate_sidecar_app" "packaged app contains packaged build/test artifacts"

main_mismatch_app="$tmp_dir/main-mismatch/WorkMax Desktop.app"
write_fixture "$main_mismatch_app"
node -e "const fs=require('node:fs'); const p=process.argv[1]; const j=require(p); j.main='dist/not-main.js'; fs.writeFileSync(p, JSON.stringify(j, null, 2))" \
  "$main_mismatch_app/Contents/Resources/app/package.json"
expect_fail "rejects package main drift" "$main_mismatch_app" "packaged app package.json main mismatch"

missing_runtime_app="$tmp_dir/missing-runtime/WorkMax Desktop.app"
write_fixture "$missing_runtime_app"
rm "$missing_runtime_app/Contents/Resources/app/dist/preload.js"
expect_fail "rejects missing runtime entry" "$missing_runtime_app" "packaged app required runtime file missing"

unexpected_payload_app="$tmp_dir/unexpected-payload/WorkMax Desktop.app"
write_fixture "$unexpected_payload_app"
printf 'module.exports = {}\n' > "$unexpected_payload_app/Contents/Resources/app/dist/debug-helper.js"
expect_fail "rejects unexpected unpacked payload entry" "$unexpected_payload_app" "packaged app contains unexpected payload entry"

unexpected_resource_app="$tmp_dir/unexpected-resource/WorkMax Desktop.app"
write_fixture "$unexpected_resource_app"
printf 'debug resource\n' > "$unexpected_resource_app/Contents/Resources/debug-resource.txt"
expect_fail "rejects unexpected top-level Resources entry" "$unexpected_resource_app" "Contents/Resources contains unexpected top-level entry"

asar_unpacked_app="$tmp_dir/asar-unpacked/WorkMax Desktop.app"
write_fixture "$asar_unpacked_app"
pack_fixture_asar "$asar_unpacked_app"
mkdir -p "$asar_unpacked_app/Contents/Resources/app.asar.unpacked/bin"
printf 'sidecar inside asar unpacked payload\n' > "$asar_unpacked_app/Contents/Resources/app.asar.unpacked/bin/workagent-desktop"
expect_fail "rejects unexpected app.asar.unpacked payload" "$asar_unpacked_app" "unexpected app.asar.unpacked payload entries"

asar_duplicate_sidecar_app="$tmp_dir/asar-duplicate-sidecar/WorkMax Desktop.app"
write_fixture "$asar_duplicate_sidecar_app"
mkdir -p "$asar_duplicate_sidecar_app/Contents/Resources/app/dist/vendor"
printf 'sidecar inside asar payload\n' > "$asar_duplicate_sidecar_app/Contents/Resources/app/dist/vendor/workagent-desktop"
pack_fixture_asar "$asar_duplicate_sidecar_app"
expect_fail "rejects sidecar duplicated inside app.asar" "$asar_duplicate_sidecar_app" "app.asar contains forbidden dev/package/sidecar artifacts"

asar_main_mismatch_app="$tmp_dir/asar-main-mismatch/WorkMax Desktop.app"
write_fixture "$asar_main_mismatch_app"
node -e "const fs=require('node:fs'); const p=process.argv[1]; const j=require(p); j.main='dist/not-main.js'; fs.writeFileSync(p, JSON.stringify(j, null, 2))" \
  "$asar_main_mismatch_app/Contents/Resources/app/package.json"
pack_fixture_asar "$asar_main_mismatch_app"
expect_fail "rejects app.asar package main drift" "$asar_main_mismatch_app" "app.asar package.json main mismatch"

asar_missing_runtime_app="$tmp_dir/asar-missing-runtime/WorkMax Desktop.app"
write_fixture "$asar_missing_runtime_app"
rm "$asar_missing_runtime_app/Contents/Resources/app/dist/preload.js"
pack_fixture_asar "$asar_missing_runtime_app"
expect_fail "rejects app.asar missing runtime entry" "$asar_missing_runtime_app" "app.asar required runtime file missing"

asar_unexpected_payload_app="$tmp_dir/asar-unexpected-payload/WorkMax Desktop.app"
write_fixture "$asar_unexpected_payload_app"
printf 'module.exports = {}\n' > "$asar_unexpected_payload_app/Contents/Resources/app/dist/debug-helper.js"
pack_fixture_asar "$asar_unexpected_payload_app"
expect_fail "rejects unexpected app.asar payload entry" "$asar_unexpected_payload_app" "app.asar contains unexpected payload entries"
