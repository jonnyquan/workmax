#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"

for required_command in git go node npm; do
  if ! command -v "$required_command" >/dev/null 2>&1; then
    echo "doctor: missing required command: $required_command" >&2
    exit 1
  fi
done

for required_file in \
  "$repo_root/server/go.mod" \
  "$repo_root/server/config.example.yaml" \
  "$repo_root/server/config.release.example.yaml" \
  "$repo_root/desktop/electron/package-lock.json" \
  "$repo_root/desktop/contracts/desktop-boundaries.v0.json"; do
  if [ ! -f "$required_file" ]; then
    echo "doctor: missing required file: $required_file" >&2
    exit 1
  fi
done

for tracked_example in server/config.example.yaml server/config.release.example.yaml; do
  if git -C "$repo_root" check-ignore -q "$tracked_example"; then
    echo "doctor: sanitized example is unexpectedly ignored: $tracked_example" >&2
    exit 1
  fi
done

node_major="$(node -p 'Number(process.versions.node.split(".")[0])')"
if [ "$node_major" -lt 20 ]; then
  echo "doctor: Node.js 20+ required, found $(node --version)" >&2
  exit 1
fi

for sensitive_path in server/config.yaml server/config-prod.yaml switchglm.sh switchkimi.sh; do
  if ! git -C "$repo_root" check-ignore -q "$sensitive_path"; then
    echo "doctor: sensitive local path is not ignored: $sensitive_path" >&2
    exit 1
  fi
done

node "$repo_root/desktop/scripts/check-desktop-boundaries.mjs"

tracked_count="$(git -C "$repo_root" ls-files | wc -l | tr -d ' ')"
if [ "$tracked_count" -le 1 ]; then
  echo "doctor: warning: imported source baseline is not tracked yet; do not use git add ." >&2
fi

echo "doctor: go=$(go version | awk '{print $3}') node=$(node --version) npm=$(npm --version)"
echo "doctor: checks passed"
