#!/usr/bin/env bash
# Build and run WorkMax Desktop from source.
#
#   ./desktop/scripts/dev.sh                 launch the app (webview + embedded sidecar)
#   ./desktop/scripts/dev.sh --serve-only    sidecar only: no window, prints its loopback URL
#   ./desktop/scripts/dev.sh --kill-check    the W1 SSE transport check
#   ./desktop/scripts/dev.sh --verify-shim   load the shipped renderer shim and check its contract
#
# One binary, one build. The Electron shell and its separate sidecar process
# are gone; --serve-only is what replaced "run the sidecar by hand", and it is
# what the smoke scripts drive.
#
# CGO is required on macOS: Wails binds Cocoa/WebKit through it, and the local
# knowledge index (ONNX Runtime) needs it too.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BIN_DIR="$REPO_ROOT/desktop/wails/bin"
BINARY="$BIN_DIR/workmax-desktop"

target_goos="$(go env GOHOSTOS)"
target_goarch="$(go env GOHOSTARCH)"

# The renderer's generated bridge library is checked in, so a normal dev run
# needs no TypeScript toolchain. Regenerate only when the source is newer.
lib="$REPO_ROOT/desktop/renderer/en/desktop/lib/desktop-bridge.js"
src="$REPO_ROOT/desktop/renderer/src/desktop-bridge.ts"
if [ ! -f "$lib" ] || [ "$src" -nt "$lib" ]; then
  echo "==> regenerating renderer bridge library"
  "$REPO_ROOT/desktop/scripts/build-renderer-lib.sh"
fi

echo "==> building workmax-desktop for $target_goos/$target_goarch"
mkdir -p "$BIN_DIR"
( cd "$REPO_ROOT/desktop/wails" \
  && GOOS="$target_goos" GOARCH="$target_goarch" CGO_ENABLED=1 \
     go build -tags desktop -o "$BINARY" . )
echo "    built: $BINARY"

# Point the app at the working tree's renderer so edits appear on relaunch
# without a packaging step. Packaged builds resolve this from the bundle.
export WORKMAX_DESKTOP_RENDERER_DIR="${WORKMAX_DESKTOP_RENDERER_DIR:-$REPO_ROOT/desktop/renderer/en/desktop}"
echo "==> renderer: $WORKMAX_DESKTOP_RENDERER_DIR"

exec "$BINARY" "$@"
