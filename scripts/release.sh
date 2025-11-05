#!/bin/bash

set -euo pipefail


# Usage function
usage() {
    echo "Usage: ./scripts/release.sh <version> [--dry-run]"
}

# Parse arguments
VERSION=""
DRY_RUN=false

while [[ $# -gt 0 ]]; do
    case $1 in
        --dry-run)
            DRY_RUN=true
            shift
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            if [[ -z "$VERSION" ]]; then
                VERSION="$1"
            else
                echo "Error: Unknown argument: $1" >&2
                usage
                exit 1
            fi
            shift
            ;;
    esac
done

if [[ -z "$VERSION" ]]; then
    echo "Error: Version is required" >&2
    usage
    exit 1
fi

# Validate version format (basic semver check)
if [[ ! "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.-]+)?$ ]]; then
    echo "Error: Version must follow semantic versioning format (e.g., v1.2.3 or v1.2.3-beta.1)" >&2
    exit 1
fi

if ! git diff-index --quiet HEAD --; then
    echo "Error: Working directory is not clean." >&2
    git status --porcelain
    exit 1
fi

if git tag --list | grep -q "^$VERSION$"; then
    echo "Error: Version '$VERSION' already exists locally" >&2
    exit 1
fi

# Check remote tags
git fetch --tags > /dev/null 2>&1 || true
if git ls-remote --tags origin | grep -q "refs/tags/$VERSION$"; then
    echo "Error: Version '$VERSION' already exists on remote" >&2
    exit 1
fi

# Show release information
COMMIT=$(git rev-parse HEAD)
SHORT_COMMIT=$(git rev-parse --short HEAD)
REPO_URL=$(git config --get remote.origin.url | sed 's/git@github.com:/https:\/\/github.com\//' | sed 's/\.git$//')
LAST_TAG=$(git tag --sort=-version:refname | grep -v -- '-rc' | head -n 1 2>/dev/null || echo "")

echo "================================================"
echo " Go SDK Release"
echo "================================================"
printf "%-13s %s\n" "version:" "$VERSION"
printf "%-13s %s\n" "commit:" "$SHORT_COMMIT"
printf "%-13s %s\n" "code:" "$REPO_URL/commit/$COMMIT"
if [[ -n "$LAST_TAG" ]]; then
    printf "%-13s %s\n" "changeset:" "$REPO_URL/compare/$LAST_TAG...$COMMIT"
else
    printf "%-13s %s\n" "changeset:" "$REPO_URL/commits/$COMMIT"
fi
echo ""

# Confirmation prompt (skip in dry-run)
if [[ "$DRY_RUN" == true ]]; then
    exit 0
fi

read -p "Are you ready to release version $VERSION? Type 'YOLO' to continue: " -r
echo ""
if [[ "$REPLY" != "YOLO" ]]; then
    exit 0
fi

if ! make ci; then
    echo "Error: make ci failed" >&2
    exit 1
fi

git tag -a "$VERSION" -m "Release $VERSION"
git push origin "$VERSION"

echo "Running goreleaser to update changelog..."
goreleaser release --clean

echo "Indexing package."
curl "https://proxy.golang.org/github.com/braintrustdata/braintrust-sdk-go/@v/$VERSION.info"
echo ""

echo "================================================"
echo " Release Complete $VERSION"
echo "================================================"
echo
echo "Note: Docs should be updated within the next hour. Request manually at the URL below"
echo "if they don't show up"
echo
echo "- Index:   https://proxy.golang.org/github.com/braintrustdata/braintrust-sdk-go/@v/$VERSION.info"
echo "- Docs:    https://pkg.go.dev/github.com/braintrustdata/braintrust-sdk-go@$VERSION/braintrust"
echo "- Release: $REPO_URL/releases/tag/$VERSION"
