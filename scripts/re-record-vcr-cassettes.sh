#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd -- "$SCRIPT_DIR/.." && pwd)

usage() {
    cat <<'EOF'
Usage: scripts/re-record-vcr-cassettes.sh

Re-record the root SDK, workflow-eval example, all nested integration modules,
and btx, then run the same suites in replay mode. Tests are never cached;
recording is serial and any failure stops the script.

Export BRAINTRUST_API_KEY, OPENAI_API_KEY, ANTHROPIC_API_KEY, GOOGLE_API_KEY,
GEMINI_API_KEY, AWS_ACCESS_KEY_ID, and AWS_SECRET_ACCESS_KEY before running.
Temporary AWS credentials also require AWS_SESSION_TOKEN. No credentials file
is sourced. Use test accounts: recording makes billable calls and mutates state.
Set OPENAI_NON_ZDR_API_KEY to also re-record stateful Responses API coverage;
without it, those tests replay their historical recordings.

Existing cassettes are not erased. Tests that explicitly force replay keep using
historical recordings. Review cassette diffs for sensitive data before committing.
EOF
}

if [[ $# -gt 0 ]]; then
    if [[ $# -eq 1 && ( "$1" == "--help" || "$1" == "-h" ) ]]; then
        usage
        exit 0
    fi
    usage >&2
    exit 2
fi

# Check all required providers before any test can overwrite a cassette.
# These checks establish presence, not credential validity or model access.
required_keys=(
    BRAINTRUST_API_KEY OPENAI_API_KEY ANTHROPIC_API_KEY
    GOOGLE_API_KEY GEMINI_API_KEY AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY
)
missing=0
for key in "${required_keys[@]}"; do
    if [[ -z "${!key:-}" ]]; then
        echo "Missing required environment variable: $key" >&2
        missing=1
    fi
done
if [[ "$missing" -ne 0 ]]; then
    echo "Export credentials before running; no cassettes were changed." >&2
    exit 1
fi

cd "$REPO_ROOT"
# Replay always targets production, so record against it too. A different
# deployment from the caller's environment would produce unreplayable cassettes.
export BRAINTRUST_API_URL=https://api.braintrust.dev
export BRAINTRUST_APP_URL=https://www.braintrust.dev
# Capture the manifest command separately so a failure cannot be hidden by
# process substitution or a later successful module test.
module_list=$("$SCRIPT_DIR/list_nested_modules.sh")
modules=()
while IFS= read -r module; do
    if [[ -n "$module" ]]; then
        modules+=("$module")
    fi
done <<< "$module_list"

run_suite() {
    local mode="$1"
    local module
    local flags=(-count=1 -failfast)
    if [[ "$mode" == "record" ]]; then
        flags+=(-p=1 -parallel=1)
    fi

    echo "--------- VCR $mode: root SDK ---------"
    VCR_MODE="$mode" go test "${flags[@]}" ./...
    echo "--------- VCR $mode: workflow-eval example ---------"
    VCR_MODE="$mode" go test "${flags[@]}" ./examples/internal/workflow-eval
    for module in "${modules[@]}" btx; do
        echo "--------- VCR $mode: $module ---------"
        VCR_MODE="$mode" go -C "$module" test "${flags[@]}" ./...
    done
}

echo "Recording makes real, billable API calls and creates/deletes test resources."
echo "Replay-only historical cassettes will be retained; existing files are not erased."
run_suite record

# Use a subshell so credential removal is confined to the replay phase.
(
    unset "${required_keys[@]}" AWS_SESSION_TOKEN GOOGLE_GENERATIVE_AI_API_KEY OPENAI_NON_ZDR_API_KEY
    run_suite replay
)

echo "--------- CASSETTE RE-RECORD AND REPLAY SUCCEEDED ---------"
echo "Review the cassette diffs, including response bodies, for sensitive data before committing."
