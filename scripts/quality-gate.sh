#!/usr/bin/env bash
# OpsCore quality gate — fast checks for pre-commit and local CI.
#
# Prevents the regressions we already shipped and had to fix:
#   * .gitignore masking cmd/opscore/ source (files silently never committed)
#   * broken UI config injection (trailing `{}` => entire <script> dead,
#     every button stopped responding)
#
# The UI-injection regression is now covered deterministically by
# internal/controlplane/server/render_test.go (no port/network dependency),
# so this gate only needs build + vet + test + the gitignore guard.
#
# Usage:
#   scripts/quality-gate.sh
# Enabled as a commit hook via: git config core.hooksPath .githooks
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
export GOTOOLCHAIN=local GOSUMDB=off

echo "==> [1/3] go build ./..."
if ! go build ./... ; then
  echo "FATAL: build failed"
  exit 1
fi

echo "==> [2/3] go vet ./..."
if ! go vet ./... ; then
  echo "FATAL: vet failed"
  exit 1
fi

echo "==> [3/3] go test -p 1 ./... (sequential; avoids cross-package CPU contention flaking timing-sensitive tests like controlplane/server TestExecutions_CancelFlow; UI-injection regression covered by server/render_test.go)"
if ! go test -p 1 ./... ; then
  echo "FATAL: go test failed"
  exit 1
fi

# Guard: CLI source must be tracked (the bare 'opscore' gitignore rule once
# masked the entire cmd/opscore/ directory).
if git check-ignore -q cmd/opscore/main.go; then
  echo "FATAL: cmd/opscore/main.go is gitignored — fix .gitignore (anchor to /opscore, not bare 'opscore')"
  exit 1
fi

echo "==> quality gate PASSED"
