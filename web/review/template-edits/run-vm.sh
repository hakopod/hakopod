#!/usr/bin/env bash
set -euo pipefail
[[ "$(uname -s)" == Linux ]] || { echo 'Run this review only on the designated development VM.' >&2; exit 1; }
case "$PWD" in /srv/hakopod-backup-scratch/binding-options-20261006/source) ;; *) echo 'Run from the reserved binding-options scratch checkout.' >&2; exit 1 ;; esac
export PATH="/srv/hakopod-backup-scratch/build-tools/node24/bin:$PATH"
export HAKOPOD_PLAYWRIGHT_MODULE="${HAKOPOD_PLAYWRIGHT_MODULE:-/srv/hakopod-backup-scratch/binding-options-20261006/browser-tools/node_modules/playwright/index.mjs}"
export PLAYWRIGHT_BROWSERS_PATH='/srv/hakopod-backup-scratch/binding-options-20261006/browsers'
export LD_LIBRARY_PATH="/srv/hakopod-backup-scratch/binding-options-20261006/browser-libs/root/usr/lib/x86_64-linux-gnu:/srv/hakopod-backup-scratch/database-cockpit-20260929/edge-browser-libs/root/usr/lib/x86_64-linux-gnu${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
export FONTCONFIG_FILE='/srv/hakopod-backup-scratch/binding-options-20261006/browser-libs/fonts.conf'
export FONTCONFIG_PATH='/srv/hakopod-backup-scratch/binding-options-20261006/browser-libs/root/etc/fonts'
mkdir -p /srv/hakopod-backup-scratch/binding-options-20261006/browser-libs/font-cache
cat > "$FONTCONFIG_FILE" <<'FONTCONFIG'
<?xml version="1.0"?>
<!DOCTYPE fontconfig SYSTEM "fonts.dtd">
<fontconfig>
  <dir>/srv/hakopod-backup-scratch/binding-options-20261006/browser-libs/root/usr/share/fonts</dir>
  <cachedir>/srv/hakopod-backup-scratch/binding-options-20261006/browser-libs/font-cache</cachedir>
</fontconfig>
FONTCONFIG
mkdir -p .local/template-edit-review
node --check web/review/template-edits/review.mjs
node web/review/template-edits/server.mjs > .local/template-edit-review/server.log 2>&1 &
server_pid=$!
trap 'kill "$server_pid" 2>/dev/null || true; wait "$server_pid" 2>/dev/null || true' EXIT
for attempt in {1..60}; do
  if curl --silent --fail "http://127.0.0.1:${HAKOPOD_TEMPLATE_REVIEW_PORT:-4198}/" > /dev/null; then break; fi
  kill -0 "$server_pid"
  sleep 1
done
node web/review/template-edits/review.mjs
