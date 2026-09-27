Hakopod 0.1.0-alpha.35 makes the Go binary the CLI you install, builds it for Windows, and ships it the way a command-line tool is normally installed.

## Installing the CLI

`curl -fsSL https://hakopod.com/scripts/cli.sh | sh` downloads the binary for your platform, verifies it against the release checksums, and installs it to `/usr/local/bin` when that is writable and `~/.local/bin` otherwise. It refuses a download whose checksum does not match rather than installing it, and re-running it is safe. `HAKOPOD_VERSION` pins a version and `HAKOPOD_INSTALL_DIR` chooses the destination.

`npm i -g @hakopod/cli` installs the same binary. The package carries no JavaScript beyond a small launcher: the six platform builds are published as separate packages listed in `optionalDependencies`, each declaring the `os` and `cpu` it is for, so npm downloads exactly one of them and silently skips the rest. There is no postinstall download step, which means it works behind a proxy and pins in a lockfile like any other dependency. That matters most on Windows, where there is no `curl | sh` path, and in CI, where a `devDependencies` entry gives you the same version developers have.

This is a separate concern from `installer.sh`, which installs the server. Nothing about installing or upgrading a Hakopod host changes in this release.

## Windows

The CLI is built for Windows on amd64 and arm64 for the first time. Two things had to change for it to work rather than merely compile.

`readConfig` refused the credential file unless its mode was exactly `0600`. Go synthesizes a POSIX-style mode for Windows files that does not reflect the real ACLs, so that check rejected perfectly good credential files and left the CLI unusable straight after login. The check is now build-tagged: unchanged on Unix, and skipped on Windows rather than pretending a synthesized mode means something.

`internal/backup` guarded restores with `syscall.Statfs`, which does not exist on Windows, and the CLI pulls that package in transitively. It now uses `GetDiskFreeSpaceEx` on Windows through `golang.org/x/sys/windows`, which was already an indirect dependency. The arithmetic and both error messages on Unix are unchanged.

Windows ships the CLI only. There is no Windows server build and no Windows host bundle.

## The Node CLI is now @hakopod/cloud

Two different tools were both named `hakopod` and both installed by `npm i -g @hakopod/cli`. The Node CLI keeps all of its behaviour and moves to `@hakopod/cloud`, with the binary `hakopod-cloud`.

It is not deprecated. It remains the only way to take a Git repository through a build pipeline, which the Go CLI does not do. Its API surface, authentication flow and credential file path are unchanged, so an existing login keeps working and nobody has to sign in again.

Which one you want: a pre-built image or a TOML spec is `@hakopod/cli`; a Git repository that needs building is `@hakopod/cloud`.

## Release archives

Existing `hakopod_{version}_{system}_{arch}.tar.gz` bundles keep their exact names and contents, because `installer.sh` fetches them by name. New alongside them are `hakopod-cli_{version}_{system}_{arch}.tar.gz`, holding only the binary, `LICENSE` and `NOTICE`, for linux, darwin and windows on both architectures.

## Upgrade

```sh
sudo sh installer.sh --upgrade --version 0.1.0-alpha.35
```

Direct upgrades are supported from alpha.33 and alpha.34, the last two published versions. Older installations must upgrade through a supported intermediate release. This release adds no database migrations and changes no API or schema; the server is unchanged apart from the disk-space check being build-tagged, which is identical on Linux.

## Validation

Engine, API and dashboard suites pass, and the installer acceptance matrix passes fresh and upgrading from both supported sources on amd64 and arm64.

The release build was run end to end across all six platforms. Both Windows archives are produced and `file` reports `PE32+ executable (console) x86-64`. Archive verification now checks the CLI archives for what they actually are: exactly three members, and on linux and darwin the binary must be byte-identical to the same binary in the full bundle, which is what would catch the copy silently diverging from the one that was scanned.

The npm packages were generated from those real archives and the launcher was confirmed to execute the real binary and report the stamped version. The shell installer was run against the real release bytes, confirmed idempotent, and confirmed to refuse a tampered archive on checksum mismatch without installing anything.
