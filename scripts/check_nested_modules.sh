#!/bin/bash

set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd -- "$SCRIPT_DIR/.." && pwd)
ROOT_MODULE="github.com/braintrustdata/braintrust-sdk-go"

expected_file=$(mktemp)
actual_file=$(mktemp)
trap 'rm -f "$expected_file" "$actual_file"' EXIT

"$SCRIPT_DIR/list_nested_modules.sh" | sort > "$expected_file"

(
	cd "$REPO_ROOT"
	find trace/contrib -name go.mod -not -path 'trace/contrib/testdata/*' -print \
		| sed 's#/go.mod$##' \
		| sort
) > "$actual_file"

if ! diff -u "$expected_file" "$actual_file"; then
	echo ""
	echo "Nested module manifest is out of sync."
	echo "Update scripts/nested_modules.txt to match releasable nested modules under trace/contrib/."
	exit 1
fi

# Every nested module must have a committed `replace` directive for the root
# braintrust-sdk-go module pointing at the repo root. Without it, release
# preparation can introduce go.mod diffs at CI time when apply_local_braintrust_replaces.sh
# materializes the replace and breaks `git diff --exit-code` in mod-verify.
missing_replace=()
while IFS= read -r module; do
	gomod="${REPO_ROOT}/${module}/go.mod"
	# Match both single-line and block-form replace directives.
	if ! grep -q "^replace ${ROOT_MODULE} =>" "$gomod" && \
	   ! grep -q "^[[:space:]]*${ROOT_MODULE} =>" "$gomod"; then
		missing_replace+=("$module")
	fi
done < "$expected_file"

if (( ${#missing_replace[@]} > 0 )); then
	echo ""
	echo "The following nested modules are missing a 'replace ${ROOT_MODULE} => <repo-root>'"
	echo "directive in their go.mod. Add one so local development and release preparation work:"
	for module in "${missing_replace[@]}"; do
		echo "  - ${module}"
	done
	echo ""
	echo "See docs/PUBLISHING.md ('Adding a new nested module') for details."
	exit 1
fi
