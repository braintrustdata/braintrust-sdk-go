#!/bin/bash

set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
NESTED_MODULES=()
while IFS= read -r module; do
    NESTED_MODULES+=("$module")
done < <("$SCRIPT_DIR/list_nested_modules.sh" --dependency-order)

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

LOCAL_TAGS=$(git tag --list)
if echo "$LOCAL_TAGS" | grep -q "^$VERSION$"; then
    echo "Error: Version '$VERSION' already exists locally" >&2
    exit 1
fi

for module in "${NESTED_MODULES[@]}"; do
    module_tag="${module}/${VERSION}"
    if echo "$LOCAL_TAGS" | grep -q "^${module_tag}$"; then
        echo "Error: Module version '$module_tag' already exists locally" >&2
        exit 1
    fi
done

# Check remote tags
git fetch --tags > /dev/null 2>&1 || true
REMOTE_TAGS=$(git ls-remote --tags origin)
if echo "$REMOTE_TAGS" | grep -q "refs/tags/$VERSION$"; then
    echo "Error: Version '$VERSION' already exists on remote" >&2
    exit 1
fi

for module in "${NESTED_MODULES[@]}"; do
    module_tag="${module}/${VERSION}"
    if echo "$REMOTE_TAGS" | grep -q "refs/tags/${module_tag}$"; then
        echo "Error: Module version '$module_tag' already exists on remote" >&2
        exit 1
    fi
done

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
for module in "${NESTED_MODULES[@]}"; do
    printf "%-13s %s\n" "module tag:" "${module}/${VERSION}"
done
echo ""

# Confirmation prompt — skipped in dry-run and in GitHub Actions (non-interactive)
if [[ "$DRY_RUN" == true ]]; then
    exit 0
fi

if [[ "${GITHUB_ACTIONS:-}" != "true" ]]; then
    read -p "Are you ready to release version $VERSION? Type 'YOLO' to continue: " -r
    echo ""
    if [[ "$REPLY" != "YOLO" ]]; then
        exit 0
    fi
fi

TAGS_TO_PUSH=("$VERSION")
git tag -a "$VERSION" -m "Release $VERSION"
for module in "${NESTED_MODULES[@]}"; do
    module_tag="${module}/${VERSION}"
    git tag -a "$module_tag" -m "Release $module_tag"
    TAGS_TO_PUSH+=("$module_tag")
done

# Push all release tags atomically so the root-tag publish workflow only starts
# after every nested module tag exists on the remote.
git push --atomic origin "${TAGS_TO_PUSH[@]}"

echo "================================================"
echo " Tag Pushed: $VERSION"
echo "================================================"
echo
echo "The GitHub Actions workflow will now:"
echo "  1. Run goreleaser to create the GitHub release"
echo "  2. Index the package with the Go proxy"
echo
echo "Monitor the release workflow at:"
echo "  $REPO_URL/actions"
echo
echo "Once complete, check:"
echo "- Release: $REPO_URL/releases/tag/$VERSION"
echo "- Docs:    https://pkg.go.dev/github.com/braintrustdata/braintrust-sdk-go@$VERSION/braintrust"
echo "- Index:   https://proxy.golang.org/github.com/braintrustdata/braintrust-sdk-go/@v/$VERSION.info"
for module in "${NESTED_MODULES[@]}"; do
    echo "- Docs:    https://pkg.go.dev/github.com/braintrustdata/braintrust-sdk-go/${module}@${VERSION}"
    echo "- Index:   https://proxy.golang.org/github.com/braintrustdata/braintrust-sdk-go/${module}/@v/${VERSION}.info"
done
echo
echo "Note: Docs should be updated within the next hour. Request manually at the URL above"
echo "if they don't show up"
