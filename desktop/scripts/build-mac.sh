#!/usr/bin/env bash
#
# Build a macOS release of WorkMax Desktop.
#
# Produces:
#   desktop/electron/release/WorkMax Desktop-<version>-<arch>.dmg
#   desktop/electron/release/WorkMax Desktop-<version>-<arch>.zip
#
# Cross-compiles the Go sidecar for the requested arch, compiles
# Electron TypeScript, runs electron-builder to assemble, sign, and package the
# .dmg + .zip, then inspects the generated .app bundle before reporting success.
# Code-signing requires a valid Developer ID certificate in the host Keychain;
# without one electron-builder falls back to an unsigned build (gatekeeper will
# refuse to open it on a different machine — fine for internal smoke-tests).
#
# Usage:
#   ./scripts/build-mac.sh                     # builds for host arch
#   ./scripts/build-mac.sh arm64               # Apple Silicon
#   ./scripts/build-mac.sh x64                 # Intel
#   ./scripts/build-mac.sh --public-release arm64
#       Builds and then runs the stricter public-release package inspection
#       requiring a bundled renderer, custom app icon, and Developer ID
#       Application hardened-runtime signature.
#   ./scripts/build-mac.sh --preflight-only arm64
#       Validates local packaging inputs without compiling or invoking
#       electron-builder. Used by script regression tests.
#
# Notes:
#   - electron-builder config: desktop/electron/electron-builder.yml
#     (app id ai.workmax.desktop matches the Keychain service the
#     sidecar uses; entitlements ready for hardenedRuntime).
#   - Notarization is NOT performed by this script. Run
#     desktop/scripts/notarize-mac.sh separately once the team has an
#     Apple notarytool keychain profile or Apple ID + app-specific
#     password configured. Without notarization, the .dmg will trigger
#     Gatekeeper warnings on a different machine.
#   - Every packaged channel requires the bundled Renderer at runtime. The
#     public-release flag adds icon and Developer ID signature gates.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && cd .. && pwd)"

target_arch=""
public_release=0
preflight_only=0

usage() {
  cat >&2 <<'USAGE'
Usage:
  desktop/scripts/build-mac.sh [--public-release] [--preflight-only] [arm64|x64]

Options:
  --public-release   Add custom app icon and Developer ID Application signature gates after build.
  --preflight-only   Validate packaging inputs and exit before build/package.
  -h, --help         Show this help.
USAGE
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    arm64|x64)
      if [ -n "$target_arch" ]; then
        echo "build-mac.sh: arch specified more than once: '$target_arch' and '$1'" >&2
        usage
        exit 1
      fi
      target_arch="$1"
      ;;
    --public-release)
      public_release=1
      ;;
    --preflight-only)
      preflight_only=1
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
      echo "build-mac.sh: unknown option '$1'" >&2
      usage
      exit 1
      ;;
    *)
      echo "build-mac.sh: unexpected argument '$1' (use arm64 or x64)" >&2
      usage
      exit 1
      ;;
  esac
  shift
done

if [ "$#" -gt 0 ]; then
  echo "build-mac.sh: unexpected trailing argument '$1'" >&2
  usage
  exit 1
fi

if [ -z "$target_arch" ]; then
  case "$(uname -m)" in
    arm64)  target_arch="arm64" ;;
    x86_64) target_arch="x64"   ;;
    *)
      echo "build-mac.sh: cannot detect host arch from $(uname -m)" >&2
      echo "  pass arm64 or x64 explicitly" >&2
      exit 1
      ;;
  esac
fi

case "$target_arch" in
  arm64)
    target_goarch="arm64"
    target_app_dir="release/mac-arm64"
    ;;
  x64)
    target_goarch="amd64"
    target_app_dir="release/mac"
    ;;
  *)
    echo "build-mac.sh: unsupported arch '$target_arch' (use arm64 or x64)" >&2
    exit 1
    ;;
esac

bin_dir="$REPO_ROOT/desktop/electron/bin"
binary_path="$bin_dir/workagent-desktop"
third_party_licenses_dir="$bin_dir/third-party-licenses"
desktop_version="$(cd "$REPO_ROOT/desktop/electron" && node -p "require('./package.json').version")"
electron_builder_config="${WORKMAX_DESKTOP_ELECTRON_BUILDER_CONFIG:-$REPO_ROOT/desktop/electron/electron-builder.yml}"
entitlements_plist="${WORKMAX_DESKTOP_ENTITLEMENTS_PLIST:-$REPO_ROOT/desktop/build/entitlements.mac.plist}"
icon_path="${WORKMAX_DESKTOP_ICON_ICNS:-$REPO_ROOT/desktop/build/icons/icon.icns}"

validate_electron_builder_config() {
  local config_path="$1"
  node - "$config_path" "$desktop_version" <<'NODE'
const fs = require("node:fs");
const [configPath, expectedVersion] = process.argv.slice(2);
const config = fs.readFileSync(configPath, "utf8");

function fail(message) {
  console.error(`==> error: electron-builder.yml ${message}`);
  process.exit(1);
}

function requireLine(pattern, message) {
  if (!pattern.test(config)) {
    fail(message);
  }
}

requireLine(/^appId:\s+ai\.workmax\.desktop\s*$/m, "must keep appId=ai.workmax.desktop");
requireLine(/^productName:\s+WorkMax Desktop\s*$/m, "must keep productName='WorkMax Desktop'");
requireLine(/^directories:\s*\n\s+output:\s+release\s*$/m, "must keep directories.output=release for predictable artifact checks");
requireLine(/^\s+hardenedRuntime:\s+true\s*$/m, "must enable mac.hardenedRuntime for notarization");
requireLine(/^\s+entitlements:\s+\.\.\/build\/entitlements\.mac\.plist\s*$/m, "must point mac.entitlements at ../build/entitlements.mac.plist");
requireLine(/^\s+entitlementsInherit:\s+\.\.\/build\/entitlements\.mac\.plist\s*$/m, "must point mac.entitlementsInherit at ../build/entitlements.mac.plist");
requireLine(/^\s+icon:\s+\.\.\/build\/icons\/icon\.icns\s*$/m, "must package ../build/icons/icon.icns");
requireLine(/^artifactName:\s+\$\{productName\}-\$\{version\}-\$\{arch\}\.\$\{ext\}\s*$/m, "artifactName must stay predictable for artifact checks");
requireLine(/^publish:\s+null\s*$/m, "must keep publish=null; build-mac.sh invokes electron-builder with --publish never");
requireLine(/^\s+-\s+dmg\s*$/m, "must build a DMG target");
requireLine(/^\s+-\s+zip\s*$/m, "must build a ZIP target");

for (const entry of [
  "dist/main.js",
  "dist/main-log.js",
  "dist/desktop-bridge.js",
  "dist/oauth-window.js",
  "dist/preload.js",
  "dist/renderer-loader.js",
  "dist/security-helpers.js",
  "dist/sidecar-manager.js",
  "dist/smoke-diagnostics.js",
  "package.json",
]) {
  requireLine(new RegExp(`^\\s+-\\s+${entry.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}\\s*$`, "m"), `files must include ${entry}`);
}

for (const forbidden of [
  /^\s+-\s+dist\/\*\*/m,
  /^\s+-\s+src\/\*\*/m,
  /^\s+-\s+\.\.\/\*\*/m,
]) {
  if (forbidden.test(config)) {
    fail("files must stay narrowly allowlisted to compiled runtime entries");
  }
}

if (!/extraResources:\s*\n[\s\S]*?from:\s+bin\/workagent-desktop[\s\S]*?to:\s+workagent-desktop/.test(config)) {
  fail("must package the sidecar as Resources/workagent-desktop");
}
if (!/extraResources:\s*\n[\s\S]*?from:\s+\.\.\/renderer[\s\S]*?to:\s+renderer/.test(config)) {
  fail("must package desktop/renderer as Resources/renderer");
}
for (const [from, to, message] of [
  ["../../LICENSE", "LICENSE", "must package the WorkMax LICENSE as Resources/LICENSE"],
  ["../../THIRD_PARTY_NOTICES.md", "THIRD_PARTY_NOTICES.md", "must package THIRD_PARTY_NOTICES.md"],
  ["bin/third-party-licenses", "third-party-licenses", "must package the generated Go dependency license bundle"],
  ["node_modules/electron/dist/LICENSE", "third-party-licenses/electron/LICENSE", "must package Electron's license"],
  ["node_modules/electron/dist/LICENSES.chromium.html", "third-party-licenses/electron/LICENSES.chromium.html", "must package Chromium third-party notices"],
]) {
  const escapedFrom = from.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const escapedTo = to.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  if (!new RegExp(`from:\\s+${escapedFrom}[\\s\\S]*?to:\\s+${escapedTo}`).test(config)) {
    fail(message);
  }
}

for (const [pattern, message] of [
  [/^extraFiles:\s*$/m, "must not use extraFiles; sidecar and renderer must stay in extraResources"],
  [/^afterSign:\s*/m, "must not use electron-builder afterSign hooks; notarization is gated by desktop/scripts/notarize-mac.sh"],
  [/^\s+notarize:\s*/m, "must not configure inline mac.notarize; use desktop/scripts/notarize-mac.sh"],
]) {
  if (pattern.test(config)) {
    fail(message);
  }
}

if (!expectedVersion) {
  fail("version preflight received an empty expected version");
}
NODE
}

validate_entitlements_plist() {
  local plist_path="$1"
  node - "$plist_path" <<'NODE'
const fs = require("node:fs");
const plistPath = process.argv[2];
const xml = fs.readFileSync(plistPath, "utf8");

function fail(message) {
  console.error(`==> error: entitlements.mac.plist ${message}`);
  process.exit(1);
}

const allKeys = Array.from(xml.matchAll(/<key>([^<]+)<\/key>/g)).map((item) => item[1]);
const keyPattern = /<key>([^<]+)<\/key>\s*<(true|false)\s*\/>/g;
const entitlements = new Map();
let match;
while ((match = keyPattern.exec(xml)) !== null) {
  entitlements.set(match[1], match[2] === "true");
}

const expected = new Set([
  "com.apple.security.cs.allow-jit",
  "com.apple.security.cs.allow-unsigned-executable-memory",
  "com.apple.security.cs.allow-dyld-environment-variables",
  "com.apple.security.network.client",
]);

for (const key of expected) {
  if (entitlements.get(key) !== true) {
    fail(`must include ${key}=true`);
  }
}

for (const key of allKeys) {
  if (key.startsWith("com.apple.security.") && !expected.has(key)) {
    fail(`contains unexpected entitlement ${key}`);
  }
  if (key.startsWith("com.apple.security.") && !entitlements.has(key)) {
    fail(`entitlement ${key} must be a boolean true/false value`);
  }
}

if (entitlements.size === 0) {
  fail("does not contain any boolean entitlements");
}
NODE
}

run_packaging_preflight() {
  if [ ! -f "$electron_builder_config" ]; then
    echo "==> error: electron-builder config not found: $electron_builder_config" >&2
    echo "    Expected desktop/electron/electron-builder.yml. Was it deleted?" >&2
    exit 1
  fi
  validate_electron_builder_config "$electron_builder_config"

  # Verify the entitlements file exists too — electron-builder would
  # fail later anyway, but failing early gives a cleaner message.
  if [ ! -f "$entitlements_plist" ]; then
    echo "==> error: desktop/build/entitlements.mac.plist not found: $entitlements_plist" >&2
    echo "    Hardened runtime needs entitlements; see electron-builder.yml mac.entitlements." >&2
    exit 1
  fi
  validate_entitlements_plist "$entitlements_plist"

  for license_input in "$REPO_ROOT/LICENSE" "$REPO_ROOT/THIRD_PARTY_NOTICES.md"; do
    if [ ! -s "$license_input" ]; then
      echo "==> error: required release license material not found or empty: $license_input" >&2
      exit 1
    fi
  done

  "$REPO_ROOT/desktop/scripts/check-bundled-renderer.sh"

  if [ ! -s "$icon_path" ]; then
    echo "==> error: desktop/build/icons/icon.icns not found or empty." >&2
    echo "    electron-builder.yml declares this as the app icon; regenerate it from the reviewed Desktop icon source." >&2
    exit 1
  fi
  icon_magic="$(head -c 4 "$icon_path" 2>/dev/null || true)"
  if [ "$icon_magic" != "icns" ]; then
    echo "==> error: desktop/build/icons/icon.icns is not a valid .icns file (missing icns header)." >&2
    exit 1
  fi
}

if [ "$preflight_only" -eq 1 ]; then
  echo "==> validating mac packaging inputs (arch=$target_arch, version=$desktop_version)"
  run_packaging_preflight
  echo "ok build-mac preflight"
  exit 0
fi

echo "==> building Go sidecar for darwin/$target_goarch (version=$desktop_version)"
mkdir -p "$bin_dir"
( cd "$REPO_ROOT/server" \
  && GOOS=darwin GOARCH="$target_goarch" \
     go build -tags desktop -ldflags "-s -w -X server/desktop/buildinfo.Version=$desktop_version" \
     -o "$binary_path" ./cmd/workagent-desktop )
echo "    built: $binary_path"
if ! grep -Fq "$desktop_version" "$binary_path"; then
  echo "==> error: built sidecar does not contain expected version '$desktop_version'" >&2
  echo "    Refusing to package a possibly stale sidecar binary." >&2
  exit 1
fi
echo "    verified sidecar version marker: $desktop_version"

cd "$REPO_ROOT/desktop/electron"

if [ ! -d node_modules ] || [ ! -x node_modules/.bin/tsc ] || [ ! -x node_modules/.bin/electron-builder ]; then
  echo "==> installing Electron npm deps"
  npm install
fi

echo "==> compiling Electron TypeScript"
npm run build --silent

echo "==> generating Go dependency license bundle"
license_staging_parent="$(mktemp -d "${TMPDIR:-/tmp}/workmax-license-bundle.XXXXXX")"
license_staging_dir="$license_staging_parent/licenses"
if ! (
  cd "$REPO_ROOT/server"
  GOFLAGS="-tags=desktop" go run github.com/google/go-licenses/v2@v2.0.1 \
    save --ignore server ./cmd/workagent-desktop ./cmd/agent-worker \
    --save_path="$license_staging_dir"
); then
  rm -rf "$license_staging_parent"
  echo "==> error: failed to generate Go dependency license bundle" >&2
  exit 1
fi
if [ ! -d "$license_staging_dir" ] || ! find "$license_staging_dir" -type f -size +0c -print -quit | grep -q .; then
  rm -rf "$license_staging_parent"
  echo "==> error: generated Go dependency license bundle is empty" >&2
  exit 1
fi
rm -rf "$third_party_licenses_dir"
mv "$license_staging_dir" "$third_party_licenses_dir"
rmdir "$license_staging_parent"

run_packaging_preflight

echo "==> packaging with electron-builder (arch=$target_arch)"
./node_modules/.bin/electron-builder --mac --"$target_arch" --publish never

target_app="$target_app_dir/WorkMax Desktop.app"
echo "==> inspecting generated .app bundle: $target_app"
if [ ! -d "$target_app" ]; then
  echo "==> error: electron-builder produced no inspectable .app bundle for arch=$target_arch" >&2
  echo "    Expected: desktop/electron/$target_app" >&2
  exit 1
fi
inspect_args=(--require-bundled-renderer)
if [ "$public_release" -eq 1 ]; then
  echo "==> public-release validation enabled: requiring app icon and Developer ID Application signature"
  inspect_args+=(--require-app-icon --require-developer-id-signature)
fi
"$REPO_ROOT/desktop/scripts/inspect-mac-package.sh" "${inspect_args[@]}" "$target_app"

expected_dmg="release/WorkMax Desktop-${desktop_version}-${target_arch}.dmg"
expected_zip="release/WorkMax Desktop-${desktop_version}-${target_arch}.zip"
for artifact in "$expected_dmg" "$expected_zip"; do
  if [ ! -s "$artifact" ]; then
    echo "==> error: expected package artifact missing or empty: desktop/electron/$artifact" >&2
    exit 1
  fi
  echo "ok package artifact: $artifact"
done

echo "==> done — artifacts in desktop/electron/release/"
ls -lh release/ 2>/dev/null || echo "  (release/ not found — check electron-builder output above)"
