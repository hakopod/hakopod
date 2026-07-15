# Local release preparation

The dedicated Linux installer is described in [installer/README.md](../installer/README.md). After the Go release build below, run `python3 release/build-installer.py` to build a separate local kit containing the actual dashboard runtime and installer inputs. Its output lives in `.local/installer-artifacts/<version>/`; no published download location is assumed. `python3 release/smoke-installer.py --arch arm64 --arch amd64` verifies the exact archives in disposable, memory-limited Linux containers. This verifies packaging/runtime, permissions and templates, not a full systemd/K3s host install. The original Go and dependency-lock SBOM scope below remains distinct.

Run from the repository root after the source and dashboard lockfile are stable:

```sh
pnpm --dir web install --frozen-lockfile
python3 release/build.py
```

This performs actual Go cross-compilation and scans the resulting binaries with checksum-verified Syft 1.51.1. It builds Linux amd64/arm64 bundles containing CLI and server, plus macOS amd64/arm64 CLI bundles. CGO is disabled. Compilation and scanning use two logical processors and a 256 MiB Go soft-memory target; targets are sequential. The script does not use Docker. It finishes by running `release/verify-archives.py`, which validates archive bytes, platforms, notice coverage, the native CLI and checksums and writes `verification.json`.

Outputs are under `.local/releases/<CLI-version>/`: platform `.tar.gz` bundles, preserved upstream notices, a path-sanitized license inventory, actual SPDX 2.3 and CycloneDX SBOMs, Syft's native catalog, source/tool provenance, and `SHA256SUMS`. Only generated output directories bearing the tooling marker can be replaced. Compilation uses an immutable, fingerprinted source snapshot so concurrent development cannot mix files halfway through a build. Provenance records whether the working tree changed afterwards; rebuild from the final source before publication.

The SBOM combines packages observed in Go binaries with packages declared by `web/pnpm-lock.yaml`, including development and optional dependencies. It is **not** an inventory of a finished container image, operating-system layers, or the tree-shaken production dashboard. Syft resolves JavaScript license metadata from the npm registry and Go license files from the local Go module cache. Its declarations and `NOASSERTION` fields are retained rather than invented. The separate notice archive preserves locally available upstream texts; see `docs/licenses.md` for coverage and remaining release obligations.

The archives normalize file order, owners, modes and timestamps (`SOURCE_DATE_EPOCH`, default zero), and Go uses `-trimpath -buildvcs=false`. SBOM machine-specific staging, cache and home paths are replaced with `$RELEASE_STAGE`, `$GOPATH/pkg/mod` and `$HOME`; package identities and license data are retained. SBOM generation timestamps and document identifiers may vary; byte-for-byte reproducibility of the entire output directory is not claimed. Cross-compilation does not establish runtime support on untested targets. None of these artifacts is published, signed, notarized, vulnerability-cleared, or a tested production installer.

Verify files before copying them elsewhere:

```sh
cd .local/releases/0.1.0-dev
shasum -a 256 -c SHA256SUMS
```

To build and verify the local API image after the Docker engine is healthy:

```sh
docker build --platform=linux/arm64 -t hakopod-api:dev .
python3 release/verify-image.py
```

Choose `linux/amd64` on an amd64 host. The Dockerfile bounds Go compilation to two processors with a 256 MiB soft-memory target and shares module/build caches between steps. The runtime image uses the pinned distroless base, a non-root user, a 192 MiB Go soft-memory target and two processors; the soft target is not a container memory limit. Root LICENSE and NOTICE accompany the image.

The verification command checks the actual local image ID, embedded OpenAPI bytes, root license files and native Linux CLI, then performs a no-network, read-only startup smoke under a 96 MiB container limit. That smoke deliberately omits the database and checks the configuration error; it does not establish full API/reconciler operation in a container. It adds `image-verification.json` and separate `hakopod-api.{spdx,cyclonedx,syft}.json` catalogs containing the image's Go dependencies and operating-system packages, then refreshes SHA256SUMS. Temporary verification containers are removed. The original combined Go/dashboard SBOM retains its distinct scope.

The earlier foundation arm64 image on 2026-09-12 was 32,075,846 bytes. Its server binary exactly matched the arm64 release executable. The image catalog identified 47 Go module records and five Debian package records; CycloneDX additionally lists filesystem components. This is one development build, not a runtime load measurement. See the current generated verification files for each build's immutable image ID and source fingerprint.

A public release still requires full runtime acceptance for the intended installation, vulnerability triage, license/source obligations for everything actually redistributed, artifact signatures, and a chosen publication destination. A local archive, startup smoke or generated SBOM is not evidence those gates passed.
