#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
cd "$repo_root"

area="all"
print_allowed_nul=0
self_test=0

usage() {
  cat <<'EOF'
Usage: ./scripts/source-baseline-audit.sh [--area AREA] [--print-allowed0] [--self-test]

Read-only audit of `git ls-files --others --exclude-standard`.

AREA is one of: all, root, docs, desktop, scripts, server, github.
--print-allowed0 emits only the approved paths as NUL-delimited stdout after a
clean audit. Diagnostics remain on stderr, making the output safe for a future
reviewed `git add --pathspec-from-file=... --pathspec-file-nul` workflow.
--self-test exercises representative allow, exception, deny, and unclassified
path rules without reading the worktree candidate list.

This command never stages, commits, deletes, moves, or rewrites repository
files. It inspects path names, Git ignore state, file type, and symlink status;
it never prints file contents.
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --area)
      if [ "$#" -lt 2 ]; then
        echo "source-baseline: --area requires a value" >&2
        exit 2
      fi
      area="$2"
      shift 2
      ;;
    --print-allowed0)
      print_allowed_nul=1
      shift
      ;;
    --self-test)
      self_test=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "source-baseline: unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

case "$area" in
  all|root|docs|desktop|scripts|server|github) ;;
  *)
    echo "source-baseline: unsupported area: $area" >&2
    exit 2
    ;;
esac

area_matches() {
  candidate_path="$1"
  case "$area" in
    all)
      return 0
      ;;
    root)
      case "$candidate_path" in
        */*) return 1 ;;
        *) return 0 ;;
      esac
      ;;
    docs)
      case "$candidate_path" in
        ProjectDocs/*) return 0 ;;
        *) return 1 ;;
      esac
      ;;
    desktop)
      case "$candidate_path" in
        desktop/*) return 0 ;;
        *) return 1 ;;
      esac
      ;;
    scripts)
      case "$candidate_path" in
        scripts/*) return 0 ;;
        *) return 1 ;;
      esac
      ;;
    server)
      case "$candidate_path" in
        server/*) return 0 ;;
        *) return 1 ;;
      esac
      ;;
    github)
      case "$candidate_path" in
        .github/*) return 0 ;;
        *) return 1 ;;
      esac
      ;;
  esac
}

# classify_path sets classification and policy_reason. Exceptions are narrow,
# reviewed binary/build-tree inputs that would otherwise match a deny rule.
classify_path() {
  candidate_path="$1"
  classification=""
  policy_reason=""

  if [ -L "$candidate_path" ]; then
    classification="denied"
    policy_reason="symlink"
    return
  fi

  case "$candidate_path" in
    server/config.example.yaml|server/config.release.example.yaml)
      classification="exception"
      policy_reason="sanitized-config-template"
      return
      ;;
    desktop/build/entitlements.mac.plist)
      classification="exception"
      policy_reason="reviewed-packaging-entitlements"
      return
      ;;
    desktop/build/icons/icon.icns|desktop/build/icons/icon.iconset/icon_*.png)
      classification="exception"
      policy_reason="reviewed-desktop-icon-source"
      return
      ;;
    .github/ISSUE_TEMPLATE/config.yml)
      classification="exception"
      policy_reason="github-issue-template-config"
      return
      ;;
    .github/CODEOWNERS)
      classification="exception"
      policy_reason="github-code-ownership-policy"
      return
      ;;
    server/resource/ip2region/ip2region.xdb)
      classification="exception"
      policy_reason="required-ip2region-lookup-dataset"
      return
      ;;
    ProjectDocs/archive/*.md)
      classification="exception"
      policy_reason="text-only-design-history-not-binary-archive"
      return
      ;;
  esac

  wrapped_path="/$candidate_path/"
  case "$wrapped_path" in
    */node_modules/*|*/vendor/*|*/.next/*|*/dist/*|*/out/*|*/coverage/*|*/.cache/*|*/.gocache/*|*/.pytest_cache/*|*/.mypy_cache/*|*/__pycache__/*|*/cache/*|*/tmp/*|*/temp/*|*/uploads/*|*/logs/*|*/pids/*|*/output/*|*/target/*)
      classification="denied"
      policy_reason="dependency-cache-runtime-or-generated-tree"
      return
      ;;
    */release/*|*/releases/*|*/artifacts/*|*/archive/*|*/archives/*|*/build/*|*/bin/*|*/signing/*)
      classification="denied"
      policy_reason="build-release-archive-or-signing-tree"
      return
      ;;
  esac

  case "$candidate_path" in
    .env|.env.*|*/.env|*/.env.*|*.env|.envrc|*/.envrc|*/config.yaml|*/config.yml|*/config.json|*/config.toml|*/config-*.yaml|*/config-*.yml|*/config-*.json|*/config-*.toml|*/config.*.yaml|*/config.*.yml|*/config.*.json|*/config.*.toml|config.yaml|config.yml|config.json|config.toml|config-*.yaml|config-*.yml|config-*.json|config-*.toml|config.*.yaml|config.*.yml|config.*.json|config.*.toml|switchglm.sh|switchkimi.sh|*/switchglm.sh|*/switchkimi.sh)
      classification="denied"
      policy_reason="live-config-or-environment"
      return
      ;;
    .npmrc|*/.npmrc|.pypirc|*/.pypirc|.netrc|*/.netrc|*/credentials|*/credential|*/credentials.json|*/credentials.yaml|*/credentials.yml|*/credentials.toml|*/credentials.ini|*/credential.json|*/credential.yaml|*/credential.yml|*/credential.toml|*/credential.ini|*/secret.yaml|*/secret.yml|*/secret.json|*/secrets.yaml|*/secrets.yml|*/secrets.json|*/service-account*.json|*/google-services.json|*/GoogleService-Info.plist)
      classification="denied"
      policy_reason="credential-file"
      return
      ;;
    *.pem|*.key|*.p8|*.p12|*.pfx|*.jks|*.keystore|*.mobileprovision|*.cer|*.crt|id_rsa|*/id_rsa|id_ed25519|*/id_ed25519)
      classification="denied"
      policy_reason="private-key-or-signing-material"
      return
      ;;
    *.db|*.db-*|*.sqlite|*.sqlite-*|*.sqlite3|*.sqlite3-*|*.rdb|*.dump|*.bak|*.backup)
      classification="denied"
      policy_reason="database-runtime-or-backup"
      return
      ;;
    */dump.sql|*/backup.sql|*/backup-*.sql|*/export.sql|server/migrations/prompts.sql)
      classification="denied"
      policy_reason="database-or-seed-dump"
      return
      ;;
    *.zip|*.tar|*.tar.gz|*.tgz|*.gz|*.bz2|*.xz|*.7z|*.rar|*.dmg|*.pkg|*.deb|*.rpm|*.msi|*.blockmap)
      classification="denied"
      policy_reason="release-or-compressed-archive"
      return
      ;;
    *.exe|*.dll|*.dylib|*.so|*.a|*.o|*.wasm|*.bin|*.class|*.jar|*.war|*.pyc)
      classification="denied"
      policy_reason="compiled-binary"
      return
      ;;
    *.png|*.jpg|*.jpeg|*.gif|*.webp|*.avif|*.ico|*.icns|*.svg|*.svgz|*.mp3|*.wav|*.mp4|*.mov|*.avi|*.mkv|*.woff|*.woff2|*.ttf|*.otf|*.pdf|*.xdb|*.dat)
      classification="denied"
      policy_reason="unreviewed-binary-or-media"
      return
      ;;
    *.swp|*.swo|*~|.DS_Store|*/.DS_Store)
      classification="denied"
      policy_reason="editor-or-os-artifact"
      return
      ;;
  esac

  case "$candidate_path" in
    Makefile|README.md|LICENSE|NOTICE|AUTHORS|CONTRIBUTING.md|CODE_OF_CONDUCT.md|SECURITY.md|SUPPORT.md|GOVERNANCE.md|CHANGELOG.md|RELEASING.md|THIRD_PARTY_NOTICES.md)
      classification="allowed"
      policy_reason="root-engineering-or-governance-source"
      ;;
    ProjectDocs/*.md|ProjectDocs/*.txt)
      classification="allowed"
      policy_reason="documentation-source"
      ;;
    desktop/.gitignore|desktop/*.md|desktop/*.txt|desktop/*.ts|desktop/*.js|desktop/*.mjs|desktop/*.json|desktop/*.html|desktop/*.css|desktop/*.sh|desktop/*.yaml|desktop/*.yml|desktop/*.plist)
      classification="allowed"
      policy_reason="desktop-source"
      ;;
    scripts/*.sh|scripts/*.md|scripts/*.txt)
      classification="allowed"
      policy_reason="repository-tooling-source"
      ;;
    server/*.go|server/*.mod|server/*.sum|server/*.sql|server/*.sh|server/*.py|server/*.md|server/*.txt|server/*.json|server/*.yaml|server/*.yml|server/*.toml|server/*.tmpl|server/*.tpl|server/resource/template/*.html)
      classification="allowed"
      policy_reason="server-source"
      ;;
    .github/*.yaml|.github/*.yml|.github/*.md|.github/*.json)
      classification="allowed"
      policy_reason="repository-automation-source"
      ;;
    *)
      classification="unclassified"
      policy_reason="not-in-source-allowlist"
      ;;
  esac
}

assert_policy() {
  test_path="$1"
  expected_classification="$2"
  expected_reason="$3"

  classify_path "$test_path"
  if [ "$classification" = "$expected_classification" ] && [ "$policy_reason" = "$expected_reason" ]; then
    return
  fi

  printf 'source-baseline: self-test mismatch path=%q expected=%s/%s actual=%s/%s\n' \
    "$test_path" "$expected_classification" "$expected_reason" "$classification" "$policy_reason" >&2
  self_test_failures=$((self_test_failures + 1))
}

run_self_test() {
  self_test_failures=0
  self_test_cases=0

  assert_policy "server/internal/example/source.go" "allowed" "server-source"
  assert_policy "server/contracts/credential/v1/credential.go" "allowed" "server-source"
  assert_policy "server/service/auth/credentials.go" "allowed" "server-source"
  assert_policy "scripts/example-check.sh" "allowed" "repository-tooling-source"
  assert_policy "LICENSE" "allowed" "root-engineering-or-governance-source"
  assert_policy "CONTRIBUTING.md" "allowed" "root-engineering-or-governance-source"
  assert_policy "RELEASING.md" "allowed" "root-engineering-or-governance-source"
  assert_policy "THIRD_PARTY_NOTICES.md" "allowed" "root-engineering-or-governance-source"
  assert_policy ".github/ISSUE_TEMPLATE/bug_report.yml" "allowed" "repository-automation-source"
  assert_policy ".github/ISSUE_TEMPLATE/config.yml" "exception" "github-issue-template-config"
  assert_policy ".github/CODEOWNERS" "exception" "github-code-ownership-policy"
  assert_policy "server/config.example.yaml" "exception" "sanitized-config-template"
  assert_policy "desktop/build/icons/icon.iconset/icon_32x32.png" "exception" "reviewed-desktop-icon-source"
  assert_policy ".env" "denied" "live-config-or-environment"
  assert_policy "server/config.yaml" "denied" "live-config-or-environment"
  assert_policy "server/local/credential.json" "denied" "credential-file"
  assert_policy "server/cache/source.go" "denied" "dependency-cache-runtime-or-generated-tree"
  assert_policy "desktop/dist/application.js" "denied" "dependency-cache-runtime-or-generated-tree"
  assert_policy "server/runtime/workmax.db" "denied" "database-runtime-or-backup"
  assert_policy "desktop/signing/developer.p12" "denied" "build-release-archive-or-signing-tree"
  assert_policy "server/releases/workmax.tar.gz" "denied" "build-release-archive-or-signing-tree"
  assert_policy "server/assets/unreviewed-logo.png" "denied" "unreviewed-binary-or-media"
  assert_policy "server/source.unknown" "unclassified" "not-in-source-allowlist"
  assert_policy "desktop/electron/node_modules/electron/index.js" "denied" "dependency-cache-runtime-or-generated-tree"
  assert_policy "desktop/electron/.env.local" "denied" "live-config-or-environment"
  assert_policy "desktop/renderer/index.html.backup" "denied" "database-runtime-or-backup"
  assert_policy "desktop/renderer/demo.mp4" "denied" "unreviewed-binary-or-media"
  assert_policy "web/client.ts" "unclassified" "not-in-source-allowlist"
  assert_policy "admin/client.ts" "unclassified" "not-in-source-allowlist"
  self_test_cases=28

  if [ "$self_test_failures" -ne 0 ]; then
    echo "source-baseline: self-test failed cases=$self_test_cases failures=$self_test_failures" >&2
    return 1
  fi

  echo "source-baseline: self-test passed cases=$self_test_cases"
}

if [ "$self_test" -eq 1 ]; then
  if [ "$print_allowed_nul" -eq 1 ]; then
    echo "source-baseline: --self-test and --print-allowed0 cannot be combined" >&2
    exit 2
  fi
  run_self_test
  exit 0
fi

increment_top_level() {
  top_level="$1"
  index=0
  while [ "$index" -lt "${#top_level_names[@]}" ]; do
    if [ "${top_level_names[$index]}" = "$top_level" ]; then
      top_level_counts[$index]=$((top_level_counts[$index] + 1))
      return
    fi
    index=$((index + 1))
  done
  top_level_names+=("$top_level")
  top_level_counts+=(1)
}

print_human_path() {
  printf '  %q\n' "$1"
}

candidates=()
allowed_paths=()
exception_paths=()
exception_reasons=()
denied_paths=()
denied_reasons=()
unclassified_paths=()
top_level_names=()
top_level_counts=()

while IFS= read -r -d '' candidate; do
  if ! area_matches "$candidate"; then
    continue
  fi
  candidates+=("$candidate")
  case "$candidate" in
    */*) candidate_top_level="${candidate%%/*}" ;;
    *) candidate_top_level="[root]" ;;
  esac
  increment_top_level "$candidate_top_level"

  classify_path "$candidate"
  case "$classification" in
    allowed)
      allowed_paths+=("$candidate")
      ;;
    exception)
      exception_paths+=("$candidate")
      exception_reasons+=("$policy_reason")
      ;;
    denied)
      denied_paths+=("$candidate")
      denied_reasons+=("$policy_reason")
      ;;
    unclassified)
      unclassified_paths+=("$candidate")
      ;;
    *)
      echo "source-baseline: internal classification error for path:" >&2
      print_human_path "$candidate" >&2
      exit 2
      ;;
  esac
done < <(git ls-files --others --exclude-standard -z)

if [ "$print_allowed_nul" -eq 1 ]; then
  if [ "${#denied_paths[@]}" -ne 0 ] || [ "${#unclassified_paths[@]}" -ne 0 ]; then
    echo "source-baseline: refusing to emit an allowlist; audit is not clean" >&2
    index=0
    while [ "$index" -lt "${#denied_paths[@]}" ]; do
      printf '  [%s] ' "${denied_reasons[$index]}" >&2
      printf '%q\n' "${denied_paths[$index]}" >&2
      index=$((index + 1))
    done
    for candidate in "${unclassified_paths[@]}"; do
      print_human_path "$candidate" >&2
    done
    exit 1
  else
    # ${arr[@]+...} guards empty-array expansion under `set -u` on bash 3.2.
    for candidate in ${allowed_paths[@]+"${allowed_paths[@]}"} ${exception_paths[@]+"${exception_paths[@]}"}; do
      printf '%s\0' "$candidate"
    done
    echo "source-baseline: emitted ${#candidates[@]} approved paths for area=$area" >&2
    exit 0
  fi
fi

echo "source-baseline: candidate_count=${#candidates[@]} area=$area"
echo "source-baseline: top_level_counts"
index=0
while [ "$index" -lt "${#top_level_names[@]}" ]; do
  printf '  %s=%s\n' "${top_level_names[$index]}" "${top_level_counts[$index]}"
  index=$((index + 1))
done
echo "source-baseline: classification allowed=${#allowed_paths[@]} exception=${#exception_paths[@]} denied=${#denied_paths[@]} unclassified=${#unclassified_paths[@]}"

if [ "${#exception_paths[@]}" -ne 0 ]; then
  echo "source-baseline: reviewed_exceptions"
  index=0
  while [ "$index" -lt "${#exception_paths[@]}" ]; do
    printf '  [%s] ' "${exception_reasons[$index]}"
    printf '%q\n' "${exception_paths[$index]}"
    index=$((index + 1))
  done
fi

if [ "${#denied_paths[@]}" -ne 0 ]; then
  echo "source-baseline: denied_paths" >&2
  index=0
  while [ "$index" -lt "${#denied_paths[@]}" ]; do
    printf '  [%s] ' "${denied_reasons[$index]}" >&2
    printf '%q\n' "${denied_paths[$index]}" >&2
    index=$((index + 1))
  done
fi

if [ "${#unclassified_paths[@]}" -ne 0 ]; then
  echo "source-baseline: unclassified_paths" >&2
  for candidate in "${unclassified_paths[@]}"; do
    print_human_path "$candidate" >&2
  done
fi

if [ "${#denied_paths[@]}" -ne 0 ] || [ "${#unclassified_paths[@]}" -ne 0 ]; then
  echo "source-baseline: failed; no staging list was emitted" >&2
  echo "source-baseline: run make secret-audit separately; this audit never prints file contents" >&2
  exit 1
fi

echo "source-baseline: passed; run make secret-audit before any reviewed area staging"
