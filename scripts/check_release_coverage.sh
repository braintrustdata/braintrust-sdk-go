#!/bin/bash

# Verifies that every go.mod file in the repo that depends on the Braintrust
# SDK is covered by the release-prep flow. This catches the failure mode where
# a helper module is added that depends on braintrust-sdk-go but is not covered
# by the nested-module or example release-prep flows, which would cause
# prepare_release.sh to skip pinning it and break mod-verify at release time.
#
# A go.mod is "covered" if it matches one of:
#   - The root SDK module itself (./go.mod, handled explicitly by prepare_release.sh)
#   - A path listed in scripts/nested_modules.txt (published nested modules)
#   - A path under examples/ (handled by prepare_release.sh's find loop)
#   - A path under any */testdata/* directory (test fixtures, not real modules)

set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd -- "$SCRIPT_DIR/.." && pwd)
ROOT_MODULE="github.com/braintrustdata/braintrust-sdk-go"

cd "$REPO_ROOT"

# Build a newline-separated list of covered paths (repo-relative directories
# containing a go.mod that is already accounted for by the release flow). Using
# a plain string for portability with bash 3 on macOS.
COVERED=$'.\n'
while IFS= read -r module; do
    COVERED+="${module}"$'\n'
done < <("$SCRIPT_DIR/list_nested_modules.sh")
is_covered() {
    local dir="$1"
    printf '%s' "$COVERED" | grep -Fxq -- "$dir"
}

uncovered=()

# Iterate every go.mod in the repo (excluding .git and dist).
while IFS= read -r gomod; do
    dir="${gomod%/go.mod}"
    dir="${dir#./}"

    # Skip examples — handled by prepare_release.sh's find loop.
    [[ "$dir" == examples || "$dir" == examples/* ]] && continue

    # Skip testdata fixtures.
    [[ "$dir" == *"/testdata/"* || "$dir" == */testdata ]] && continue

    # Skip if covered by one of the manifests (or is the root SDK itself).
    if is_covered "$dir"; then
        continue
    fi

    # Does this go.mod depend on the Braintrust SDK? We match the root module
    # path followed by a space and a version-like token, which covers both
    # single-line requires and entries inside a require block.
    if grep -qE "${ROOT_MODULE}(/[a-zA-Z0-9_./-]+)? v[0-9]" "$gomod"; then
        uncovered+=("$dir")
    fi
done < <(find . -name go.mod -not -path './.git/*' -not -path './dist/*' -print | sort)

if (( ${#uncovered[@]} > 0 )); then
    echo "Error: the following go.mod files depend on ${ROOT_MODULE} but are not"
    echo "covered by the release-prep flow:"
    for dir in "${uncovered[@]}"; do
        echo "  - ${dir}/go.mod"
    done
    echo ""
    echo "Add each one to ONE of:"
    echo "  - scripts/nested_modules.txt (if it should be tagged and published)"
    echo "  - examples/                  (if it's a standalone example)"
    echo ""
    echo "Without coverage, prepare_release.sh will not pin its Braintrust deps,"
    echo "and mod-verify will fail during the release flow."
    exit 1
fi
