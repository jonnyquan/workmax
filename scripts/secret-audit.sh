#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
cd "$repo_root"

# Include tracked and untracked source while honoring .gitignore. This matters
# during the imported baseline, where almost all application files are still
# untracked. Never print matching text: a failed audit reports paths and rule
# names only.
candidates=()
while IFS= read -r -d '' candidate; do
  case "$candidate" in
    scripts/secret-audit.sh)
      continue
      ;;
  esac
  if [ -f "$candidate" ]; then
    candidates+=("$candidate")
  fi
done < <(git ls-files --cached --others --exclude-standard -z)

if [ "${#candidates[@]}" -eq 0 ]; then
  echo "secret-audit: no source candidates found"
  exit 0
fi

labels=(
  "private-key"
  "aws-access-key"
  "google-api-key"
  "github-token"
  "stripe-secret"
  "provider-secret"
)
patterns=(
  '-----BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY-----'
  'AKIA[0-9A-Z]{16}'
  'AIza[0-9A-Za-z_-]{35}'
  'gh[pousr]_[0-9A-Za-z_]{20,}'
  '(sk_(live|test)|whsec)_[0-9A-Za-z]{16,}'
  'sk-(?!test[-_]|example[-_]|fake[-_]|redacted[-_])[0-9A-Za-z_-]{24,}'
)

failed=0
for index in "${!patterns[@]}"; do
  matches=""
  if matches="$(rg -l --no-messages --pcre2 -e "${patterns[$index]}" -- "${candidates[@]}")"; then
    failed=1
    echo "secret-audit: ${labels[$index]} pattern found in:" >&2
    while IFS= read -r match; do
      [ -n "$match" ] && echo "  $match" >&2
    done <<<"$matches"
  fi
done

if [ "$failed" -ne 0 ]; then
  echo "secret-audit: failed; inspect and rotate/remove credentials before staging" >&2
  exit 1
fi

echo "secret-audit: checked ${#candidates[@]} source candidates; no known credential pattern found"
echo "secret-audit: note: CI supplements this known-pattern check with a full-history Gitleaks scan"
