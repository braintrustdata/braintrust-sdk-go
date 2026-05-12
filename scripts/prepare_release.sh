#!/bin/bash

set -euo pipefail

usage() {
    echo "Usage: ./scripts/prepare_release.sh <version>" >&2
}

VERSION="${1:-}"
if [[ -z "$VERSION" ]]; then
    usage
    exit 1
fi

if [[ ! "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.-]+)?$ ]]; then
    echo "Error: version must be semver (e.g. v1.2.3 or v1.2.3-beta.1)" >&2
    exit 1
fi

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd -- "$SCRIPT_DIR/.." && pwd)
ROOT_MODULE="github.com/braintrustdata/braintrust-sdk-go"

mapfile -t NESTED_MODULES < <("$SCRIPT_DIR/list_nested_modules.sh")

pin_braintrust_versions() {
    local module_dir="$1"
    local gomod="${module_dir}/go.mod"

    # Match module path followed by a version (works for both single-line and
    # multi-line require blocks).
    if grep -q "${ROOT_MODULE} v" "${gomod}"; then
        GOWORK=off go mod edit -require="${ROOT_MODULE}@${VERSION}" "${gomod}"
    fi

    for other in "${NESTED_MODULES[@]}"; do
        if grep -q "${ROOT_MODULE}/${other} v" "${gomod}"; then
            GOWORK=off go mod edit -require="${ROOT_MODULE}/${other}@${VERSION}" "${gomod}"
        fi
    done

    GOWORK=off go mod tidy -C "${module_dir}"
}

cd "$REPO_ROOT"

for module in "${NESTED_MODULES[@]}"; do
    pin_braintrust_versions "${module}"
done

# Examples are not published, but release PRs pin their Braintrust dependencies
# to the new release so standalone example modules stay current for users.
while IFS= read -r gomod; do
    pin_braintrust_versions "$(dirname "${gomod}")"
done < <(find examples -name go.mod -print | sort)
