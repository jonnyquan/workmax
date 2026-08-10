#!/usr/bin/env bash
# Assemble a macOS .app for WorkMax Desktop.
#
#   ./desktop/scripts/build-mac.sh [arm64|x64]     build for one architecture
#   ./desktop/scripts/build-mac.sh --preflight-only  validate inputs, build nothing
#
# The Wails bundle is small enough to assemble by hand: one executable, one
# renderer directory, an icon and an Info.plist. There is no packaging
# framework here on purpose — the previous shell's packaging was a large
# configuration surface that mostly existed to stop electron-builder from
# sweeping the wrong files in, and this layout has nothing to sweep.
#
# Every build is inspected before it is reported as usable
# (inspect-mac-package.sh), so a bundle that reaches you has already been
# enumerated: no unreviewed files, no stale executable, no packaged sidecar.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RENDERER_SRC="$REPO_ROOT/desktop/renderer/en/desktop"
ICON_SRC="$REPO_ROOT/desktop/build/icons/icon.icns"
RELEASE_DIR="${WORKMAX_DESKTOP_RELEASE_DIR:-$REPO_ROOT/desktop/wails/release}"

# The renderer files that ship, and nothing else. Kept in step with the
# allowlists in check-bundled-renderer.sh and inspect-mac-package.sh: three
# places must agree, so all three are explicit rather than globbing a
# directory and hoping.
RENDERER_FILES=(index.html styles.css renderer.js dom.js fence.js protocol.js events.js markdown.js transcript.js composer.js threads.js context-panel.js shim.js lib/desktop-bridge.js)

preflight_only=0
target_arch=""

while [ $# -gt 0 ]; do
  case "$1" in
    --preflight-only) preflight_only=1 ;;
    arm64|x64) target_arch="$1" ;;
    -h|--help) sed -n '2,8p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "build-mac.sh: unknown argument $1" >&2; exit 2 ;;
  esac
  shift
done

if [ -z "$target_arch" ]; then
  case "$(uname -m)" in
    arm64) target_arch="arm64" ;;
    x86_64) target_arch="x64" ;;
    *) echo "build-mac.sh: cannot detect host arch; pass arm64 or x64" >&2; exit 2 ;;
  esac
fi
case "$target_arch" in
  arm64) goarch="arm64" ;;
  x64) goarch="amd64" ;;
esac

fail() { echo "build-mac.sh: $1" >&2; exit 1; }

version="$(grep -oE '"[0-9][^"]*"' "$REPO_ROOT/server/desktop/buildinfo/buildinfo.go" | head -1 | tr -d '"')"
[ -n "$version" ] || fail "cannot read the version from server/desktop/buildinfo/buildinfo.go"

# --- preflight ---------------------------------------------------------------
# Everything checked here is an input we control, so a failure means the repo
# is wrong rather than the build machine.
[ -s "$ICON_SRC" ] || fail "missing or empty $ICON_SRC"
[ "$(head -c 4 "$ICON_SRC")" = "icns" ] || fail "$ICON_SRC is not a valid icns file"
for file in "${RENDERER_FILES[@]}"; do
  [ -s "$RENDERER_SRC/$file" ] || fail "missing or empty renderer file: $file"
done
"$REPO_ROOT/desktop/scripts/check-bundled-renderer.sh" >/dev/null || \
  fail "the renderer source failed its own checks; fix that before packaging it"

if [ "$preflight_only" -eq 1 ]; then
  echo "ok build-mac preflight (arch=$target_arch version=$version)"
  exit 0
fi

# --- build -------------------------------------------------------------------
# CGO is required: Wails binds Cocoa and WebKit through it. Cross-compiling
# with cgo needs a matching C toolchain, so building for the other arch on this
# machine is refused rather than producing something that fails at launch.
host_arch="$(uname -m)"
if { [ "$goarch" = "arm64" ] && [ "$host_arch" != "arm64" ]; } || \
   { [ "$goarch" = "amd64" ] && [ "$host_arch" != "x86_64" ]; }; then
  fail "cannot cross-compile $target_arch from $host_arch: cgo needs a matching C toolchain. Build on that architecture, or set CC yourself and re-run."
fi

app_dir="$RELEASE_DIR/$target_arch/WorkMax Desktop.app"
contents="$app_dir/Contents"
echo "==> building workmax-desktop for darwin/$goarch (version=$version)"
rm -rf "$app_dir"
mkdir -p "$contents/MacOS" "$contents/Resources/renderer/en/desktop/lib"

( cd "$REPO_ROOT/desktop/wails" \
  && GOOS=darwin GOARCH="$goarch" CGO_ENABLED=1 \
     go build -tags desktop \
       -ldflags "-s -w -X server/desktop/buildinfo.Version=$version" \
       -o "$contents/MacOS/WorkMax Desktop" . )
chmod +x "$contents/MacOS/WorkMax Desktop"

# --- assemble ----------------------------------------------------------------
for file in "${RENDERER_FILES[@]}"; do
  cp "$RENDERER_SRC/$file" "$contents/Resources/renderer/en/desktop/$file"
done
cp "$ICON_SRC" "$contents/Resources/icon.icns"
cp "$REPO_ROOT/LICENSE" "$contents/Resources/LICENSE"
cp "$REPO_ROOT/THIRD_PARTY_NOTICES.md" "$contents/Resources/THIRD_PARTY_NOTICES.md"

# LSMinimumSystemVersion 11.0 matches what the toolchain links against.
# NSHighResolutionCapable because a webview app on a Retina display without it
# renders blurry. LSApplicationCategoryType is required for distribution.
cat > "$contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>WorkMax Desktop</string>
  <key>CFBundleDisplayName</key><string>WorkMax Desktop</string>
  <key>CFBundleIdentifier</key><string>ai.workmax.desktop</string>
  <key>CFBundleExecutable</key><string>WorkMax Desktop</string>
  <key>CFBundleIconFile</key><string>icon.icns</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>$version</string>
  <key>CFBundleVersion</key><string>$version</string>
  <key>LSMinimumSystemVersion</key><string>11.0</string>
  <key>LSApplicationCategoryType</key><string>public.app-category.productivity</string>
  <key>NSHighResolutionCapable</key><true/>
</dict>
</plist>
PLIST

# --- inspect -----------------------------------------------------------------
# Not optional. A build that has not been enumerated is a build whose contents
# nobody knows, and this is the only moment where fixing it is cheap.
"$REPO_ROOT/desktop/scripts/inspect-mac-package.sh" \
  --require-bundled-renderer --require-app-icon "$app_dir"

size="$(du -sh "$app_dir" | cut -f1 | tr -d ' ')"
echo "ok build-mac: $app_dir (arch=$target_arch version=$version size=$size)"
echo "    unsigned. Sign with a Developer ID Application certificate, then:"
echo "    ./desktop/scripts/notarize-mac.sh $target_arch"
