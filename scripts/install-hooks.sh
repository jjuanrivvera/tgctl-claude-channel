#!/usr/bin/env bash
# Point git at the versioned hooks in .githooks so pre-commit runs fmt/vet/tests.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
git config core.hooksPath .githooks
echo "installed: git hooks at .githooks (core.hooksPath)"
