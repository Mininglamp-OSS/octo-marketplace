#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"

workflows='auto-add-to-project.yml
check-sprint.yml
dependency-review.yml
history-check.yml
labeler.yml
pr-title-lint.yml
secret-scan.yml
workflow-sanity.yml'

reusable_workflow_ref() {
  case "$1" in
    auto-add-to-project.yml) echo 'Mininglamp-OSS/.github/.github/workflows/auto-add-to-project.yml@v1' ;;
    check-sprint.yml) echo 'Mininglamp-OSS/.github/.github/workflows/reusable-check-sprint.yml@v1' ;;
    dependency-review.yml) echo 'Mininglamp-OSS/.github/.github/workflows/reusable-dependency-review.yml@v1' ;;
    history-check.yml) echo 'Mininglamp-OSS/.github/.github/workflows/reusable-history-check.yml@v1' ;;
    labeler.yml) echo 'Mininglamp-OSS/.github/.github/workflows/reusable-pr-labeler.yml@v1' ;;
    pr-title-lint.yml) echo 'Mininglamp-OSS/.github/.github/workflows/reusable-pr-title-lint.yml@v1' ;;
    secret-scan.yml) echo 'Mininglamp-OSS/.github/.github/workflows/reusable-secret-scan.yml@v1' ;;
    workflow-sanity.yml) echo 'Mininglamp-OSS/.github/.github/workflows/workflow-sanity.yml@v1' ;;
    *) return 1 ;;
  esac
}

for workflow in $workflows; do
  path=".github/workflows/$workflow"
  expected="uses: $(reusable_workflow_ref "$workflow")"

  if [[ ! -f "$path" ]]; then
    echo "missing required workflow: $path" >&2
    exit 1
  fi

  if ! grep -Fq "$expected" "$path"; then
    echo "unexpected reusable workflow reference in $path" >&2
    exit 1
  fi

  if grep -Eq '^[[:space:]]+run:' "$path"; then
    echo "reusable workflow caller must not execute local commands: $path" >&2
    exit 1
  fi
done

for workflow in auto-add-to-project.yml check-sprint.yml labeler.yml pr-title-lint.yml; do
  path=".github/workflows/$workflow"
  if ! grep -Fq 'pull_request_target:' "$path"; then
    echo "metadata-only workflow must use pull_request_target: $path" >&2
    exit 1
  fi
  if grep -Fq 'actions/checkout' "$path"; then
    echo "pull_request_target workflow must not check out PR code: $path" >&2
    exit 1
  fi
done

for workflow in auto-add-to-project.yml check-sprint.yml; do
  path=".github/workflows/$workflow"
  if ! grep -Fq 'PROJECT_TOKEN' "$path"; then
    echo "project workflow must pass PROJECT_TOKEN: $path" >&2
    exit 1
  fi
done

echo "community workflow contract passed"
