#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

if rg -n --glob '!**/*_test.go' 'docs/plans/active/completed' cmd pkg; then
  echo "found forbidden completed path reference in source tree" >&2
  exit 1
fi

if rg -n --glob '!**/*_test.go' 'filepath\.Join\(filepath\.Dir\(planFile\), "completed"' cmd pkg; then
  echo "found legacy sibling completed path resolver in source tree" >&2
  exit 1
fi

rg -n 'CompletedPlanPath' pkg/git/service.go cmd/ralphex/main.go >/dev/null

go test ./pkg/git ./cmd/ralphex

echo "invariants verified"
