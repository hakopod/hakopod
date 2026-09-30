#!/usr/bin/env bash
set -euo pipefail

# Source tests and image packaging run in the approved development VM window.
# This helper does not install a controller or change a database.
if [ "$(uname -s)" != Linux ] || [ "$(uname -m)" != x86_64 ]; then
  echo "Managed MongoDB builds require the approved linux/amd64 VM."
  exit 1
fi
if [ "$#" -ne 1 ] || [ -e "$1" ]; then
  echo "Usage: build-managed-mongodb.sh <new-empty-scratch-directory>"
  exit 1
fi
task_root=$(cd "$(dirname "$0")/.." && pwd)
mkdir -p "$1"
task_scratch=$(cd "$1" && pwd)
task_revision=75fa89bca8c1395a1beb0f244723f255f67f8719

git init -q "$task_scratch/operator"
git -C "$task_scratch/operator" remote add origin https://github.com/mongodb/mongodb-kubernetes.git
git -C "$task_scratch/operator" fetch --depth=1 origin "$task_revision"
git -C "$task_scratch/operator" checkout --detach FETCH_HEAD
python3 "$task_root/scripts/apply-managed-mongodb-patches.py" --source "$task_scratch/operator"

export GOMAXPROCS=1 GOFLAGS=-p=1 CGO_ENABLED=0 GOTOOLCHAIN=local
export GOCACHE="${GOCACHE:-$task_scratch/go-cache}" GOTMPDIR="$task_scratch/go-tmp"
mkdir -p "$GOCACHE" "$GOTMPDIR" "$task_scratch/controller/bin"
cd "$task_scratch/operator"
task_formatter="$(go env GOROOT)/bin/gofmt"
"$task_formatter" -w mongodb-community-operator/controllers/replica_set_controller.go mongodb-community-operator/controllers/hakopod_tls_scaling_test.go
go test -count=1 ./mongodb-community-operator/controllers ./mongodb-community-operator/pkg/readiness/... ./pkg/util/scale -timeout=5m
git add --intent-to-add mongodb-community-operator/controllers/hakopod_tls_scaling_test.go
git diff --check
git diff > "$task_scratch/controller/upstream.patch"
go build -trimpath -buildvcs=false \
  -ldflags='-s -w -X github.com/mongodb/mongodb-kubernetes/pkg/util.OperatorVersion=1.13.0-hakopod.1 -X github.com/mongodb/mongodb-kubernetes/pkg/util.LogAutomationConfigDiff=false' \
  -o "$task_scratch/controller/bin/mongodb-kubernetes-operator" .
cp "$task_root/Dockerfile.mongodb-operator" "$task_scratch/controller/Dockerfile"
sha256sum "$task_scratch/controller/bin/mongodb-kubernetes-operator" "$task_scratch/controller/upstream.patch" > "$task_scratch/binary-sha256.txt"
echo "Source tests passed and patched binary built. Package the image and run native acceptance before changing the release pin."
