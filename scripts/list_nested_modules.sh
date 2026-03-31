#!/bin/bash

set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
MANIFEST="$SCRIPT_DIR/nested_modules.txt"
REPO_ROOT=$(cd -- "$SCRIPT_DIR/.." && pwd)

if [[ "${1:-}" == "--dependency-order" ]]; then
    (
        cd "$REPO_ROOT"
        go run ./internal/nestedmodules/cmd
    )
    exit 0
fi

grep -v '^[[:space:]]*#' "$MANIFEST" | sed '/^[[:space:]]*$/d'
