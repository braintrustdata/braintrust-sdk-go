#!/bin/bash

set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
NESTED_MODULES=()
while IFS= read -r module; do
    NESTED_MODULES+=("$module")
done < <("$SCRIPT_DIR/list_nested_modules.sh" --dependency-order)
MAX_PUSH_REFS=5

# Usage function
usage() {
    echo "Usage: ./scripts/release.sh <version> [--dry-run]"
}

local_tag_target_commit() {
    local tag="$1"
    git rev-parse -q --verify "refs/tags/${tag}^{}" 2>/dev/null || true
}

remote_tag_target_commit() {
    local tag="$1"
    git ls-remote --tags origin "refs/tags/${tag}^{}" | awk 'NR==1 {print $1}'
}

ensure_local_tag_at_head() {
    local tag="$1"
    local existing_commit

    existing_commit=$(local_tag_target_commit "$tag")
    if [[ -n "$existing_commit" ]]; then
        if [[ "$existing_commit" != "$COMMIT" ]]; then
            echo "Error: Local tag '$tag' already exists at $existing_commit (expected $COMMIT)" >&2
            exit 1
        fi
        return
    fi

    git tag -a "$tag" -m "Release $tag"
}

push_tags_in_batches() {
    local label="$1"
    shift
    local tags=("$@")
    local total=${#tags[@]}
    local index=0
    local batch_number=1

    while (( index < total )); do
        local batch=("${tags[@]:index:MAX_PUSH_REFS}")
        echo "Pushing ${label} batch ${batch_number}: ${batch[*]}"
        git push origin "${batch[@]}"
        index=$((index + ${#batch[@]}))
        batch_number=$((batch_number + 1))
    done
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

# Check remote tags
git fetch --tags > /dev/null 2>&1 || true

# Show release information
COMMIT=$(git rev-parse HEAD)
SHORT_COMMIT=$(git rev-parse --short HEAD)
REPO_URL=$(git config --get remote.origin.url | sed 's/git@github.com:/https:\/\/github.com\//' | sed 's/\.git$//')
LAST_TAG=$(git tag --sort=-version:refname | grep -v -- '-rc' | head -n 1 2>/dev/null || echo "")

PENDING_NESTED_TAGS=()
for module in "${NESTED_MODULES[@]}"; do
    module_tag="${module}/${VERSION}"

    local_commit=$(local_tag_target_commit "$module_tag")
    if [[ -n "$local_commit" && "$local_commit" != "$COMMIT" ]]; then
        echo "Error: Local tag '$module_tag' already exists at $local_commit (expected $COMMIT)" >&2
        exit 1
    fi

    remote_commit=$(remote_tag_target_commit "$module_tag")
    if [[ -n "$remote_commit" ]]; then
        if [[ "$remote_commit" != "$COMMIT" ]]; then
            echo "Error: Remote tag '$module_tag' already exists at $remote_commit (expected $COMMIT)" >&2
            exit 1
        fi
    else
        PENDING_NESTED_TAGS+=("$module_tag")
    fi
done

ROOT_TAG_PENDING=true
local_root_commit=$(local_tag_target_commit "$VERSION")
if [[ -n "$local_root_commit" && "$local_root_commit" != "$COMMIT" ]]; then
    echo "Error: Local tag '$VERSION' already exists at $local_root_commit (expected $COMMIT)" >&2
    exit 1
fi

remote_root_commit=$(remote_tag_target_commit "$VERSION")
if [[ -n "$remote_root_commit" ]]; then
    if [[ "$remote_root_commit" != "$COMMIT" ]]; then
        echo "Error: Remote tag '$VERSION' already exists at $remote_root_commit (expected $COMMIT)" >&2
        exit 1
    fi
    ROOT_TAG_PENDING=false
fi

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

if (( ${#PENDING_NESTED_TAGS[@]} == 0 )) && [[ "$ROOT_TAG_PENDING" != "true" ]]; then
    echo "All release tags already exist on origin for $VERSION."
    exit 0
fi

if [[ "$ROOT_TAG_PENDING" != "true" && ${#PENDING_NESTED_TAGS[@]} -gt 0 ]]; then
    echo "Warning: Root tag '$VERSION' already exists on origin."
    echo "Nested tags will be pushed without delaying the Release workflow."
    echo "If publish already ran before the nested tags existed, rerun the Release workflow manually afterward."
    echo ""
fi

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

ensure_local_tag_at_head "$VERSION"
for module in "${NESTED_MODULES[@]}"; do
    ensure_local_tag_at_head "${module}/${VERSION}"
done

# Push nested module tags first in batches small enough to satisfy repository
# rules. Push the root tag last so the Release workflow only starts after every
# nested module tag already exists on the remote.
if (( ${#PENDING_NESTED_TAGS[@]} > 0 )); then
    push_tags_in_batches "nested tags" "${PENDING_NESTED_TAGS[@]}"
fi

if [[ "$ROOT_TAG_PENDING" == "true" ]]; then
    echo "Pushing root tag: $VERSION"
    git push origin "$VERSION"
fi

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
