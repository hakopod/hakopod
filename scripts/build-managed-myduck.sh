#!/bin/sh
set -eu

SOURCE_SHA=6e3427591fd8895df9585969e7256f958fb639bb
UPSTREAM=https://github.com/apecloud/myduckserver.git
MODE=${1:-}
SCRATCH=${2:-}

if [ "$MODE" != source-only ] && [ "$MODE" != build ]; then
  echo "usage: $0 source-only|build NEW_EMPTY_SCRATCH" >&2
  exit 2
fi
if [ -z "$SCRATCH" ] || [ -e "$SCRATCH" ]; then
  echo "scratch path must not already exist" >&2
  exit 2
fi
case "$(uname -s):$(uname -m)" in Linux:x86_64) ;; *) echo "requires the approved Linux AMD64 VM" >&2; exit 2;; esac

mkdir -m 700 "$SCRATCH"
mkdir -m 700 "$SCRATCH/src" "$SCRATCH/gomodcache" "$SCRATCH/gocache" "$SCRATCH/tmp" "$SCRATCH/out"
git -C "$SCRATCH/src" init -q
git -C "$SCRATCH/src" remote add origin "$UPSTREAM"
git -C "$SCRATCH/src" fetch --depth=1 origin "$SOURCE_SHA"
git -C "$SCRATCH/src" checkout -q --detach FETCH_HEAD
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
python3 "$SCRIPT_DIR/apply-managed-myduck-patches.py" "$SCRATCH/src"

export GOMAXPROCS=2 GOFLAGS='-p=2 -tags=duckdb_arrow' GOMEMLIMIT=5GiB
export GOMODCACHE="$SCRATCH/gomodcache" GOCACHE="$SCRATCH/gocache" GOTMPDIR="$SCRATCH/tmp"
cd "$SCRATCH/src"
go test -run '^TestManagedLockdownBoundary$' ./catalog
go test -run '^$' ./pgserver .
if [ "$MODE" = source-only ]; then
  exit 0
fi
go build -trimpath -ldflags='-s -w' -o "$SCRATCH/out/myduckserver" .
test -x "$SCRATCH/out/myduckserver"
printf '%s\n' "$SCRATCH/out/myduckserver"
