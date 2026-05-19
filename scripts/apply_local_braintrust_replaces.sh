#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd -- "$SCRIPT_DIR/.." && pwd)
ROOT_MODULE="github.com/braintrustdata/braintrust-sdk-go"

MODULE_DIRS=(".")
while IFS= read -r module; do
    MODULE_DIRS+=("$module")
done < <("$SCRIPT_DIR/list_nested_modules.sh")

for module_dir in "${MODULE_DIRS[@]}"; do
    gomod="${REPO_ROOT}/${module_dir}/go.mod"
    [[ -f "$gomod" ]] || continue

    if grep -q "${ROOT_MODULE} v" "$gomod"; then
        GOWORK=off go mod edit -replace="${ROOT_MODULE}=${REPO_ROOT}" "$gomod"
    fi

    for other in "${MODULE_DIRS[@]:1}"; do
        if grep -q "${ROOT_MODULE}/${other} v" "$gomod"; then
            GOWORK=off go mod edit -replace="${ROOT_MODULE}/${other}=${REPO_ROOT}/${other}" "$gomod"
        fi
    done
done
