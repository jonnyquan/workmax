#!/usr/bin/env bash
#
# Notarize a previously built macOS WorkMax Desktop DMG.
#
# Build first:
#   ./desktop/scripts/build-mac.sh arm64
#   ./desktop/scripts/build-mac.sh x64
#
# Credentials:
#   Prefer an Apple notarytool keychain profile:
#     WORKMAX_NOTARY_KEYCHAIN_PROFILE=workmax-notary ./desktop/scripts/notarize-mac.sh arm64
#
#   Or provide app-specific password credentials:
#     APPLE_ID=release@example.com \
#     APPLE_TEAM_ID=ABCDE12345 \
#     APPLE_APP_SPECIFIC_PASSWORD=xxxx-xxxx-xxxx-xxxx \
#     ./desktop/scripts/notarize-mac.sh arm64
#
# Use --dry-run to validate paths, signing state, bundled-renderer readiness,
# and credentials without submitting anything to Apple.
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
Usage:
  notarize-mac.sh [--dry-run] [--allow-hosted-renderer] [arm64|x64]
  notarize-mac.sh [--dry-run] [--allow-hosted-renderer] <path/to/WorkMax Desktop-<version>-<arch>.dmg>

Options:
  --dry-run                  validate without submitting to Apple
  --allow-hosted-renderer    legacy option name: skip bundled Renderer entry
                             inspection for controlled dry-runs only. This does
                             not enable a runtime hosted Renderer.
                             Arbitrary DMG paths that cannot be mapped to a
                             neighboring .app are dry-run only.

Environment:
  WORKMAX_NOTARY_KEYCHAIN_PROFILE       notarytool keychain profile name

  Or all of:
  APPLE_ID                           Apple ID email
  APPLE_TEAM_ID                      Apple Developer Team ID
  APPLE_APP_SPECIFIC_PASSWORD        App-specific password
EOF
}

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && cd .. && pwd)"

dry_run=0
allow_hosted_renderer=0
target=""

while [ "$#" -gt 0 ]; do
  case "$1" in
    --dry-run)
      dry_run=1
      shift
      ;;
    --allow-hosted-renderer)
      allow_hosted_renderer=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    --*)
      echo "notarize-mac.sh: unknown option: $1" >&2
      usage
      exit 2
      ;;
    *)
      if [ -n "$target" ]; then
        echo "notarize-mac.sh: unexpected extra argument: $1" >&2
        usage
        exit 2
      fi
      target="$1"
      shift
      ;;
  esac
done

if [ -z "$target" ]; then
  case "$(uname -m)" in
    arm64)  target="arm64" ;;
    x86_64) target="x64" ;;
    *)
      echo "notarize-mac.sh: cannot detect host arch from $(uname -m)" >&2
      echo "  pass arm64, x64, or a .dmg path explicitly" >&2
      exit 1
      ;;
  esac
fi

desktop_version="$(cd "$REPO_ROOT/desktop/electron" && node -p "require('./package.json').version")"

case "$target" in
  arm64)
    dmg_path="$REPO_ROOT/desktop/electron/release/WorkMax Desktop-${desktop_version}-arm64.dmg"
    app_path="$REPO_ROOT/desktop/electron/release/mac-arm64/WorkMax Desktop.app"
    ;;
  x64)
    dmg_path="$REPO_ROOT/desktop/electron/release/WorkMax Desktop-${desktop_version}-x64.dmg"
    app_path="$REPO_ROOT/desktop/electron/release/mac/WorkMax Desktop.app"
    ;;
  *.dmg)
    dmg_path="$target"
    app_path=""
    dmg_dir="$(cd "$(dirname "$dmg_path")" && pwd)"
    dmg_name="$(basename "$dmg_path")"
    case "$dmg_name" in
      "WorkMax Desktop-${desktop_version}-arm64.dmg")
        app_path="$dmg_dir/mac-arm64/WorkMax Desktop.app"
        ;;
      "WorkMax Desktop-${desktop_version}-x64.dmg")
        app_path="$dmg_dir/mac/WorkMax Desktop.app"
        ;;
    esac
    ;;
  *)
    usage
    exit 1
    ;;
esac

if [ ! -s "$dmg_path" ]; then
  echo "notarize-mac.sh: missing or empty DMG: $dmg_path" >&2
  echo "  Run desktop/scripts/build-mac.sh first." >&2
  exit 1
fi

if [ "$allow_hosted_renderer" -eq 1 ] && [ "$dry_run" -eq 0 ]; then
  echo "notarize-mac.sh: --allow-hosted-renderer is dry-run only; public notarization requires the bundled renderer gate" >&2
  exit 1
fi

if [ -n "$app_path" ]; then
  if [ ! -d "$app_path" ]; then
    echo "notarize-mac.sh: missing app bundle for inspection: $app_path" >&2
    echo "  Run desktop/scripts/build-mac.sh $target first." >&2
    exit 1
  fi
  inspect_args=()
  if [ "$allow_hosted_renderer" -eq 0 ]; then
    inspect_args+=(--require-bundled-renderer)
  fi
  inspect_args+=(--require-app-icon --require-developer-id-signature)
  "$REPO_ROOT/desktop/scripts/inspect-mac-package.sh" "${inspect_args[@]}" "$app_path" >/dev/null

  if ! codesign_output="$(codesign -dv --verbose=4 "$app_path" 2>&1)"; then
    echo "notarize-mac.sh: app is not codesigned strongly enough for notarization" >&2
    printf '%s\n' "$codesign_output" >&2
    exit 1
  fi
  if printf '%s\n' "$codesign_output" | grep -Fq "Signature=adhoc"; then
    echo "notarize-mac.sh: app has an ad-hoc signature; use a Developer ID Application certificate" >&2
    exit 1
  fi
  if printf '%s\n' "$codesign_output" | grep -Fq "TeamIdentifier=not set"; then
    echo "notarize-mac.sh: app signature has no TeamIdentifier; use Developer ID Application signing" >&2
    exit 1
  fi
  if ! printf '%s\n' "$codesign_output" | grep -Fq "Runtime Version="; then
    echo "notarize-mac.sh: app signature is missing hardened runtime; enable hardenedRuntime for Developer ID notarization" >&2
    exit 1
  fi
  if ! printf '%s\n' "$codesign_output" | grep -Eq '^Authority=Developer ID Application:'; then
    echo "notarize-mac.sh: app signature is not a Developer ID Application signature" >&2
    echo "  codesign -dv must report Authority=Developer ID Application: ... before notarization" >&2
    exit 1
  fi
  if ! codesign_verify_output="$(codesign --verify --deep --strict --verbose=2 "$app_path" 2>&1)"; then
    echo "notarize-mac.sh: app signature failed strict verification" >&2
    printf '%s\n' "$codesign_verify_output" >&2
    exit 1
  fi
elif [ "$allow_hosted_renderer" -eq 0 ]; then
  echo "notarize-mac.sh: explicit DMG path cannot verify the .app bundle or bundled renderer" >&2
  echo "  Pass arm64/x64 so the script can inspect release/mac*/WorkMax Desktop.app, or use --allow-hosted-renderer for controlled early-access only." >&2
  exit 1
elif [ "$dry_run" -eq 0 ]; then
  echo "notarize-mac.sh: refusing to submit explicit DMG path without inspecting its .app bundle" >&2
  echo "  Pass arm64/x64, pass a versioned release DMG with a neighboring release/mac*/WorkMax Desktop.app, or use --dry-run for local validation only." >&2
  exit 1
fi

notary_args=()
if [ -n "${WORKMAX_NOTARY_KEYCHAIN_PROFILE:-}" ]; then
  notary_args=(--keychain-profile "$WORKMAX_NOTARY_KEYCHAIN_PROFILE")
else
  missing=()
  [ -n "${APPLE_ID:-}" ] || missing+=("APPLE_ID")
  [ -n "${APPLE_TEAM_ID:-}" ] || missing+=("APPLE_TEAM_ID")
  [ -n "${APPLE_APP_SPECIFIC_PASSWORD:-}" ] || missing+=("APPLE_APP_SPECIFIC_PASSWORD")
  if [ "${#missing[@]}" -gt 0 ]; then
    echo "notarize-mac.sh: missing notarization credential env: ${missing[*]}" >&2
    usage
    exit 1
  fi
  notary_args=(--apple-id "$APPLE_ID" --team-id "$APPLE_TEAM_ID" --password "$APPLE_APP_SPECIFIC_PASSWORD")
fi

echo "notarize-mac.sh: DMG ready: $dmg_path"
if [ "$dry_run" -eq 1 ]; then
  echo "notarize-mac.sh: dry run only; skipping Apple submission and stapling"
  exit 0
fi

if ! command -v xcrun >/dev/null 2>&1; then
  echo "notarize-mac.sh: xcrun not found; install Xcode command line tools" >&2
  exit 1
fi

echo "notarize-mac.sh: submitting to Apple notary service"
xcrun notarytool submit "$dmg_path" "${notary_args[@]}" --wait

echo "notarize-mac.sh: stapling notarization ticket"
xcrun stapler staple "$dmg_path"

echo "notarize-mac.sh: verifying stapled DMG"
xcrun stapler validate "$dmg_path"
spctl -a -vv -t open --context context:primary-signature "$dmg_path"

echo "notarize-mac.sh: notarization complete"
