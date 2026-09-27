# @hakopod/cli-binary

Source for the npm distribution of the Go `hakopod` CLI (`cmd/hakopod/`),
using the optionalDependencies-per-platform pattern (as used by esbuild,
swc, etc.).

## Layout

- `bin/hakopod.mjs` — the wrapper's bin entry. Resolves the platform
  package for the current `process.platform`/`process.arch`, execs its
  binary with `stdio: 'inherit'`, and forwards the exit code. No
  postinstall/download step — npm's optionalDependencies resolution does
  the fetching.
- `wrapper-package.json` — template for the `@hakopod/cli` wrapper
  package.json (`__VERSION__` is substituted by the generator).
- `generate.mjs` — generator script. Produces seven publishable package
  directories: the wrapper (`cli/`) and six platform packages
  (`cli-<os>-<arch>/`), one per supported platform.

Platform packages (name / os / cpu):

| npm package                    | os      | cpu   |
|---------------------------------|---------|-------|
| `@hakopod/cli-darwin-arm64`      | darwin  | arm64 |
| `@hakopod/cli-darwin-x64`        | darwin  | x64   |
| `@hakopod/cli-linux-x64`         | linux   | x64   |
| `@hakopod/cli-linux-arm64`       | linux   | arm64 |
| `@hakopod/cli-win32-x64`         | win32   | x64   |
| `@hakopod/cli-win32-arm64`       | win32   | arm64 |

## Generator invocation

Input is a directory containing one extracted release archive per
platform, named `<system>_<arch>` (release-asset naming: `darwin`,
`linux`, `windows` / `amd64`, `arm64`), each holding the `hakopod`
binary (`hakopod.exe` on windows), `LICENSE`, and `NOTICE` — i.e. the
contents of `hakopod-cli_<version>_<system>_<arch>.tar.gz` extracted.

```sh
node packages/cli-binary/generate.mjs <version> <extracted-archives-dir> <out-dir>
```

Example:

```sh
node packages/cli-binary/generate.mjs 0.1.0-alpha.34 /tmp/hakopod-release-assets /tmp/hakopod-npm-out
```

This writes `<out-dir>/cli/` (the wrapper) and
`<out-dir>/cli-<os>-<arch>/` (the six platform packages), each a
complete, publishable package directory.

A release workflow calling this script is out of scope here; wiring it
in is reserved for whoever owns `.github/workflows/`.
