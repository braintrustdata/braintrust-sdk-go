#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd -- "$SCRIPT_DIR/.." && pwd)
ROOT_MODULE="github.com/braintrustdata/braintrust-sdk-go"

# Default: only patch the SDK root + releasable nested modules. Pass
# --include-examples to also patch out-of-workspace example modules
# (used by `make build-examples` to verify examples build against
# current SDK source). We keep this off by default because
# scripts/publish.sh requires a clean working tree, and mutating
# example go.mod files would break the release path.
INCLUDE_EXAMPLES=false
if [[ "${1:-}" == "--include-examples" ]]; then
    INCLUDE_EXAMPLES=true
fi

MODULE_DIRS=(".")
while IFS= read -r module; do
    MODULE_DIRS+=("$module")
done < <("$SCRIPT_DIR/list_nested_modules.sh")

if [[ "$INCLUDE_EXAMPLES" == "true" ]]; then
    while IFS= read -r gomod; do
        abs_dir=$(dirname "$gomod")
        rel_dir=${abs_dir#"$REPO_ROOT/"}
        # Skip the in-workspace examples module (resolved via go.work).
        [[ "$rel_dir" == "examples" ]] && continue
        MODULE_DIRS+=("$rel_dir")
    done < <(find "$REPO_ROOT/examples" -name go.mod -print | sort)
fi

has_replace() {
    local module="$1"
    local gomod="$2"
    grep -q "^[[:space:]]*${module} =>" "$gomod" || grep -q "^replace ${module} =>" "$gomod"
}

for module_dir in "${MODULE_DIRS[@]}"; do
    gomod="${REPO_ROOT}/${module_dir}/go.mod"
    [[ -f "$gomod" ]] || continue

    if grep -q "${ROOT_MODULE} v" "$gomod" && ! has_replace "${ROOT_MODULE}" "$gomod"; then
        GOWORK=off go mod edit -replace="${ROOT_MODULE}=${REPO_ROOT}" "$gomod"
    fi

    for other in "${MODULE_DIRS[@]:1}"; do
        module="${ROOT_MODULE}/${other}"
        if grep -q "${module} v" "$gomod" && ! has_replace "${module}" "$gomod"; then
            GOWORK=off go mod edit -replace="${module}=${REPO_ROOT}/${other}" "$gomod"
        fi
    done
done
