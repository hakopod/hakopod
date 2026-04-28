# Local release preparation

Run from the repository root after the source and dashboard lockfile are stable:

```sh
pnpm --dir web install --frozen-lockfile
python3 release/build.py
```

This performs actual Go cross-compilation and scans the resulting binaries with checksum-verified Syft 1.51.1. It builds Linux amd64/arm64 bundles containing CLI and server, plus macOS amd64/arm64 CLI bundles. CGO is disabled. Compilation and scanning use two logical processors and a 256 MiB Go soft-memory target; targets are sequential. The script does not use Docker.

Outputs are under `.local/releases/<CLI-version>/`: platform `.tar.gz` bundles, preserved upstream notices, a path-sanitized license inventory, actual SPDX 2.3 and CycloneDX SBOMs, Syft's native catalog, source/tool provenance, and `SHA256SUMS`. Only generated output directories bearing the tooling marker can be replaced. Compilation uses an immutable, fingerprinted source snapshot so concurrent development cannot mix files halfway through a build. Provenance records whether the working tree changed afterwards; rebuild from the final source before publication.

The SBOM combines packages observed in Go binaries with packages declared by `web/pnpm-lock.yaml`, including development and optional dependencies. It is **not** an inventory of a finished container image, operating-system layers, or the tree-shaken production dashboard. Syft resolves JavaScript license metadata from the npm registry and Go license files from the local Go module cache. Its declarations and `NOASSERTION` fields are retained rather than invented. The separate notice archive preserves locally available upstream texts; see `docs/licenses.md` for coverage and remaining release obligations.

The archives normalize file order, owners, modes and timestamps (`SOURCE_DATE_EPOCH`, default zero), and Go uses `-trimpath -buildvcs=false`. SBOM machine-specific staging, cache and home paths are replaced with `$RELEASE_STAGE`, `$GOPATH/pkg/mod` and `$HOME`; package identities and license data are retained. SBOM generation timestamps and document identifiers may vary; byte-for-byte reproducibility of the entire output directory is not claimed. Cross-compilation does not establish runtime support on untested targets. None of these artifacts is published, signed, notarized, vulnerability-cleared, or a tested production installer.

Verify files before copying them elsewhere:

```sh
cd .local/releases/0.1.0-dev
shasum -a 256 -c SHA256SUMS
```

A public release still requires the final runtime verification, Docker image rebuild and image SBOM, vulnerability triage, license/source obligations for everything actually redistributed, artifact signatures, and a chosen publication destination. A local archive is not evidence those gates passed.
