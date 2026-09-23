#!/bin/bash

# Dry-runs scripts/prepare_release.sh against a scratch copy of the repo using a
# version that is never tagged. This mirrors the Prepare Release workflow, where
# the release tags do not exist yet, and catches failures like a module that
# (directly or transitively) depends on an untagged nested Braintrust module
# without a local replace, e.g.:
#
#   reading .../trace/contrib/a2a/go.mod at revision trace/contrib/a2a/v0.15.0:
#   unknown revision trace/contrib/a2a/v0.15.0
#
# The working tree is never modified: tracked and untracked (non-ignored) files
# are copied to a temp directory and the release prep runs there.

set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd -- "$SCRIPT_DIR/.." && pwd)

# Higher than any real release so MVS always selects it, and a prerelease so it
# can never collide with a real tag.
CHECK_VERSION="v0.9999.0-prepare-release-check"

scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT

(
    cd "$REPO_ROOT"
    git ls-files -z --cached --others --exclude-standard \
        | while IFS= read -r -d '' f; do [[ -e "$f" ]] && printf '%s\0' "$f"; done \
        | tar --null -T - -cf -
) | tar -xf - -C "$scratch"

echo "Dry-running prepare_release.sh ${CHECK_VERSION} in ${scratch}"
"$scratch/scripts/prepare_release.sh" "$CHECK_VERSION" || {
    echo "Error: prepare_release.sh dry run failed; the Prepare Release workflow would too." >&2
    exit 1
}

echo "prepare_release.sh dry run succeeded."
