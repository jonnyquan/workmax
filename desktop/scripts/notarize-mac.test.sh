#!/usr/bin/env bash
#
# Regression tests for notarize-mac.sh validation paths that do not require a
# signed app, Apple credentials, or network access.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NOTARIZE="$SCRIPT_DIR/notarize-mac.sh"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && cd .. && pwd)"

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/workmax-notarize-mac-test.XXXXXX")"
trap 'rm -rf "$tmp_dir"' EXIT

expect_pass() {
  local name="$1"
  shift
  local output
  if ! output="$("$@" 2>&1)"; then
    printf 'not ok - %s\n%s\n' "$name" "$output" >&2
    exit 1
  fi
  printf 'ok - %s\n' "$name"
}

expect_fail() {
  local name="$1"
  local want="$2"
  shift 2
  local output
  if output="$("$@" 2>&1)"; then
    printf 'not ok - %s: command unexpectedly passed\n%s\n' "$name" "$output" >&2
    exit 1
  fi
  if ! grep -Fq -- "$want" <<<"$output"; then
    printf 'not ok - %s: missing expected failure text: %s\n%s\n' "$name" "$want" "$output" >&2
    exit 1
  fi
  printf 'ok - %s\n' "$name"
}

expect_fail "rejects unsupported target" "Usage:" \
  "$NOTARIZE" --dry-run linux

expect_fail "rejects missing explicit DMG before credentials" "missing or empty DMG" \
  "$NOTARIZE" --dry-run "$tmp_dir/missing.dmg"

fake_dmg="$tmp_dir/custom-build.dmg"
printf 'fake dmg bytes\n' > "$fake_dmg"

expect_fail "rejects explicit DMG without app inspection by default" "explicit DMG path cannot verify" \
  "$NOTARIZE" --dry-run "$fake_dmg"

expect_fail "rejects arbitrary explicit DMG submission with hosted-renderer escape hatch" "--allow-hosted-renderer is dry-run only" \
  env WORKMAX_NOTARY_KEYCHAIN_PROFILE=workmax-notary \
    "$NOTARIZE" --allow-hosted-renderer "$fake_dmg"

# The version comes from the same place the shipped binary stamps it, now
# that there is no package.json to read it from.
desktop_version="$(cd "$SCRIPT_DIR/../../server" && grep -oE '"[0-9][^"]*"' desktop/buildinfo/buildinfo.go | head -1 | tr -d '"')"
release_dmg="$tmp_dir/WorkMax Desktop-${desktop_version}-arm64.dmg"
printf 'fake release dmg bytes\n' > "$release_dmg"

expect_fail "infers app bundle for versioned arm64 DMG" "missing app bundle for inspection" \
  "$NOTARIZE" --dry-run "$release_dmg"

expect_fail "rejects hosted-renderer escape hatch for real notarization" "--allow-hosted-renderer is dry-run only" \
  env WORKMAX_NOTARY_KEYCHAIN_PROFILE=workmax-notary \
    "$NOTARIZE" --allow-hosted-renderer "$release_dmg"

expect_fail "rejects missing credentials" "missing notarization credential env" \
  env -u WORKMAX_NOTARY_KEYCHAIN_PROFILE -u APPLE_ID -u APPLE_TEAM_ID -u APPLE_APP_SPECIFIC_PASSWORD \
    "$NOTARIZE" --dry-run --allow-hosted-renderer "$fake_dmg"

expect_pass "accepts keychain profile in dry run" \
  env WORKMAX_NOTARY_KEYCHAIN_PROFILE=workmax-notary \
    "$NOTARIZE" --dry-run --allow-hosted-renderer "$fake_dmg"

expect_pass "accepts app-specific password credentials in dry run" \
  env -u WORKMAX_NOTARY_KEYCHAIN_PROFILE \
    APPLE_ID=release@example.com \
    APPLE_TEAM_ID=ABCDE12345 \
    APPLE_APP_SPECIFIC_PASSWORD=xxxx-xxxx-xxxx-xxxx \
    "$NOTARIZE" --dry-run --allow-hosted-renderer "$fake_dmg"

write_release_app_fixture() {
  local app_path="$1"
  mkdir -p "$app_path/Contents/MacOS" "$app_path/Contents/Resources/renderer/en/desktop/lib"

  cat > "$app_path/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleIdentifier</key>
  <string>ai.workmax.desktop</string>
  <key>CFBundleShortVersionString</key>
  <string>$desktop_version</string>
  <key>CFBundleExecutable</key>
  <string>WorkMax Desktop</string>
  <key>CFBundleIconFile</key>
  <string>icon.icns</string>
</dict>
</plist>
PLIST

  # The Wails layout: one binary in MacOS/, the bundled renderer in Resources/.
  # No app payload, no separate sidecar executable — the shell and the sidecar
  # are one process now.
  #
  # The fixture has to satisfy inspect-mac-package.sh, which notarize-mac.sh
  # runs before submitting, so it carries the real renderer files and a
  # version marker rather than placeholders. That coupling is the point: these
  # two scripts gate the same artifact and should agree about what it is.
  printf '#!/usr/bin/env bash\n# %s\nexit 0\n' "$desktop_version" > "$app_path/Contents/MacOS/WorkMax Desktop"
  chmod +x "$app_path/Contents/MacOS/WorkMax Desktop"
  printf 'icnsfake icon bytes\n' > "$app_path/Contents/Resources/icon.icns"
  local renderer_src="$SCRIPT_DIR/../renderer/en/desktop"
  cp "$renderer_src/index.html" "$renderer_src/styles.css" "$renderer_src/renderer.js" \
     "$renderer_src/shim.js" "$app_path/Contents/Resources/renderer/en/desktop/"
  cp "$renderer_src/lib/desktop-bridge.js" "$app_path/Contents/Resources/renderer/en/desktop/lib/"
}

make_codesign_stub() {
  local mode="$1"
  local dir="$tmp_dir/codesign-$mode"
  mkdir -p "$dir"
  cat > "$dir/codesign" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
mode="${WORKMAX_TEST_CODESIGN_MODE:?}"
case "${1:-}" in
  -dv)
    case "$mode" in
      adhoc)
        echo "Signature=adhoc" >&2
        echo "TeamIdentifier=not set" >&2
        ;;
      no-team)
        echo "Signature size=1234" >&2
        echo "TeamIdentifier=not set" >&2
        ;;
      no-runtime)
        echo "Signature size=1234" >&2
        echo "TeamIdentifier=ABCDE12345" >&2
        ;;
      apple-development)
        echo "Signature size=1234" >&2
        echo "Authority=Apple Development: WorkMax Dev (ABCDE12345)" >&2
        echo "TeamIdentifier=ABCDE12345" >&2
        echo "Runtime Version=16.0.0" >&2
        ;;
      valid|verify-fail)
        echo "Signature size=1234" >&2
        echo "Authority=Developer ID Application: WorkMax Inc. (ABCDE12345)" >&2
        echo "Authority=Developer ID Certification Authority" >&2
        echo "Authority=Apple Root CA" >&2
        echo "TeamIdentifier=ABCDE12345" >&2
        echo "Runtime Version=16.0.0" >&2
        ;;
    esac
    ;;
  --verify)
    if [ "$mode" = "verify-fail" ]; then
      echo "strict verification failed" >&2
      exit 1
    fi
    ;;
  *)
    echo "unexpected codesign invocation: $*" >&2
    exit 2
    ;;
esac
SH
  chmod +x "$dir/codesign"
  cat > "$dir/spctl" <<'SH'
#!/usr/bin/env bash
echo "stub gatekeeper assessment failed" >&2
exit 1
SH
  chmod +x "$dir/spctl"
  printf '%s\n' "$dir"
}

release_app="$tmp_dir/mac-arm64/WorkMax Desktop.app"
write_release_app_fixture "$release_app"

codesign_stub_dir="$(make_codesign_stub adhoc)"
WORKMAX_TEST_CODESIGN_MODE=adhoc \
PATH="$codesign_stub_dir:$PATH" \
  expect_fail "rejects ad-hoc app signatures before notarization" \
  "app has an ad-hoc signature; use a Developer ID Application certificate" \
  env WORKMAX_NOTARY_KEYCHAIN_PROFILE=workmax-notary \
    "$NOTARIZE" --dry-run --allow-hosted-renderer "$release_dmg"

codesign_stub_dir="$(make_codesign_stub no-team)"
WORKMAX_TEST_CODESIGN_MODE=no-team \
PATH="$codesign_stub_dir:$PATH" \
  expect_fail "rejects signatures without TeamIdentifier before notarization" \
  "app signature has no TeamIdentifier; use Developer ID Application signing" \
  env WORKMAX_NOTARY_KEYCHAIN_PROFILE=workmax-notary \
    "$NOTARIZE" --dry-run --allow-hosted-renderer "$release_dmg"

codesign_stub_dir="$(make_codesign_stub no-runtime)"
WORKMAX_TEST_CODESIGN_MODE=no-runtime \
PATH="$codesign_stub_dir:$PATH" \
  expect_fail "rejects signatures without hardened runtime before notarization" \
  "app signature is missing hardened runtime; enable hardenedRuntime for Developer ID notarization" \
  env WORKMAX_NOTARY_KEYCHAIN_PROFILE=workmax-notary \
    "$NOTARIZE" --dry-run --allow-hosted-renderer "$release_dmg"

codesign_stub_dir="$(make_codesign_stub verify-fail)"
WORKMAX_TEST_CODESIGN_MODE=verify-fail \
PATH="$codesign_stub_dir:$PATH" \
  expect_fail "rejects strict codesign verification failures before notarization" \
  "app signature failed strict verification" \
  env WORKMAX_NOTARY_KEYCHAIN_PROFILE=workmax-notary \
    "$NOTARIZE" --dry-run --allow-hosted-renderer "$release_dmg"

codesign_stub_dir="$(make_codesign_stub apple-development)"
WORKMAX_TEST_CODESIGN_MODE=apple-development \
PATH="$codesign_stub_dir:$PATH" \
  expect_fail "rejects non-Developer-ID authority before notarization" \
  "app signature is not a Developer ID Application signature" \
  env WORKMAX_NOTARY_KEYCHAIN_PROFILE=workmax-notary \
    "$NOTARIZE" --dry-run --allow-hosted-renderer "$release_dmg"

codesign_stub_dir="$(make_codesign_stub valid)"
WORKMAX_TEST_CODESIGN_MODE=valid \
PATH="$codesign_stub_dir:$PATH" \
  expect_pass "accepts Developer ID Application signature in dry run" \
  env WORKMAX_NOTARY_KEYCHAIN_PROFILE=workmax-notary \
    "$NOTARIZE" --dry-run --allow-hosted-renderer "$release_dmg"
