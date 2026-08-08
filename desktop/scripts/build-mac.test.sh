#!/usr/bin/env bash
#
# Regression tests for build-mac.sh validation paths that do not compile Go,
# install npm dependencies, or invoke electron-builder.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && cd .. && pwd)"
BUILD="$SCRIPT_DIR/build-mac.sh"
tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/workmax-build-mac-test.XXXXXX")"
trap 'rm -rf "$tmp_dir"' EXIT

expect_pass() {
  local name="$1"
  shift
  local output
  if ! output="$("$BUILD" "$@" 2>&1)"; then
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
  if ! output="$("$BUILD" "$@" 2>&1)"; then
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
  if output="$("$BUILD" "$@" 2>&1)"; then
    printf 'not ok - %s: build unexpectedly passed\n%s\n' "$name" "$output" >&2
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
  if output="$("$@" 2>&1)"; then
    printf 'not ok - %s: build unexpectedly passed\n%s\n' "$name" "$output" >&2
    exit 1
  fi
  if ! grep -Fq -- "$want" <<<"$output"; then
    printf 'not ok - %s: missing expected failure text: %s\n%s\n' "$name" "$want" "$output" >&2
    exit 1
  fi
  printf 'ok - %s\n' "$name"
}

write_config_fixture() {
  local path="$1"
  cp "$REPO_ROOT/desktop/electron/electron-builder.yml" "$path"
}

write_entitlements_fixture() {
  local path="$1"
  cp "$REPO_ROOT/desktop/build/entitlements.mac.plist" "$path"
}

expect_pass "help exits before validation" --help
expect_fail "rejects unknown option" "unknown option" --bogus
expect_fail "rejects unexpected argument" "unexpected argument" linux
expect_fail "rejects duplicate arch" "arch specified more than once" arm64 x64
expect_fail "rejects trailing argument" "unexpected trailing argument" -- arm64
expect_pass_contains "preflight validates current arm64 packaging inputs" "ok build-mac preflight" \
  --preflight-only arm64
expect_pass_contains "preflight validates current public-release inputs" "ok build-mac preflight" \
  --preflight-only --public-release x64

missing_runtime_config="$tmp_dir/missing-runtime-entry.yml"
write_config_fixture "$missing_runtime_config"
perl -0pi -e 's/^\s+- dist\/smoke-diagnostics\.js\n//m' "$missing_runtime_config"
expect_fail_env "preflight rejects missing runtime allowlist entry" "files must include dist/smoke-diagnostics.js" \
  env WORKMAX_DESKTOP_ELECTRON_BUILDER_CONFIG="$missing_runtime_config" \
    "$BUILD" --preflight-only arm64

no_hardened_runtime_config="$tmp_dir/no-hardened-runtime.yml"
write_config_fixture "$no_hardened_runtime_config"
perl -0pi -e 's/hardenedRuntime: true/hardenedRuntime: false/' "$no_hardened_runtime_config"
expect_fail_env "preflight rejects disabled hardened runtime" "must enable mac.hardenedRuntime" \
  env WORKMAX_DESKTOP_ELECTRON_BUILDER_CONFIG="$no_hardened_runtime_config" \
    "$BUILD" --preflight-only arm64

wrong_output_dir_config="$tmp_dir/wrong-output-dir.yml"
write_config_fixture "$wrong_output_dir_config"
perl -0pi -e 's/output: release/output: dist-release/' "$wrong_output_dir_config"
expect_fail_env "preflight rejects release output drift" "must keep directories.output=release" \
  env WORKMAX_DESKTOP_ELECTRON_BUILDER_CONFIG="$wrong_output_dir_config" \
    "$BUILD" --preflight-only arm64

publish_enabled_config="$tmp_dir/publish-enabled.yml"
write_config_fixture "$publish_enabled_config"
perl -0pi -e 's/publish: null/publish:\n  provider: github/' "$publish_enabled_config"
expect_fail_env "preflight rejects publish config drift" "must keep publish=null" \
  env WORKMAX_DESKTOP_ELECTRON_BUILDER_CONFIG="$publish_enabled_config" \
    "$BUILD" --preflight-only arm64

extra_files_config="$tmp_dir/extra-files.yml"
write_config_fixture "$extra_files_config"
cat >> "$extra_files_config" <<'YAML'
extraFiles:
  - from: ../release-notes
    to: release-notes
YAML
expect_fail_env "preflight rejects extraFiles sidecar bypass" "must not use extraFiles" \
  env WORKMAX_DESKTOP_ELECTRON_BUILDER_CONFIG="$extra_files_config" \
    "$BUILD" --preflight-only arm64

after_sign_config="$tmp_dir/after-sign.yml"
write_config_fixture "$after_sign_config"
cat >> "$after_sign_config" <<'YAML'
afterSign: scripts/after-sign.js
YAML
expect_fail_env "preflight rejects electron-builder afterSign hooks" "must not use electron-builder afterSign hooks" \
  env WORKMAX_DESKTOP_ELECTRON_BUILDER_CONFIG="$after_sign_config" \
    "$BUILD" --preflight-only arm64

inline_notarize_config="$tmp_dir/inline-notarize.yml"
write_config_fixture "$inline_notarize_config"
cat >> "$inline_notarize_config" <<'YAML'
mac:
  notarize:
    teamId: ABCDE12345
YAML
expect_fail_env "preflight rejects inline mac notarize config" "must not configure inline mac.notarize" \
  env WORKMAX_DESKTOP_ELECTRON_BUILDER_CONFIG="$inline_notarize_config" \
    "$BUILD" --preflight-only arm64

missing_renderer_resource_config="$tmp_dir/missing-renderer-resource.yml"
write_config_fixture "$missing_renderer_resource_config"
perl -0pi -e 's/\n  - from: \.\.\/renderer\n    to: renderer\n    filter:\n      - "\*\*\/\*"//' "$missing_renderer_resource_config"
expect_fail_env "preflight rejects missing bundled renderer resource" "must package desktop/renderer as Resources/renderer" \
  env WORKMAX_DESKTOP_ELECTRON_BUILDER_CONFIG="$missing_renderer_resource_config" \
    "$BUILD" --preflight-only arm64

missing_license_resource_config="$tmp_dir/missing-license-resource.yml"
write_config_fixture "$missing_license_resource_config"
perl -0pi -e 's/\n  - from: \.\.\/\.\.\/LICENSE\n    to: LICENSE//' "$missing_license_resource_config"
expect_fail_env "preflight rejects missing WorkMax license resource" "must package the WorkMax LICENSE" \
  env WORKMAX_DESKTOP_ELECTRON_BUILDER_CONFIG="$missing_license_resource_config" \
    "$BUILD" --preflight-only arm64

missing_go_license_bundle_config="$tmp_dir/missing-go-license-bundle.yml"
write_config_fixture "$missing_go_license_bundle_config"
perl -0pi -e 's/\n  - from: bin\/third-party-licenses\n    to: third-party-licenses\n    filter:\n      - "\*\*\/\*"//' "$missing_go_license_bundle_config"
expect_fail_env "preflight rejects missing Go license bundle resource" "must package the generated Go dependency license bundle" \
  env WORKMAX_DESKTOP_ELECTRON_BUILDER_CONFIG="$missing_go_license_bundle_config" \
    "$BUILD" --preflight-only arm64

missing_network_client_entitlements="$tmp_dir/missing-network-client.plist"
write_entitlements_fixture "$missing_network_client_entitlements"
perl -0pi -e 's/\s*<key>com\.apple\.security\.network\.client<\/key>\s*<true\/>//' "$missing_network_client_entitlements"
expect_fail_env "preflight rejects missing network client entitlement" "must include com.apple.security.network.client=true" \
  env WORKMAX_DESKTOP_ENTITLEMENTS_PLIST="$missing_network_client_entitlements" \
    "$BUILD" --preflight-only arm64

extra_network_server_entitlements="$tmp_dir/extra-network-server.plist"
write_entitlements_fixture "$extra_network_server_entitlements"
perl -0pi -e 's#</dict>#    <key>com.apple.security.network.server</key>\n    <true/>\n</dict>#' "$extra_network_server_entitlements"
expect_fail_env "preflight rejects unexpected network server entitlement" "contains unexpected entitlement com.apple.security.network.server" \
  env WORKMAX_DESKTOP_ENTITLEMENTS_PLIST="$extra_network_server_entitlements" \
    "$BUILD" --preflight-only arm64

string_network_server_entitlements="$tmp_dir/string-network-server.plist"
write_entitlements_fixture "$string_network_server_entitlements"
perl -0pi -e 's#</dict>#    <key>com.apple.security.network.server</key>\n    <string>true</string>\n</dict>#' "$string_network_server_entitlements"
expect_fail_env "preflight rejects unexpected string-valued entitlement" "contains unexpected entitlement com.apple.security.network.server" \
  env WORKMAX_DESKTOP_ENTITLEMENTS_PLIST="$string_network_server_entitlements" \
    "$BUILD" --preflight-only arm64

string_network_client_entitlements="$tmp_dir/string-network-client.plist"
write_entitlements_fixture "$string_network_client_entitlements"
perl -0pi -e 's#<key>com.apple.security.network.client</key>\n    <true/>#<key>com.apple.security.network.client</key>\n    <string>true</string>#' "$string_network_client_entitlements"
expect_fail_env "preflight rejects non-boolean required entitlement" "must include com.apple.security.network.client=true" \
  env WORKMAX_DESKTOP_ENTITLEMENTS_PLIST="$string_network_client_entitlements" \
    "$BUILD" --preflight-only arm64
