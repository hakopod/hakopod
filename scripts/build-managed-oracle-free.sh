#!/usr/bin/env bash
set -euo pipefail

# Build only on an approved Linux work VM. This does not publish an image,
# install a controller, accept database terms, or open the Oracle release gate.
if [ "$(uname -s)" != Linux ] || [ "$(uname -m)" != x86_64 ]; then
  echo "Oracle Free builds require the approved linux/amd64 VM." >&2
  exit 1
fi
if [ "$#" -ne 1 ] || [ -e "$1" ]; then
  echo "Usage: build-managed-oracle-free.sh <new-scratch-directory>" >&2
  exit 1
fi
task_root=$(cd "$(dirname "$0")/.." && pwd)
mkdir -p "$1"
task_scratch=$(cd "$1" && pwd)
git init -q "$task_scratch/source"
git -C "$task_scratch/source" remote add origin https://github.com/oracle/oracle-database-operator.git
git -C "$task_scratch/source" fetch --depth=1 origin ff6f9178c1650df30afbf203ebdb633e9b80760a
git -C "$task_scratch/source" checkout --detach FETCH_HEAD
python3 "$task_root/scripts/apply-managed-oracle-patches.py" "$task_scratch/source" --free-only

export GOMAXPROCS=2 GOFLAGS=-p=1 CGO_ENABLED=0 GOENV=off GOWORK=off
export GOCACHE="${GOCACHE:-$task_scratch/go-build}" GOTMPDIR="$task_scratch/go-tmp"
mkdir -p "$GOCACHE" "$GOTMPDIR" "$task_scratch/image/bin" "$task_scratch/image/crds"
export HAKOPOD_ORACLE_FREE_CANDIDATE_DIR="$task_scratch/candidate"
cd "$task_root"
go test -count=1 ./internal/cluster -run '^TestOracleFreeExportCandidate$' -timeout=5m
cd "$task_scratch/source"
python3 "$task_root/scripts/export-oracle-free-crd.py" config/crd/bases/database.oracle.com_singleinstancedatabases.yaml "$task_scratch/candidate/sidb.yaml"
task_formatter="$(go env GOROOT)/bin/gofmt"
"$task_formatter" -w main.go hakopod_free_health.go hakopod_free_health_test.go commons/database/hakopod_policy.go commons/database/hakopod_free_policy.go commons/database/hakopod_free_policy_test.go controllers/database/hakopod_free_resource.go controllers/database/hakopod_free_resource_test.go commons/database/utils.go commons/database/constants.go controllers/database/singleinstancedatabase_controller.go controllers/dataguard/dataguardbroker_topology_execution.go controllers/dataguard/dataguardbroker_reconcile_helpers.go
go test -count=1 ./commons/database -run '^TestHakopodOracleFree' -timeout=5m
go test -count=1 ./controllers/database -run '^TestHakopodOracleFree' -timeout=5m
go test -count=1 -run '^TestHakopodOracleFree' . -timeout=5m
go build -trimpath -ldflags='-s -w' -o "$task_scratch/image/bin/manager" .
git add --intent-to-add hakopod_free_health.go hakopod_free_health_test.go commons/database/hakopod_policy.go commons/database/hakopod_free_policy.go commons/database/hakopod_free_policy_test.go controllers/database/hakopod_free_resource.go controllers/database/hakopod_free_resource_test.go
git diff --check
git diff > "$task_scratch/image/upstream.patch"
cp "$task_root/Dockerfile.oracle-free-operator" "$task_scratch/image/Dockerfile"
cp LICENSE.txt "$task_scratch/image/LICENSE.txt"
python3 "$task_root/scripts/export-oracle-free-crd.py" config/crd/bases/database.oracle.com_singleinstancedatabases.yaml "$task_scratch/image/crds/singleinstancedatabases.yaml"
sha256sum "$task_scratch/image/bin/manager" "$task_scratch/image/upstream.patch" "$task_scratch/image/crds/singleinstancedatabases.yaml" > "$task_scratch/artifact-sha256.txt"
echo "Built the Free-only operator and v4 CRD. Publish and pin its digest before native acceptance."
