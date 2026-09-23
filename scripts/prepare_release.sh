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

NESTED_MODULES=()
while IFS= read -r module; do
    NESTED_MODULES+=("$module")
done < <("$SCRIPT_DIR/list_nested_modules.sh")
BRAINTRUST_MODULES=("${ROOT_MODULE}" "${NESTED_MODULES[@]/#/${ROOT_MODULE}/}")

# Modules that depend on Braintrust SDK modules but are not themselves released
# via tags. We still pin their Braintrust dependencies so they stay in sync with
# the release. (e.g. btx is an internal helper module that is not published.)
# Sourced from scripts/pinned_unreleased_modules.txt so the new check in
# scripts/check_release_coverage.sh can validate the same set.
PINNED_UNRELEASED_MODULES=()
while IFS= read -r module; do
    [[ -z "$module" || "$module" =~ ^[[:space:]]*# ]] && continue
    PINNED_UNRELEASED_MODULES+=("$module")
done < "$SCRIPT_DIR/pinned_unreleased_modules.txt"

pin_braintrust_versions() {
    local module_dir="$1"
    local gomod="${module_dir}/go.mod"
    local module_path
    module_path=$(awk '$1 == "module" { print $2; exit }' "${gomod}")
    local -a edit_args=()
    local -a drop_args=()

    for braintrust_module in "${BRAINTRUST_MODULES[@]}"; do
        [[ "${braintrust_module}" == "${module_path}" ]] && continue

        # Match module path followed by a version (works for both single-line
        # and multi-line require blocks).
        if grep -q "${braintrust_module} v" "${gomod}"; then
            edit_args+=(-require="${braintrust_module}@${VERSION}")
        fi

        # Release PRs are opened before the new tags exist. Add temporary local
        # replacements so tidy can resolve Braintrust modules at the new
        # version, then drop only the replacements that this script added.
        # Pre-existing example replacements are kept because examples are
        # intended to run from checkout. Every Braintrust module is replaced,
        # not just the pinned ones, because an untagged module can be reached
        # transitively (e.g. via trace/contrib/all); tidy ignores unused ones.
        #
        # Match both single-line (`replace foo => bar`) and block-form
        # (`replace (\n    foo => bar\n)`) replace directives so we don't
        # append a duplicate when the module already has a committed local
        # replacement.
        if ! grep -q "^replace ${braintrust_module} =>" "${gomod}" && \
           ! grep -q "^[[:space:]]*${braintrust_module} =>" "${gomod}"; then
            edit_args+=(-replace="${braintrust_module}=${REPO_ROOT}${braintrust_module#${ROOT_MODULE}}")
            drop_args+=(-dropreplace="${braintrust_module}")
        fi
    done

    if (( ${#edit_args[@]} > 0 )); then
        GOWORK=off go mod edit "${edit_args[@]}" "${gomod}"
    fi

    GOWORK=off go mod tidy -C "${module_dir}"

    if (( ${#drop_args[@]} > 0 )); then
        GOWORK=off go mod edit "${drop_args[@]}" "${gomod}"
    fi
}

cd "$REPO_ROOT"

for module in "${NESTED_MODULES[@]}"; do
    pin_braintrust_versions "${module}"
done

# Pin Braintrust dependencies in unreleased helper modules (e.g. btx) so they
# track the release version. These modules are not tagged or published, but
# CI's mod-verify expects their go.mod files to be clean.
for module in "${PINNED_UNRELEASED_MODULES[@]}"; do
    pin_braintrust_versions "${module}"
done

# Examples are not published, but release PRs pin their Braintrust dependencies
# to the new release so standalone example modules stay current for users.
while IFS= read -r gomod; do
    pin_braintrust_versions "$(dirname "${gomod}")"
done < <(find examples -name go.mod -print | sort)
