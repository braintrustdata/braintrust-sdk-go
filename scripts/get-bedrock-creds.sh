#!/usr/bin/env bash
# Refresh AWS credentials for the Bedrock test / example workflow.
#
# This script is designed to be runnable from a clean machine:
#   1. Checks that a named SSO profile exists; if not, walks the user through
#      `aws configure sso` once.
#   2. Runs `aws sso login` to refresh the cached SSO token (~8h lifetime).
#   3. Prints export lines for AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY /
#      AWS_SESSION_TOKEN / AWS_REGION.
#
# Usage:
#   scripts/get-bedrock-creds.sh              # print export lines
#   eval "$(scripts/get-bedrock-creds.sh)"    # apply to current shell
#   scripts/get-bedrock-creds.sh --env >> .env # append to your .env
#
# Override the profile by setting BEDROCK_AWS_PROFILE before running.

set -euo pipefail

PROFILE="${BEDROCK_AWS_PROFILE:-SdkPowerUser-820175832402}"
REGION_DEFAULT="us-east-2"

err() { printf 'error: %s\n' "$*" >&2; }
note() { printf '%s\n' "$*" >&2; }

command -v aws >/dev/null || {
  err "aws CLI not found. Install from https://aws.amazon.com/cli/ and retry."
  exit 1
}

# 1. Ensure the profile exists. If not, run one-time SSO setup.
if ! aws configure list-profiles 2>/dev/null | grep -qx "$PROFILE"; then
  note "SSO profile '$PROFILE' not found — launching 'aws configure sso'."
  note "Use 'sdk' as the session name and the braintrustdata start URL when prompted."
  aws configure sso
  # The wizard may create a profile with a default name; bail if it still
  # isn't there so the user can re-run with BEDROCK_AWS_PROFILE=<name>.
  if ! aws configure list-profiles | grep -qx "$PROFILE"; then
    err "SSO setup finished but profile '$PROFILE' still missing."
    err "Re-run with BEDROCK_AWS_PROFILE=<the profile you just created>."
    exit 1
  fi
fi

# 2. Refresh the SSO token. No-op if still valid.
note "Logging in to SSO profile '$PROFILE'…"
aws sso login --profile "$PROFILE" >&2

# 3. Print exportable creds. The --format env output is shell-compatible.
note "Exporting credentials (eval this or append to .env):"
aws configure export-credentials --profile "$PROFILE" --format env
# Add AWS_REGION explicitly — export-credentials doesn't emit it.
printf 'export AWS_REGION=%s\n' "$(aws configure get region --profile "$PROFILE" 2>/dev/null || printf '%s' "$REGION_DEFAULT")"

note ""
note "To apply these to your current shell, re-run as:"
note "  eval \"\$($0)\""
