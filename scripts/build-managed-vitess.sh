#!/usr/bin/env bash
set -euo pipefail

# Builds artifacts only. Installation, image publication and native acceptance
# remain separate operator actions. Run inside the approved Linux VM window.
if [ "$(uname -s)" != Linux ] || [ "$(uname -m)" != x86_64 ]; then
  echo "Managed Vitess builds require the approved linux/amd64 VM." >&2
  exit 1
fi
if [ "$#" -lt 1 ] || [ "$#" -gt 2 ] || [ -e "$1" ] || { [ "$#" -eq 2 ] && [ "$2" != --source-only ]; }; then
  echo "Usage: build-managed-vitess.sh <new-empty-scratch-directory> [--source-only]" >&2
  exit 1
fi
task_root=$(cd "$(dirname "$0")/.." && pwd)
mkdir -p "$1"
task_scratch=$(cd "$1" && pwd)

checkout() {
  git init -q "$task_scratch/$1"
  git -C "$task_scratch/$1" remote add origin "$2"
  git -C "$task_scratch/$1" fetch --depth=1 origin "$3"
  git -C "$task_scratch/$1" checkout --detach FETCH_HEAD
}
checkout vitess https://github.com/vitessio/vitess.git 0f1ed062dec171e0adfab796110549752901e299
checkout operator https://github.com/planetscale/vitess-operator.git 10a3b742c02c38f97d554739d5a257197daa48f9
python3 "$task_root/scripts/apply-managed-vitess-patches.py" --vitess-source "$task_scratch/vitess" --operator-source "$task_scratch/operator"

export GOMAXPROCS=1 GOFLAGS=-p=1 CGO_ENABLED=0
export GOCACHE="${GOCACHE:-$task_scratch/go-build}" GOTMPDIR="$task_scratch/go-tmp"
mkdir -p "$GOCACHE" "$GOTMPDIR" "$task_scratch/runtime/bin" "$task_scratch/controller/bin"
export HAKOPOD_VITESS_CANDIDATE_DIR="$task_scratch/candidate-specs"
cd "$task_root"
GOTOOLCHAIN=auto go test -count=1 ./internal/cluster -run '^TestVitessExportCandidateSchemaManifests$' -timeout=5m
export GOTOOLCHAIN=go1.25.13

cd "$task_scratch/vitess"
task_formatter="$(go env GOROOT)/bin/gofmt"
"$task_formatter" -w go/mysql/flavor_mysql.go go/mysql/flavor_mysql_test.go go/mysql/hakopod_replication_tls_test.go
"$task_formatter" -w go/vt/vttablet/tabletmanager/semisyncmonitor/monitor.go go/vt/vttablet/tabletmanager/semisyncmonitor/hakopod_account_test.go
go test -count=1 ./go/mysql -run 'TestHakopodReplicationVerifiesIdentity|Test.*(SetReplicationSource|CatchupToGTID)' -timeout=5m
go test -count=1 ./go/vt/vttablet/tabletmanager/semisyncmonitor -timeout=5m
git add --intent-to-add go/mysql/hakopod_replication_tls_test.go go/vt/vttablet/tabletmanager/semisyncmonitor/hakopod_account_test.go
git diff --check
git diff > "$task_scratch/runtime/upstream.patch"
cp "$task_root/Dockerfile.vitess-runtime" "$task_scratch/runtime/Dockerfile"

cd "$task_scratch/operator"
"$task_formatter" -w pkg/operator/vttablet/flags.go pkg/operator/vttablet/hakopod_hostname_test.go
"$task_formatter" -w pkg/operator/controllermanager/flags.go pkg/operator/controllermanager/hakopod_backup_flags_test.go pkg/controller/vitessshard/reconcile_backup_job.go pkg/controller/vitessbackupstorage/reconcile_subcontroller.go pkg/controller/vitessbackupschedule/vitessbackupschedule_controller.go pkg/controller/vitessbackupschedule/hakopod_backup.go pkg/controller/vitessbackupschedule/hakopod_backup_test.go pkg/controller/vitessbackupschedule/hakopod_api_test.go pkg/controller/vitessbackupschedule/hakopod_control_permissions_test.go pkg/operator/vttablet/vtbackup_pod.go pkg/operator/vttablet/hakopod_backup_test.go pkg/operator/vtgate/deployment.go pkg/operator/vtctld/deployment.go pkg/operator/vtorc/deployment.go
go test -count=1 ./pkg/operator/controllermanager ./pkg/operator/vttablet ./pkg/controller/vitessshard ./pkg/controller/vitessbackupschedule ./pkg/controller/vitessbackupstorage -timeout=5m
git add --intent-to-add pkg/operator/controllermanager/hakopod_backup_flags_test.go pkg/controller/vitessbackupschedule/hakopod_backup.go pkg/controller/vitessbackupschedule/hakopod_backup_test.go pkg/controller/vitessbackupschedule/hakopod_api_test.go pkg/controller/vitessbackupschedule/hakopod_control_permissions_test.go pkg/operator/vttablet/hakopod_backup_test.go pkg/operator/vttablet/hakopod_hostname_test.go
git diff --check
git diff > "$task_scratch/controller/upstream.patch"
cp "$task_root/Dockerfile.vitess-operator" "$task_scratch/controller/Dockerfile"
cp -R deploy/crds "$task_scratch/controller/crds"

if [ "${2:-}" = --source-only ]; then
  echo "Pinned upstream package tests passed. No runtime binaries or images were built."
  exit 0
fi

cd "$task_scratch/vitess"
for binary in vtctld vtctldclient vtgate vttablet vtorc vtbackup mysqlctld; do
  go build -trimpath -ldflags='-s -w -X vitess.io/vitess/go/vt/servenv.buildGitRev=0f1ed062dec171e0adfab796110549752901e299 -X vitess.io/vitess/go/vt/servenv.buildGitBranch=hakopod-replication-identity-v1' -o "$task_scratch/runtime/bin/$binary" "./go/cmd/$binary"
done
cd "$task_scratch/operator"
go build -trimpath -o "$task_scratch/controller/bin/vitess-operator" ./cmd/manager

cd "$task_scratch"
sha256sum runtime/bin/* controller/bin/* > binary-sha256.txt
echo "Built source-tested artifacts. Build the two image contexts, pin registry digests, then run native acceptance before enabling creation."
