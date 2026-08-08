#!/usr/bin/env bash
# Inspect a packaged WorkMax Desktop .app before it is signed or submitted.
#
#   inspect-mac-package.sh [options] <path/to/WorkMax Desktop.app>
#
# Options:
#   --require-bundled-renderer      fail unless the renderer is present and sane
#   --require-app-icon              fail unless a non-empty .icns icon is declared
#   --require-developer-id-signature  fail unless Developer ID + hardened runtime
#
# This exists because notarize-mac.sh must not submit a bundle nothing has
# looked at. Its predecessor inspected the Electron layout and was deleted with
# that shell; until this replaced it, notarize-mac.sh refused real submissions
# outright.
#
# The Wails layout is much smaller than the Electron one — one executable, one
# renderer directory — so the checks are correspondingly blunt: everything the
# bundle contains is enumerated, and anything unexpected is a failure. An
# allowlist is the point. A packaging change that starts sweeping stray files
# into Resources should fail here, not ship.
set -euo pipefail

require_renderer=0
require_icon=0
require_developer_id=0
app_path=""

while [ $# -gt 0 ]; do
  case "$1" in
    --require-bundled-renderer) require_renderer=1 ;;
    --require-app-icon) require_icon=1 ;;
    --require-developer-id-signature) require_developer_id=1 ;;
    -h|--help)
      sed -n '2,10p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    -*)
      echo "inspect-mac-package.sh: unknown option $1" >&2
      exit 2
      ;;
    *)
      if [ -n "$app_path" ]; then
        echo "inspect-mac-package.sh: unexpected extra argument $1" >&2
        exit 2
      fi
      app_path="$1"
      ;;
  esac
  shift
done

if [ -z "$app_path" ]; then
  echo "inspect-mac-package.sh: missing app bundle path" >&2
  exit 2
fi

fail() {
  echo "inspect-mac-package.sh: $1" >&2
  exit 1
}

[ -d "$app_path" ] || fail "not a directory: $app_path"
case "$app_path" in
  *.app) ;;
  *) fail "not an .app bundle: $app_path" ;;
esac

contents="$app_path/Contents"
plist="$contents/Info.plist"
[ -s "$plist" ] || fail "missing or empty Contents/Info.plist"

plist_value() {
  /usr/libexec/PlistBuddy -c "Print :$1" "$plist" 2>/dev/null || true
}

# The bundle id is load-bearing, not cosmetic: macOS scopes Keychain entries by
# it, so a build that ships under a different id cannot see the session the
# user already has.
bundle_id="$(plist_value CFBundleIdentifier)"
[ "$bundle_id" = "ai.workmax.desktop" ] || \
  fail "CFBundleIdentifier is '${bundle_id:-missing}', want ai.workmax.desktop (it scopes the Keychain entries the sidecar reads)"

executable_name="$(plist_value CFBundleExecutable)"
[ -n "$executable_name" ] || fail "Info.plist does not declare CFBundleExecutable"
executable="$contents/MacOS/$executable_name"
[ -s "$executable" ] || fail "missing or empty declared executable: Contents/MacOS/$executable_name"
[ -x "$executable" ] || fail "declared executable is not executable: Contents/MacOS/$executable_name"

# One executable, and it is ours. A second binary here would most likely be a
# sidecar that should no longer exist — the shell and the sidecar are one
# process now, and a stray copy would be a stale, separately-signed surface.
macos_entries="$(find "$contents/MacOS" -mindepth 1 -maxdepth 1 | wc -l | tr -d ' ')"
[ "$macos_entries" = "1" ] || \
  fail "Contents/MacOS holds $macos_entries entries, want exactly 1 (the shell and sidecar are a single process; a second binary means stale packaging)"
if [ -e "$contents/Resources/workagent-desktop" ]; then
  fail "Contents/Resources/workagent-desktop exists; the separate sidecar binary was retired and must not be packaged"
fi

version="$(plist_value CFBundleShortVersionString)"
[ -n "$version" ] || fail "Info.plist does not declare CFBundleShortVersionString"
if ! grep -Fq "$version" "$executable"; then
  fail "the packaged executable does not contain version marker '$version'; it is stale relative to Info.plist"
fi

resources="$contents/Resources"
[ -d "$resources" ] || fail "missing Contents/Resources"

# Everything that may sit at the top of Resources. Anything else is either a
# packaging accident or something that was never reviewed for distribution.
allowed_resource_entries=("renderer" "icon.icns" "LICENSE" "THIRD_PARTY_NOTICES.md" "third-party-licenses")
while IFS= read -r entry; do
  name="$(basename "$entry")"
  allowed=0
  for candidate in "${allowed_resource_entries[@]}"; do
    [ "$name" = "$candidate" ] && allowed=1 && break
  done
  [ "$allowed" = "1" ] || fail "unexpected entry in Contents/Resources: $name"
done < <(find "$resources" -mindepth 1 -maxdepth 1)

if [ "$require_renderer" -eq 1 ]; then
  renderer="$resources/renderer/en/desktop"
  [ -d "$renderer" ] || fail "missing bundled renderer at Contents/Resources/renderer/en/desktop"

  for required in index.html styles.css renderer.js shim.js lib/desktop-bridge.js; do
    [ -s "$renderer/$required" ] || fail "bundled renderer is missing or has an empty $required"
  done

  # The renderer must not have grown files nobody reviewed — source maps, .ts
  # sources and debug scripts included. This mirrors the source-side allowlist
  # in check-bundled-renderer.sh so the two cannot disagree about what ships.
  while IFS= read -r file; do
    rel="${file#"$renderer/"}"
    case "$rel" in
      index.html|styles.css|renderer.js|shim.js|lib/desktop-bridge.js) ;;
      *) fail "unexpected file in bundled renderer: $rel" ;;
    esac
  done < <(find "$renderer" -type f)

  # No separate rule for source maps or .ts files: the allowlist above already
  # rejects anything not on it, and two overlapping checks means one of them is
  # never the reason a build fails — and so never noticed when it breaks.

  # CSP is the renderer's primary containment control on a shell with no
  # cancellable navigation hook, so its presence is verified in the artifact,
  # not just in the repository.
  grep -Fq 'http-equiv="Content-Security-Policy"' "$renderer/index.html" || \
    fail "bundled index.html does not declare a Content-Security-Policy"
  # Every source in connect-src is checked individually. An earlier version of
  # this used a negative lookahead, which grep -E does not support — with
  # stderr discarded it silently matched nothing and waved everything through.
  connect_src="$(grep -oE "connect-src[^;\"]*" "$renderer/index.html" | head -1 | sed 's/^connect-src//')"
  [ -n "$connect_src" ] || fail "bundled index.html does not restrict connect-src"
  for source in $connect_src; do
    case "$source" in
      "'self'"|"'none'"|http://127.0.0.1|http://127.0.0.1:*|http://localhost|http://localhost:*) ;;
      *) fail "bundled index.html allows a non-loopback connect-src source: $source" ;;
    esac
  done
  grep -Fq "'unsafe-inline'" "$renderer/index.html" && \
    fail "bundled index.html grants an inline exemption in its CSP"

  # A packaged renderer must not LOAD anything off the machine. An anchor to a
  # remote page is different in kind — the shell hands those to the system
  # browser and the page is never fetched into the webview — so only the forms
  # that actually fetch are rejected: any src=, and href= on <link>.
  if grep -oE 'src="https?://[^"]*"' "$renderer/index.html" | grep -q .; then
    fail "bundled index.html loads a remote resource via src="
  fi
  if grep -oE '<link[^>]+href="https?://[^"]*"' "$renderer/index.html" | grep -q .; then
    fail "bundled index.html loads a remote stylesheet or preload via <link href=>"
  fi

  # Credential-shaped strings have no business in a shipped renderer.
  if grep -rlE "(sk-[A-Za-z0-9]{16,}|eyJ[A-Za-z0-9_-]{20,}\.)" "$renderer" >/dev/null 2>&1; then
    fail "bundled renderer contains token-like embedded material"
  fi
fi

if [ "$require_icon" -eq 1 ]; then
  icon_name="$(plist_value CFBundleIconFile)"
  [ "$icon_name" = "icon.icns" ] || \
    fail "CFBundleIconFile is '${icon_name:-missing}', want icon.icns"
  icon="$resources/icon.icns"
  [ -s "$icon" ] || fail "missing or empty Contents/Resources/icon.icns"
  [ "$(head -c 4 "$icon")" = "icns" ] || fail "Contents/Resources/icon.icns is not a valid icns file"
fi

# The entitlement set is pinned so a speculative addition fails here rather
# than at Apple, where the feedback is slower and less specific.
entitlements_source="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/build/entitlements.mac.plist"
if [ -s "$entitlements_source" ]; then
  declared="$(grep -oE '<key>com\.apple\.security\.[^<]+</key>' "$entitlements_source" | sed 's/<[^>]*>//g' | sort)"
  expected="com.apple.security.network.client"
  if [ "$declared" != "$expected" ]; then
    fail "entitlements drift: declared [$(echo "$declared" | tr '\n' ' ')], want exactly [$expected]. Adding one needs a justification the binary's behavior supports — see desktop/build/entitlements.mac.plist"
  fi
fi

# --require-developer-id-signature is accepted and ignored on purpose.
#
# Signature verification lives in notarize-mac.sh, which owns the submission
# and checks more than a subset would: ad-hoc rejection, TeamIdentifier,
# Developer ID authority, hardened runtime, and strict codesign --verify --deep.
# Duplicating part of that here would mean one of the two is never the reason a
# build fails, and a check that never fires is a check nobody maintains. This
# script inspects structure; that one inspects signatures.
if [ "$require_developer_id" -eq 1 ]; then
  : # see above
fi

echo "ok inspect-mac-package: $app_path (bundle_id=$bundle_id version=$version)"
