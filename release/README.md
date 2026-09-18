# Release preparation

`.github/workflows/release.yml` builds tags such as `v0.1.0-alpha.2` from commits
already merged into `main`. It cross-compiles the Linux server/CLI and macOS CLI,
builds the dashboard from source, collects notices and SBOMs, and verifies the
archives. Separate native Linux amd64 and arm64 jobs run the same immutable
artifacts in disposable 512 MiB containers. Both must pass before publication. The same packaged smoke jobs run on installer pull requests, alongside the full native systemd/K3s host matrix.

The publish job binds those smoke reports to the archive hashes, generates
GitHub build-provenance attestations using OIDC, and attaches the assets to a
draft release before publishing it. Prerelease tags are marked prerelease and
do not replace GitHub's latest stable release. All action references are pinned
to commits. No private submodule or private token is needed; public UI sources
come from the checked-in verified bundle.

The first published prerelease is `v0.1.0-alpha.2`. The earlier
`v0.1.0-alpha.1` tag remains immutable; its release was blocked by a root-run
test-fixture failure before publication. Future tags require reviewed, merged
source and passing candidate smoke and host checks.
The [verification record](../docs/prebuilt-installation-verification.md) links
the published release, exact source and website bootstrap verification.
Hosting `hakopod.com/scripts/installer.sh` remains a separate website deployment;
future releases do not change that endpoint automatically.

Before creating a tag, add its version and supported source versions to
`release/upgrade-paths.json` and set the installer acceptance workflow's default
candidate to that version. Run the same preflight used by publication:

```sh
python3 release/publication.py version v0.1.0-alpha.13
python3 release/upgrade-paths.py --version 0.1.0-alpha.13 --require-policy
```

Merge only after the candidate's native smoke and host matrix pass. Each declared
source is tested across both managed-database architectures and the amd64 local
and external PostgreSQL modes. Create the tag from that merged source; let the
workflow create and publish the release after its artifact checks. Do not create
an empty published release while the build is pending.

`v0.1.0-alpha.11` was tagged without an upgrade policy and produced no release
assets. Its tag remains unchanged. The corrected candidate is `v0.1.0-alpha.12`,
which declares upgrades from alpha.8, alpha.9 and alpha.10. An empty alpha.11
release is not an installable source version.

Release assets include `installer.sh`, the installer kit, the actual dashboard
runtime, all four platform archives, `SHA256SUMS`, SPDX/CycloneDX/Syft inventories,
license notices, build records, native smoke reports and an attestation bundle.
The bootstrap downloads only the kit, dashboard and selected Linux architecture.
Go linker flags inject the exact version into `hakopod version`; development
builds retain `0.1.0-dev`. Public builds explicitly use `hakopod_selfhosted`,
overriding ambient cloud build tags. Provenance records the product capability,
and archive verification checks the actual Go build metadata of every binary.
Public artifacts cannot enable open signup through runtime environment or TOML
settings. Cloud builds require an explicit `hakopod_cloud` tag as well as the
managed-cloud mode and signup policy. This is a shipped-artifact policy; anyone
modifying and rebuilding the open-source program can change it. There is no compilation on the installation target.

Verify downloaded assets before use:

```sh
sha256sum --check SHA256SUMS
gh attestation verify hakopod_0.1.0-alpha.2_linux_arm64.tar.gz --repo hakopod/hakopod
```

The attestation bundle is excluded from SHA256SUMS to avoid a circular digest.
Its signatures bind the other artifacts, including SHA256SUMS. Checksums alone
do not authenticate the publisher. Release workflow success establishes the
checks recorded in the reports, including full native systemd/K3s installation, resume and service restart recovery. Host reboot, public DNS/ACME, restore drills and physical RDS remain outside those checks.

## Local builds

The dedicated Linux installer is described in [installer/README.md](../installer/README.md). After the Go release build below, run `python3 release/build-installer.py` to build a separate local kit containing the actual dashboard runtime and installer inputs. Its output lives in `.local/installer-artifacts/<version>/`; no published download location is assumed. `python3 release/smoke-installer.py --arch arm64 --arch amd64` verifies the exact archives in disposable, memory-limited Linux containers. This verifies packaging/runtime, permissions and templates, not a full systemd/K3s host install. The original Go and dependency-lock SBOM scope below remains distinct.

Run from the repository root after the source and dashboard lockfile are stable:

```sh
pnpm --dir web install --frozen-lockfile
python3 release/build.py --version 0.1.0-dev
```

This performs actual Go cross-compilation and scans the resulting binaries with checksum-verified Syft 1.51.1. It builds Linux amd64/arm64 bundles containing CLI and server, plus macOS amd64/arm64 CLI bundles. CGO is disabled. Compilation and scanning use two logical processors and a 256 MiB Go soft-memory target; targets are sequential. The script does not use Docker. It finishes by running `release/verify-archives.py`, which validates archive bytes, platforms, notice coverage, the native CLI and checksums and writes `verification.json`.

Outputs are under `.local/releases/<CLI-version>/`: platform `.tar.gz` bundles, preserved upstream notices, a path-sanitized license inventory, actual SPDX 2.3 and CycloneDX SBOMs, Syft's native catalog, source/tool provenance, and `SHA256SUMS`. Only generated output directories bearing the tooling marker can be replaced. Compilation uses an immutable, fingerprinted source snapshot so concurrent development cannot mix files halfway through a build. Provenance records whether the working tree changed afterwards; rebuild from the final source before publication.

The SBOM combines packages observed in Go binaries with packages declared by `web/pnpm-lock.yaml`, including development and optional dependencies. It is **not** an inventory of a finished container image, operating-system layers, or the tree-shaken production dashboard. Syft resolves JavaScript license metadata from the npm registry and Go license files from the local Go module cache. Its declarations and `NOASSERTION` fields are retained rather than invented. The separate notice archive preserves locally available upstream texts; see `docs/licenses.md` for coverage and remaining release obligations.

The archives normalize file order, owners, modes and timestamps (`SOURCE_DATE_EPOCH`, default zero), and Go uses `-trimpath -buildvcs=false`. SBOM machine-specific staging, cache and home paths are replaced with `$RELEASE_STAGE`, `$GOPATH/pkg/mod` and `$HOME`; package identities and license data are retained. SBOM generation timestamps and document identifiers may vary; byte-for-byte reproducibility of the entire output directory is not claimed. Cross-compilation does not establish runtime support on untested targets. Local build commands do not publish, sign, notarize or vulnerability-clear these artifacts. The release workflow performs the separate attestation and publication steps.

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

## Prebuilt readiness probe

The release workflow builds `Dockerfile.probe` on native amd64 and arm64 runners
only after the archive, installer smoke and host acceptance jobs pass. Each
image is tested under a nonroot user, a read-only root filesystem and a 64 MiB
limit. The smoke checks the init-copy path, executable permissions, trust bundle,
license files and execution of the copied binary. Protocol behavior is covered
by the Go readiness suite; this packaging smoke does not send SMTP mail or claim
production SMTP delivery acceptance.

The tested images are combined into
`ghcr.io/hakopod/hakopod-probe:<release-tag>` (including the `v` prefix). No moving
`latest` tag is published, including for prereleases. `probe-image.txt` contains
the digest-pinned multi-platform reference; `probe-image.json` binds the native
smoke records and child digests to the release source revision. Both are included
in release checksums and file attestations. The index has its own OCI provenance
attestation. Final GitHub release publication depends on the image job and
successful anonymous pulls for both platforms. Each pull uses the verified child digest
to avoid classic Docker image stores trying to bind one index digest to two architectures.

Before the first release completes, an organization package administrator must
set the new **hakopod-probe** container package visibility to **Public** in GitHub
Packages. GHCR creates new packages as private by default; the workflow deliberately
fails its anonymous-pull gate until this is done. Grant this repository Actions
write access if the package already exists. The workflow uses its scoped
`GITHUB_TOKEN` with `packages: write`; users do not need a personal access token.
Run-scoped architecture tags are intermediate build outputs, not recommended
installation references. If a run stops before final publication, do not treat
its registry tags as a released version; use a published release's verified asset.

Infrastructure > Setup links to the release asset and shows the systemd setting.
See [helper installation](../docs/readiness.md#helper-installation). The helper
remains opt-in and digest-pinned; releases do not silently change an installation's
configured image or running application pods.

The public template submodule must be initialized (`git submodule update --init templates`).
Both builders snapshot its content; publication verifies every file against the
parent's pinned commit, including ignored additions. Catalog and upstream icon
license notices are packaged alongside binaries and dashboard assets.
