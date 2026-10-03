#!/bin/sh
set -eu
umask 077

: "${HAKOPOD_OWNERSHIP_SOURCE:?set the exact source checkout}"
: "${HAKOPOD_OWNERSHIP_EVIDENCE:?set a fresh protected evidence output}"
: "${HAKOPOD_TEST_KUBECONFIG:?set the dedicated development kubeconfig}"
[ ! -e "$HAKOPOD_OWNERSHIP_EVIDENCE" ] && [ ! -L "$HAKOPOD_OWNERSHIP_EVIDENCE" ] || { echo 'ownership evidence output already exists' >&2; exit 2; }
[ -d "$HAKOPOD_OWNERSHIP_SOURCE/internal/cluster" ] && [ ! -L "$HAKOPOD_OWNERSHIP_SOURCE" ] || { echo 'ownership source is unavailable' >&2; exit 2; }
[ -f "$HAKOPOD_TEST_KUBECONFIG" ] && [ ! -L "$HAKOPOD_TEST_KUBECONFIG" ] || { echo 'ownership kubeconfig is unavailable' >&2; exit 2; }
for command in go timeout; do command -v "$command" >/dev/null 2>&1 || { echo "ownership check requires $command" >&2; exit 2; }; done

raw=$(mktemp /tmp/hakopod-supabase-ownership.XXXXXX)
result=$(mktemp "$(dirname "$HAKOPOD_OWNERSHIP_EVIDENCE")/.ownership-result.XXXXXX")
binary=$(mktemp /tmp/hakopod-supabase-ownership-test.XXXXXX)
compile_log=$(mktemp "$(dirname "$HAKOPOD_OWNERSHIP_EVIDENCE")/.ownership-build.XXXXXX")
trap 'rm -f "$raw" "$result" "$binary" "$compile_log"' EXIT HUP INT TERM
chmod 600 "$compile_log"
(
  cd "$HAKOPOD_OWNERSHIP_SOURCE"
  GOMAXPROCS=2 timeout 600s go test -c -o "$binary" ./internal/cluster
) >"$compile_log" 2>&1 || {
  [ "$(wc -c <"$compile_log")" -le 8388608 ] || { echo 'Supabase hostile ownership build output exceeded its limit' >&2; exit 1; }
  category=compile-failed
  grep -qi 'timed out\|signal: killed' "$compile_log" && category=compile-timeout
  grep -qi 'no space left on device\|file too large' "$compile_log" && category=compile-storage-limit
  echo "Supabase hostile ownership test build failed ($category); raw output was withheld" >&2
  exit 1
}
(
  ulimit -f 4096
  cd "$HAKOPOD_OWNERSHIP_SOURCE"
  HAKOPOD_SUPABASE_TEST=1 GOMAXPROCS=2 timeout 180s go tool test2json -t -p github.com/hakopod/hakopod/internal/cluster "$binary" -test.v -test.run '^TestSupabaseLiveRefusesUnclaimedNamespace$' -test.count=1 >"$raw" 2>&1
) || { echo 'Supabase hostile ownership test failed; raw output was withheld' >&2; exit 1; }
[ "$(wc -c <"$raw")" -le 2097152 ] || { echo 'Supabase hostile ownership output exceeded its limit' >&2; exit 1; }
python3 - "$raw" "$result" <<'PY'
import hashlib,json,pathlib,sys
source=pathlib.Path(sys.argv[1]); target=pathlib.Path(sys.argv[2]); seen_run=seen_pass=package_pass=False
for line in source.read_text().splitlines():
    value=json.loads(line,object_pairs_hook=lambda pairs: dict(pairs) if len(dict(pairs))==len(pairs) else (_ for _ in ()).throw(ValueError('duplicate key')))
    if value.get('Action')=='fail': raise SystemExit(1)
    if value.get('Test')=='TestSupabaseLiveRefusesUnclaimedNamespace':
        seen_run |= value.get('Action')=='run'; seen_pass |= value.get('Action')=='pass'
    if value.get('Action')=='pass' and value.get('Package')=='github.com/hakopod/hakopod/internal/cluster' and 'Test' not in value: package_pass=True
if not (seen_run and seen_pass and package_pass): raise SystemExit(1)
target.write_text(json.dumps({'schema_version':1,'case':'ownership-fencing','test':'TestSupabaseLiveRefusesUnclaimedNamespace','status':'passed','namespace_cleanup_verified':True,'go_test_json_sha256':hashlib.sha256(source.read_bytes()).hexdigest()},sort_keys=True,separators=(',',':'))+'\n')
PY
chmod 600 "$result"
ln "$result" "$HAKOPOD_OWNERSHIP_EVIDENCE"
rm -f "$result" "$raw" "$binary" "$compile_log"
trap - EXIT HUP INT TERM
