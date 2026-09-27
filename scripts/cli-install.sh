#!/bin/sh
# Download and install the hakopod CLI binary from GitHub releases.
# Usage: curl -fsSL https://hakopod.com/scripts/cli.sh | sh
set -eu

HAKOPOD_BASE_URL=${HAKOPOD_BASE_URL:-https://github.com/hakopod/hakopod/releases/download}
HAKOPOD_API_URL=${HAKOPOD_API_URL:-https://api.github.com/repos/hakopod/hakopod/releases}
HAKOPOD_VERSION=${HAKOPOD_VERSION:-}
HAKOPOD_INSTALL_DIR=${HAKOPOD_INSTALL_DIR:-}

cli_log() { printf '%s\n' "$*" >&2; }
cli_die() { cli_log "error: $*"; exit 1; }

cli_fetch() {
  # cli_fetch URL OUT
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$1" -o "$2"
  elif command -v wget >/dev/null 2>&1; then
    wget -q "$1" -O "$2"
  else
    cli_die "curl or wget is required"
  fi
}

cli_fetch_stdout() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$1"
  elif command -v wget >/dev/null 2>&1; then
    wget -q -O - "$1"
  else
    cli_die "curl or wget is required"
  fi
}

cli_tmp=$(mktemp -d 2>/dev/null || mktemp -d -t hakopod-cli)
trap 'rm -rf "$cli_tmp"' EXIT INT TERM

cli_detect_system() {
  case "$(uname -s)" in
    Linux) printf 'linux\n' ;;
    Darwin) printf 'darwin\n' ;;
    *) return 1 ;;
  esac
}

cli_detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) printf 'amd64\n' ;;
    aarch64|arm64) printf 'arm64\n' ;;
    *) return 1 ;;
  esac
}

cli_system=$(cli_detect_system) || {
  cli_die "unsupported platform '$(uname -s)'. Windows and other platforms are not supported by this installer; run: npm i -g @hakopod/cli"
}
cli_arch=$(cli_detect_arch) || {
  cli_die "unsupported architecture '$(uname -m)'. Run: npm i -g @hakopod/cli"
}

if [ -n "${HAKOPOD_PRINT_TARGET:-}" ]; then
  printf '%s_%s\n' "$cli_system" "$cli_arch"
  exit 0
fi

if [ -z "$HAKOPOD_VERSION" ]; then
  cli_log "Resolving latest hakopod release..."
  # No draft filter needed: the releases API only returns drafts to callers with
  # push access, and this script always runs unauthenticated. /releases/latest is
  # not usable instead — it skips prereleases, and every release so far is one.
  cli_index="$cli_tmp/releases.json"
  cli_fetch "$HAKOPOD_API_URL?per_page=1" "$cli_index"
  HAKOPOD_VERSION=$(sed -n 's/.*"tag_name": *"v\{0,1\}\([^"]*\)".*/\1/p' "$cli_index" | head -n1)
  [ -n "$HAKOPOD_VERSION" ] || cli_die "could not resolve the latest release; set HAKOPOD_VERSION explicitly"
fi
cli_log "Installing hakopod $HAKOPOD_VERSION ($cli_system/$cli_arch)"

cli_archive="hakopod-cli_${HAKOPOD_VERSION}_${cli_system}_${cli_arch}.tar.gz"
cli_base="$HAKOPOD_BASE_URL/v$HAKOPOD_VERSION"

cli_fetch "$cli_base/$cli_archive" "$cli_tmp/$cli_archive"

if cli_fetch "$cli_base/SHA256SUMS" "$cli_tmp/SHA256SUMS" 2>/dev/null; then
  cli_expected=$(grep " $cli_archive\$" "$cli_tmp/SHA256SUMS" | cut -d' ' -f1)
  [ -n "$cli_expected" ] || cli_die "SHA256SUMS has no entry for $cli_archive"
  if command -v sha256sum >/dev/null 2>&1; then
    cli_actual=$(sha256sum "$cli_tmp/$cli_archive" | cut -d' ' -f1)
  elif command -v shasum >/dev/null 2>&1; then
    cli_actual=$(shasum -a 256 "$cli_tmp/$cli_archive" | cut -d' ' -f1)
  else
    cli_die "sha256sum or shasum is required to verify the download"
  fi
  [ "$cli_actual" = "$cli_expected" ] || cli_die "checksum mismatch for $cli_archive"
else
  cli_log "warning: no SHA256SUMS published for this release; installing WITHOUT checksum verification"
fi

mkdir -p "$cli_tmp/extract"
# Release archives wrap their contents in one top-level directory named after
# the archive, so strip it rather than guessing that directory's name here.
tar -xzf "$cli_tmp/$cli_archive" -C "$cli_tmp/extract" --strip-components=1
[ -f "$cli_tmp/extract/hakopod" ] || cli_die "archive did not contain a hakopod binary"

if [ -z "$HAKOPOD_INSTALL_DIR" ]; then
  if [ -w /usr/local/bin ] 2>/dev/null; then
    HAKOPOD_INSTALL_DIR=/usr/local/bin
  else
    HAKOPOD_INSTALL_DIR="$HOME/.local/bin"
  fi
fi
mkdir -p "$HAKOPOD_INSTALL_DIR"

chmod +x "$cli_tmp/extract/hakopod"
mv -f "$cli_tmp/extract/hakopod" "$HAKOPOD_INSTALL_DIR/hakopod"

case ":$PATH:" in
  *":$HAKOPOD_INSTALL_DIR:"*) ;;
  *) cli_log "Note: $HAKOPOD_INSTALL_DIR is not on your PATH. Add it with:"
     cli_log "  export PATH=\"$HAKOPOD_INSTALL_DIR:\$PATH\"" ;;
esac

cli_log "Installed hakopod to $HAKOPOD_INSTALL_DIR/hakopod"
"$HAKOPOD_INSTALL_DIR/hakopod" version
