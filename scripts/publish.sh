#!/bin/bash

set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
NESTED_MODULES=()
while IFS= read -r module; do
    NESTED_MODULES+=("$module")
done < <("$SCRIPT_DIR/list_nested_modules.sh")

# Check if working directory is clean
if ! git diff-index --quiet HEAD --; then
    echo "Error: Working directory is not clean." >&2
    git status --porcelain
    exit 1
fi

# Check if we're on the intended release tag. When multiple tags point at the
# same commit, prefer the explicit root tag passed by CI.
VERSION="${RELEASE_TAG:-}"
if [ -z "$VERSION" ]; then
    VERSION=$(git describe --tags --exact-match 2>/dev/null || echo "")
fi
if [ -z "$VERSION" ]; then
    echo "Error: Not on a tagged commit. Please create and push a tag first." >&2
    exit 1
fi

echo "Releasing version $VERSION..."

# Run goreleaser
goreleaser release --clean

# Get repository URL
REPO_URL=$(git config --get remote.origin.url | sed 's/git@github.com:/https:\/\/github.com\//' | sed 's/\.git$//')

# Show completion information
echo "RELEASE COMPLETE: $VERSION"
echo ""
echo "Note: Docs should be updated within the next hour. Request manually at the URL below"
echo "if they don't show up"
echo "- Release: $REPO_URL/releases/tag/$VERSION"
echo "- Docs:    https://pkg.go.dev/github.com/braintrustdata/braintrust-sdk-go@$VERSION"
echo "- Index:   https://proxy.golang.org/github.com/braintrustdata/braintrust-sdk-go/@v/$VERSION.info"
for module in "${NESTED_MODULES[@]}"; do
    echo "- Docs:    https://pkg.go.dev/github.com/braintrustdata/braintrust-sdk-go/${module}@${VERSION}"
    echo "- Index:   https://proxy.golang.org/github.com/braintrustdata/braintrust-sdk-go/${module}/@v/$VERSION.info"
done
