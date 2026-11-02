#!/bin/sh
# Issue two single-job GitHub runner registrations for the scale-to-zero live
# acceptance and store them as a repository secret. Run this locally as a
# repository administrator; CI receives only these registrations, never your
# administration token. Each registration accepts one job and expires unused.
#
# Usage: scripts/actions/mint-scale-jit.sh <owner/repository> <runner-label> [secret-name]
set -eu
repository=${1:?owner/repository required}
label=${2:?runner label required}
secret=${3:-HAKOPOD_ACTIONS_SCALE_JIT}
case $label in
  *[!A-Za-z0-9_.-]*|'') echo 'label may contain letters, numbers, dots, underscores and hyphens' >&2; exit 2 ;;
esac
nonce=$(od -An -N6 -tx1 /dev/urandom | tr -d ' \n')
first=$(gh api -X POST "repos/$repository/actions/runners/generate-jitconfig" \
  -f name="hakopod-scale-$nonce-1" -F runner_group_id=1 -f "labels[]=$label" -f work_folder=_work)
second=$(gh api -X POST "repos/$repository/actions/runners/generate-jitconfig" \
  -f name="hakopod-scale-$nonce-2" -F runner_group_id=1 -f "labels[]=$label" -f work_folder=_work)
printf '[%s,%s]' "$first" "$second" | gh secret set "$secret" --repo "$repository"
echo "Stored two single-job registrations for label $label in $secret."
