#!/bin/bash

set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd -- "$SCRIPT_DIR/.." && pwd)

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
