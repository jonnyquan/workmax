#!/usr/bin/env bash
#
# Inspect an electron-builder macOS .app bundle without launching it.
# Use after desktop/scripts/build-mac.sh extracts/creates an app bundle
# and before handing a packaged build to testers.
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
Usage:
  inspect-mac-package.sh [--require-bundled-renderer] [--require-app-icon] [--require-developer-id-signature] <path/to/WorkMax Desktop.app>

Checks:
  - bundle Info.plist exists
  - CFBundleIdentifier is ai.workmax.desktop
  - CFBundleShortVersionString matches desktop/electron/package.json
  - CFBundleExecutable exists under Contents/MacOS and is executable
  - packaged Electron app payload exists under Contents/Resources
  - packaged Electron app payload uses exactly one mode: app.asar or Resources/app
  - packaged Electron app package name/main/version match desktop/electron/package.json
  - packaged Electron app payload contains only expected runtime entries
  - packaged Electron app payload does not contain tests, source maps,
    nested .app bundles, release/, dist/mac* package output, source files,
    app.asar.unpacked payload, or a duplicated sidecar binary
  - Contents/Resources contains only expected top-level runtime resources
  - Contents/Resources/workagent-desktop exists and is executable
  - embedded sidecar binary contains the expected version marker
  - WorkMax LICENSE and THIRD_PARTY_NOTICES.md match the release source
  - generated Go dependency licenses, Electron's license, and Chromium notices
    are present under Contents/Resources/third-party-licenses
  - with --require-bundled-renderer: bundled renderer payload exists at
    Contents/Resources/renderer/en/desktop/ with index.html, styles.css,
    renderer.js, loopback-only CSP, and no token-like embedded secrets
  - with --require-app-icon: CFBundleIconFile is icon.icns and
    Contents/Resources/icon.icns exists with an icns file header
  - signing/notarization status is reported as informational output; with
    --require-developer-id-signature, ad-hoc/non-Developer-ID/missing
    TeamIdentifier/missing hardened runtime/strict verification failures are
    fatal
USAGE
}

require_bundled_renderer=0
require_app_icon=0
require_developer_id_signature=0
while [ "$#" -gt 0 ]; do
  case "${1:-}" in
    --require-bundled-renderer)
      require_bundled_renderer=1
      shift
      ;;
    --require-app-icon)
      require_app_icon=1
      shift
      ;;
    --require-developer-id-signature)
      require_developer_id_signature=1
      shift
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    --*)
      echo "inspect-mac-package.sh: unknown option: $1" >&2
      usage
      exit 2
      ;;
    *)
      break
      ;;
  esac
done

if [ "$#" -ne 1 ]; then
  usage
  exit 2
fi

app_path="${1%/}"
if [ ! -d "$app_path" ]; then
  echo "inspect-mac-package.sh: app bundle not found: $app_path" >&2
  exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && cd .. && pwd)"
expected_version="$(cd "$REPO_ROOT/desktop/electron" && node -p "require('./package.json').version")"
expected_package_name="$(cd "$REPO_ROOT/desktop/electron" && node -p "require('./package.json').name")"
expected_package_main="$(cd "$REPO_ROOT/desktop/electron" && node -p "require('./package.json').main")"
expected_bundle_id="ai.workmax.desktop"

info_plist="$app_path/Contents/Info.plist"
macos_dir="$app_path/Contents/MacOS"
resources_dir="$app_path/Contents/Resources"
sidecar_path="$resources_dir/workagent-desktop"
license_path="$resources_dir/LICENSE"
third_party_notices_path="$resources_dir/THIRD_PARTY_NOTICES.md"
third_party_licenses_dir="$resources_dir/third-party-licenses"
electron_license_path="$third_party_licenses_dir/electron/LICENSE"
chromium_notices_path="$third_party_licenses_dir/electron/LICENSES.chromium.html"
renderer_entry="$resources_dir/renderer/en/desktop/index.html"
renderer_dir="$resources_dir/renderer/en/desktop"
renderer_css="$renderer_dir/styles.css"
renderer_js="$renderer_dir/renderer.js"
expected_icon_file="icon.icns"
expected_icon_path="$resources_dir/$expected_icon_file"

if [ ! -f "$info_plist" ]; then
  echo "inspect-mac-package.sh: missing Info.plist: $info_plist" >&2
  exit 1
fi

plist_value() {
  /usr/libexec/PlistBuddy -c "Print :$1" "$info_plist" 2>/dev/null
}

bundle_id="$(plist_value CFBundleIdentifier || true)"
if [ "$bundle_id" != "$expected_bundle_id" ]; then
  echo "inspect-mac-package.sh: CFBundleIdentifier mismatch: got '$bundle_id', want '$expected_bundle_id'" >&2
  exit 1
fi

bundle_version="$(plist_value CFBundleShortVersionString || true)"
if [ "$bundle_version" != "$expected_version" ]; then
  echo "inspect-mac-package.sh: CFBundleShortVersionString mismatch: got '$bundle_version', want '$expected_version'" >&2
  exit 1
fi

bundle_icon_file="$(plist_value CFBundleIconFile || true)"
if [ "$require_app_icon" -eq 1 ]; then
  if [ "$bundle_icon_file" != "$expected_icon_file" ]; then
    echo "inspect-mac-package.sh: CFBundleIconFile mismatch: got '$bundle_icon_file', want '$expected_icon_file'" >&2
    exit 1
  fi
  if [ ! -s "$expected_icon_path" ]; then
    echo "inspect-mac-package.sh: missing app icon: $expected_icon_path" >&2
    exit 1
  fi
  icon_magic="$(head -c 4 "$expected_icon_path" 2>/dev/null || true)"
  if [ "$icon_magic" != "icns" ]; then
    echo "inspect-mac-package.sh: invalid app icon header: $expected_icon_path must be an .icns file" >&2
    exit 1
  fi
fi

bundle_executable="$(plist_value CFBundleExecutable || true)"
if [ -z "$bundle_executable" ]; then
  echo "inspect-mac-package.sh: missing CFBundleExecutable in Info.plist" >&2
  exit 1
fi

main_executable="$macos_dir/$bundle_executable"
if [ ! -f "$main_executable" ]; then
  echo "inspect-mac-package.sh: missing main executable: $main_executable" >&2
  exit 1
fi

if [ ! -x "$main_executable" ]; then
  echo "inspect-mac-package.sh: main executable is not executable: $main_executable" >&2
  exit 1
fi

for release_document in "$license_path" "$third_party_notices_path"; do
  if [ ! -s "$release_document" ]; then
    echo "inspect-mac-package.sh: missing release license material: $release_document" >&2
    exit 1
  fi
done
if ! cmp -s "$REPO_ROOT/LICENSE" "$license_path"; then
  echo "inspect-mac-package.sh: packaged LICENSE does not match the release source" >&2
  exit 1
fi
if ! cmp -s "$REPO_ROOT/THIRD_PARTY_NOTICES.md" "$third_party_notices_path"; then
  echo "inspect-mac-package.sh: packaged THIRD_PARTY_NOTICES.md does not match the release source" >&2
  exit 1
fi
for upstream_notice_pair in \
  "$REPO_ROOT/desktop/electron/node_modules/electron/dist/LICENSE:$electron_license_path" \
  "$REPO_ROOT/desktop/electron/node_modules/electron/dist/LICENSES.chromium.html:$chromium_notices_path"; do
  upstream_notice="${upstream_notice_pair%%:*}"
  packaged_notice="${upstream_notice_pair#*:}"
  if [ ! -s "$packaged_notice" ]; then
    echo "inspect-mac-package.sh: missing third-party license material: $packaged_notice" >&2
    exit 1
  fi
  if [ ! -s "$upstream_notice" ]; then
    echo "inspect-mac-package.sh: cannot verify third-party notice; upstream file is missing: $upstream_notice" >&2
    exit 1
  fi
  if ! cmp -s "$upstream_notice" "$packaged_notice"; then
    echo "inspect-mac-package.sh: packaged third-party notice differs from upstream: $packaged_notice" >&2
    exit 1
  fi
done
if ! find "$third_party_licenses_dir" -path "$third_party_licenses_dir/electron" -prune -o -type f -size +0c -print -quit | grep -q .; then
  echo "inspect-mac-package.sh: generated Go dependency license bundle is missing or empty: $third_party_licenses_dir" >&2
  exit 1
fi

app_asar="$resources_dir/app.asar"
app_asar_unpacked="$resources_dir/app.asar.unpacked"
app_dir="$resources_dir/app"
app_payload_label=""
required_runtime_entries=(
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
if [ -f "$app_asar" ] && [ -e "$app_dir" ]; then
  echo "inspect-mac-package.sh: ambiguous Electron app payload: both $app_asar and $app_dir exist" >&2
  exit 1
fi
if [ -f "$app_asar" ]; then
  if [ ! -s "$app_asar" ]; then
    echo "inspect-mac-package.sh: app.asar is empty: $app_asar" >&2
    exit 1
  fi
  if [ ! -f "$REPO_ROOT/desktop/electron/node_modules/@electron/asar/package.json" ]; then
    echo "inspect-mac-package.sh: missing @electron/asar dependency needed to inspect app.asar" >&2
    echo "    Run: cd desktop/electron && npm install" >&2
    exit 1
  fi
  ASAR_PATH="$app_asar" \
    EXPECTED_VERSION="$expected_version" \
    EXPECTED_PACKAGE_NAME="$expected_package_name" \
    EXPECTED_PACKAGE_MAIN="$expected_package_main" \
    REPO_ROOT="$REPO_ROOT" \
    node <<'NODE'
const path = require('node:path')
const asar = require(path.join(process.env.REPO_ROOT, 'desktop/electron/node_modules/@electron/asar'))

const asarPath = process.env.ASAR_PATH
const expectedVersion = process.env.EXPECTED_VERSION
const expectedPackageName = process.env.EXPECTED_PACKAGE_NAME
const expectedPackageMain = process.env.EXPECTED_PACKAGE_MAIN
const requiredRuntimeEntries = [
  'dist/main.js',
  'dist/main-log.js',
  'dist/desktop-bridge.js',
  'dist/oauth-window.js',
  'dist/preload.js',
  'dist/renderer-loader.js',
  'dist/security-helpers.js',
  'dist/sidecar-manager.js',
  'dist/smoke-diagnostics.js',
]
const allowedEntries = new Set([
  '/dist',
  '/package.json',
  ...requiredRuntimeEntries.map((entry) => `/${entry}`),
])
const entries = asar.listPackage(asarPath)
const entrySet = new Set(entries)

function fail(message) {
  console.error(`inspect-mac-package.sh: ${message}`)
  process.exit(1)
}

let packageJSON
try {
  packageJSON = JSON.parse(asar.extractFile(asarPath, 'package.json').toString('utf8'))
} catch (error) {
  fail(`app.asar package.json unreadable: ${error.message}`)
}

if (packageJSON.version !== expectedVersion) {
  fail(`app.asar package.json version mismatch: got '${packageJSON.version || ''}', want '${expectedVersion}'`)
}

if (packageJSON.name !== expectedPackageName) {
  fail(`app.asar package.json name mismatch: got '${packageJSON.name || ''}', want '${expectedPackageName}'`)
}

if (packageJSON.main !== expectedPackageMain) {
  fail(`app.asar package.json main mismatch: got '${packageJSON.main || ''}', want '${expectedPackageMain}'`)
}

for (const entry of requiredRuntimeEntries) {
  if (!entrySet.has(`/${entry}`)) {
    fail(`app.asar required runtime file missing: ${entry}`)
  }
}

const forbidden = entries.filter((entry) => (
  /\.test\.js$/.test(entry) ||
  /\.map$/.test(entry) ||
  /\.app(?:\/|$)/.test(entry) ||
  /^\/(?:release|dist)\/mac(?:-|\/)/.test(entry) ||
  /^\/src(?:\/|$)/.test(entry) ||
  /^\/tsconfig(?:\.[^/]+)?\.json$/.test(entry) ||
  /(?:^|\/)workagent-desktop$/.test(entry)
))

if (forbidden.length > 0) {
  console.error('inspect-mac-package.sh: app.asar contains forbidden dev/package/sidecar artifacts:')
  for (const entry of forbidden) {
    console.error(entry)
  }
  process.exit(1)
}

const unexpected = entries.filter((entry) => !allowedEntries.has(entry))
if (unexpected.length > 0) {
  console.error('inspect-mac-package.sh: app.asar contains unexpected payload entries:')
  for (const entry of unexpected) {
    console.error(entry)
  }
  process.exit(1)
}
NODE
  app_payload_label="$app_asar"
elif [ -f "$app_dir/package.json" ]; then
  app_payload_label="$app_dir"
  app_package_name="$(node -e "const p=require(process.argv[1]); process.stdout.write(p.name || '')" "$app_dir/package.json")"
  if [ "$app_package_name" != "$expected_package_name" ]; then
    echo "inspect-mac-package.sh: packaged app package.json name mismatch: got '$app_package_name', want '$expected_package_name'" >&2
    exit 1
  fi
  app_package_version="$(node -e "const p=require(process.argv[1]); process.stdout.write(p.version || '')" "$app_dir/package.json")"
  if [ "$app_package_version" != "$expected_version" ]; then
    echo "inspect-mac-package.sh: packaged app package.json version mismatch: got '$app_package_version', want '$expected_version'" >&2
    exit 1
  fi
  app_package_main="$(node -e "const p=require(process.argv[1]); process.stdout.write(p.main || '')" "$app_dir/package.json")"
  if [ "$app_package_main" != "$expected_package_main" ]; then
    echo "inspect-mac-package.sh: packaged app package.json main mismatch: got '$app_package_main', want '$expected_package_main'" >&2
    exit 1
  fi
  if [ ! -f "$app_dir/$app_package_main" ]; then
    echo "inspect-mac-package.sh: packaged app main file missing: $app_dir/$app_package_main" >&2
    exit 1
  fi
  for runtime_entry in "${required_runtime_entries[@]}"; do
    if [ ! -f "$app_dir/$runtime_entry" ]; then
      echo "inspect-mac-package.sh: packaged app required runtime file missing: $app_dir/$runtime_entry" >&2
      exit 1
    fi
  done
  forbidden_payload_entries="$(find "$app_dir" \( \
    -name "*.test.js" -o \
    -name "*.map" -o \
    -name "*.app" -o \
    -name "workagent-desktop" -o \
    -name "tsconfig*.json" -o \
    -path "$app_dir/src" -o \
    -path "$app_dir/src/*" -o \
    -path "$app_dir/release/*" -o \
    -path "$app_dir/dist/mac/*" -o \
    -path "$app_dir/dist/mac-*/*" \
  \) -print)"
  if [ -n "$forbidden_payload_entries" ]; then
    echo "inspect-mac-package.sh: packaged app contains packaged build/test artifacts:" >&2
    printf '%s\n' "$forbidden_payload_entries" >&2
    exit 1
  fi
  while IFS= read -r payload_entry; do
    rel_entry="${payload_entry#"$app_dir"/}"
    expected_entry=false
    if [ "$rel_entry" = "package.json" ] || [ "$rel_entry" = "dist" ]; then
      expected_entry=true
    else
      for runtime_entry in "${required_runtime_entries[@]}"; do
        if [ "$rel_entry" = "$runtime_entry" ]; then
          expected_entry=true
          break
        fi
      done
    fi
    if [ "$expected_entry" != true ]; then
      echo "inspect-mac-package.sh: packaged app contains unexpected payload entry: $payload_entry" >&2
      exit 1
    fi
  done < <(find "$app_dir" -mindepth 1 -print)
else
  echo "inspect-mac-package.sh: missing Electron app payload: expected $app_asar or $app_dir/package.json" >&2
  exit 1
fi

if [ ! -f "$sidecar_path" ]; then
  echo "inspect-mac-package.sh: missing packaged sidecar: $sidecar_path" >&2
  exit 1
fi

allowed_resource_entries=(
  "workagent-desktop"
  "renderer"
  "icon.icns"
  "LICENSE"
  "THIRD_PARTY_NOTICES.md"
  "third-party-licenses"
)
if [ -f "$app_asar" ]; then
  allowed_resource_entries+=("app.asar")
else
  allowed_resource_entries+=("app")
fi
if [ -e "$app_asar_unpacked" ]; then
  allowed_resource_entries+=("app.asar.unpacked")
fi

while IFS= read -r resource_entry; do
  resource_name="${resource_entry##*/}"
  allowed_resource=false
  for expected_resource in "${allowed_resource_entries[@]}"; do
    if [ "$resource_name" = "$expected_resource" ]; then
      allowed_resource=true
      break
    fi
  done
  if [ "$allowed_resource" != true ]; then
    echo "inspect-mac-package.sh: Contents/Resources contains unexpected top-level entry: $resource_entry" >&2
    exit 1
  fi
done < <(find "$resources_dir" -mindepth 1 -maxdepth 1 -print)

if [ -e "$app_asar_unpacked" ]; then
  asar_unpacked_entries="$(find "$app_asar_unpacked" -mindepth 1 -print)"
  if [ -n "$asar_unpacked_entries" ]; then
    echo "inspect-mac-package.sh: unexpected app.asar.unpacked payload entries:" >&2
    printf '%s\n' "$asar_unpacked_entries" >&2
    exit 1
  fi
fi

if [ ! -x "$sidecar_path" ]; then
  echo "inspect-mac-package.sh: packaged sidecar is not executable: $sidecar_path" >&2
  exit 1
fi

if ! grep -Fq "$expected_version" "$sidecar_path"; then
  echo "inspect-mac-package.sh: sidecar does not contain expected version marker '$expected_version'" >&2
  exit 1
fi

if [ "$require_bundled_renderer" -eq 1 ]; then
  validate_bundled_renderer() {
    for renderer_file in "$renderer_entry" "$renderer_css" "$renderer_js"; do
      if [ ! -s "$renderer_file" ]; then
        echo "inspect-mac-package.sh: missing bundled renderer file: $renderer_file" >&2
        exit 1
      fi
    done

    node - "$renderer_dir" <<'NODE'
const fs = require("node:fs");
const path = require("node:path");
const rendererDir = process.argv[2];
const allowed = new Set(["index.html", "styles.css", "renderer.js"]);
const unexpected = [];

function walk(dir) {
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const fullPath = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      walk(fullPath);
      continue;
    }
    if (!entry.isFile()) {
      unexpected.push(path.relative(rendererDir, fullPath));
      continue;
    }
    const rel = path.relative(rendererDir, fullPath).split(path.sep).join("/");
    if (!allowed.has(rel)) {
      unexpected.push(rel);
    }
  }
}

walk(rendererDir);
if (unexpected.length > 0) {
  console.error("inspect-mac-package.sh: bundled renderer contains unexpected files:");
  for (const file of unexpected) {
    console.error(file);
  }
  process.exit(1);
}
NODE

    node - "$renderer_entry" <<'NODE'
const fs = require("node:fs");
const entry = process.argv[2];
const html = fs.readFileSync(entry, "utf8");
const metaMatch = html.match(/<meta\s+[^>]*http-equiv=["']Content-Security-Policy["'][^>]*>/i);

function fail(message) {
  console.error(`inspect-mac-package.sh: ${message}`);
  process.exit(1);
}

if (!metaMatch) {
  fail("bundled renderer index.html missing Content-Security-Policy");
}
const contentMatch =
  metaMatch[0].match(/\bcontent="([^"]+)"/i) ||
  metaMatch[0].match(/\bcontent='([^']+)'/i);
if (!contentMatch) {
  fail("bundled renderer Content-Security-Policy meta tag must include a content attribute");
}

const expected = new Map([
  ["default-src", "'self'"],
  ["script-src", "'self'"],
  ["style-src", "'self'"],
  ["img-src", "'self' data:"],
  ["connect-src", "http://127.0.0.1:*"],
  ["object-src", "'none'"],
  ["base-uri", "'none'"],
  ["form-action", "'none'"],
  ["frame-ancestors", "'none'"],
]);

const actual = new Map();
for (const directive of contentMatch[1].split(";")) {
  const trimmed = directive.trim().replace(/\s+/g, " ");
  if (!trimmed) continue;
  const [name, ...rest] = trimmed.split(" ");
  if (actual.has(name)) {
    fail(`bundled renderer CSP directive is duplicated: ${name}`);
  }
  actual.set(name, rest.join(" "));
}

for (const [name, value] of expected) {
  if (actual.get(name) !== value) {
    fail(`bundled renderer CSP directive ${name} mismatch: got '${actual.get(name) || ""}', want '${value}'`);
  }
}
for (const name of actual.keys()) {
  if (!expected.has(name)) {
    fail(`bundled renderer CSP contains unexpected directive: ${name}`);
  }
}
NODE
    if ! grep -Fq 'href="./styles.css"' "$renderer_entry"; then
      echo "inspect-mac-package.sh: bundled renderer index.html must reference ./styles.css" >&2
      exit 1
    fi
    if ! grep -Fq 'src="./renderer.js"' "$renderer_entry"; then
      echo "inspect-mac-package.sh: bundled renderer index.html must reference ./renderer.js" >&2
      exit 1
    fi
    if ! grep -Fq 'id="source-code-link"' "$renderer_entry" || \
       ! grep -Fq 'href="https://github.com/jonnyquan/workmax"' "$renderer_entry" || \
       ! grep -Fq 'AGPL-3.0' "$renderer_entry"; then
      echo "inspect-mac-package.sh: bundled renderer must expose the AGPL source-code link" >&2
      exit 1
    fi
    if grep -R -E 'WORKMAX_LOCAL_TOKEN|refresh_token|access_token|Authorization: Bearer' "$renderer_dir" >/dev/null; then
      echo "inspect-mac-package.sh: bundled renderer must not embed token-like secrets" >&2
      exit 1
    fi
  }
  validate_bundled_renderer
fi

signing_status="unknown"
signing_detail=""
if signing_output="$(codesign -dv --verbose=4 "$app_path" 2>&1)"; then
  if printf '%s\n' "$signing_output" | grep -Fq "Signature=adhoc"; then
    signing_status="adhoc"
    signing_detail="ad-hoc signature; not suitable for public distribution"
  elif printf '%s\n' "$signing_output" | grep -Fq "TeamIdentifier=not set"; then
    signing_status="unsigned-or-untrusted"
    signing_detail="no TeamIdentifier"
  else
    signing_status="signed"
    signing_detail="$(printf '%s\n' "$signing_output" | sed -n 's/^TeamIdentifier=/TeamIdentifier=/p' | head -1)"
  fi
else
  signing_status="unsigned-or-unreadable"
  signing_detail="$(printf '%s\n' "$signing_output" | head -1)"
fi

codesign_verify_status="not verified"
if codesign_verify_output="$(codesign --verify --deep --strict --verbose=2 "$app_path" 2>&1)"; then
  codesign_verify_status="passed"
else
  codesign_verify_status="failed: $(printf '%s\n' "$codesign_verify_output" | head -1)"
fi

if [ "$require_developer_id_signature" -eq 1 ]; then
  case "$signing_status" in
    signed) ;;
    adhoc)
      echo "inspect-mac-package.sh: app has an ad-hoc signature; use a Developer ID Application certificate" >&2
      exit 1
      ;;
    unsigned-or-untrusted)
      echo "inspect-mac-package.sh: app signature has no TeamIdentifier; use Developer ID Application signing" >&2
      exit 1
      ;;
    *)
      echo "inspect-mac-package.sh: app is not codesigned strongly enough for public release" >&2
      printf '%s\n' "$signing_detail" >&2
      exit 1
      ;;
  esac
  if ! printf '%s\n' "$signing_output" | grep -Fq "Runtime Version="; then
    echo "inspect-mac-package.sh: app signature is missing hardened runtime; enable hardenedRuntime for Developer ID notarization" >&2
    exit 1
  fi
  if ! printf '%s\n' "$signing_output" | grep -Eq '^Authority=Developer ID Application:'; then
    echo "inspect-mac-package.sh: app signature is not a Developer ID Application signature" >&2
    echo "    codesign -dv must report Authority=Developer ID Application: ... for public release artifacts" >&2
    exit 1
  fi
  if [ "$codesign_verify_status" != "passed" ]; then
    echo "inspect-mac-package.sh: app signature failed strict verification" >&2
    printf '%s\n' "$codesign_verify_output" >&2
    exit 1
  fi
fi

gatekeeper_status="not assessed"
if gatekeeper_output="$(spctl --assess --type execute --verbose=4 "$app_path" 2>&1)"; then
  gatekeeper_status="passed"
else
  gatekeeper_status="failed: $(printf '%s\n' "$gatekeeper_output" | head -1)"
fi

echo "ok app bundle: $app_path"
echo "ok bundle id: $bundle_id"
echo "ok app version: $bundle_version"
echo "ok main executable: $main_executable"
echo "ok app payload: $app_payload_label"
echo "ok sidecar: $sidecar_path"
echo "ok sidecar version marker: $expected_version"
echo "ok release license material: $license_path"
echo "ok third-party license bundle: $third_party_licenses_dir"
if [ "$require_bundled_renderer" -eq 1 ]; then
  echo "ok bundled renderer: $renderer_dir"
fi
if [ "$require_app_icon" -eq 1 ]; then
  echo "ok app icon: $expected_icon_path"
fi
echo "info signing status: $signing_status ($signing_detail)"
echo "info codesign verify: $codesign_verify_status"
echo "info gatekeeper assessment: $gatekeeper_status"
