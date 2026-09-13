# Prebuilt installation verification

The current verified prerelease is
[`v0.1.0-alpha.4`](https://github.com/hakopod/hakopod/releases/tag/v0.1.0-alpha.4),
from source `99a70c7f6b872176ec6238e1ce3b6e97682d3d0a`.
[Release run 34788441399](https://github.com/hakopod/hakopod/actions/runs/34788441399)
passed all eight jobs and published on September 13, 2026 at 23:11:53 UTC
(September 14 in Asia/Kolkata).

The same main commit passed all seven jobs in
[candidate run 34787920322](https://github.com/hakopod/hakopod/actions/runs/34787920322)
before the tag was created. The preparation branch also passed
[run 34787457562](https://github.com/hakopod/hakopod/actions/runs/34787457562).
The release rebuilt and tested the exact tagged source.

| Host | PostgreSQL mode | Result |
| --- | --- | --- |
| Ubuntu 24.04, native amd64 | Installer-owned K3s pod | Passed |
| Ubuntu 24.04, native arm64 | Installer-owned K3s pod | Passed |
| Ubuntu 24.04, native amd64 | Existing local PostgreSQL over loopback TCP | Passed |
| Ubuntu 24.04, native amd64 | Existing PostgreSQL with verified TLS | Passed |

Each host job installed the exact downloaded, checksum-verified archives on a
fresh GitHub-hosted VM with systemd and cgroup v2. The API and dashboard became
healthy, the node reached Ready, first-user setup remained open, and public
signup remained disabled. Resume preserved installation identity and saved
secrets. Restarting the API and dashboard recovered without creating an account.

Existing-database checks used a separate PostgreSQL fixture on the same VM.
They refused a nonempty database before creating installation state and preserved
an unrelated sentinel database through installation and resume. The external
mode used a verified test CA and refused an untrusted certificate before cluster
mutation.

The jobs used configured limits of K3s 2048 MiB, API 256 MiB, dashboard 320 MiB,
managed PostgreSQL 256 MiB and HAProxy 256 MiB. These are hard limits, not measured
idle-memory figures or workload capacity guarantees. Docker installation,
optional application storage and ACME were disabled.

This evidence does not cover host reboot, public DNS or ACME issuance, physical
AWS RDS, restore drills, other supported Linux distributions, or existing-database
modes on arm64. Full host jobs invoke the verified installer kit directly. They
check the packaged bootstrap defaults but do not execute the public website's
interactive curl-to-shell flow.

## Published artifact checks

All 21 public assets were downloaded. The complete SHA256 manifest and all 20
signed subjects were verified against this repository, release workflow, tagged
source and GitHub-hosted runner identity. The attestation bundle is excluded from
the checksum inventory to avoid a circular digest. Four host reports and both
native smoke reports bind their results to the source revision and artifact
hashes. The six CLI/server binaries have the expected Linux or macOS architecture;
public server binaries carry the explicit `hakopod_selfhosted` build tag.

Environment variables and operator TOML cannot enable public signup in these
binaries. First-owner setup and explicit Free team invitations use their separate
authorization paths. Actual upstream OAuth and external-secret provider accounts
were not exercised by release acceptance.

## Bootstrap default correction

Alpha.3's generated bootstrap still defaulted to alpha.2, even though its own
binaries and direct-kit host checks passed. Its tag and assets remain unchanged,
and its release notes explain the explicit-version workaround.

Alpha.4 stamps the requested version into both generated bootstrap copies without
editing the source template. Packaging, native smoke/host jobs and publication
check their defaults and byte equality. Regression checks reject malformed
declarations and reproduce the mismatch in the published alpha.3 artifact.
Both downloaded alpha.4 copies select `0.1.0-alpha.4` without a version override.

## Website bootstrap

The website serves the exact release `installer.sh` at
<https://hakopod.com/scripts/installer.sh>. Its SHA-256 is:

```text
315182e8d724aff80f338d2da2c65f54148ffb57cb51b2dd3dd359ca7b796a5c
```

The release manifest SHA-256 is:

```text
af61569c26e00986adea400f4bd2032c51a2a06f3ec7d4860299d223863698cf
```

This endpoint is deployed separately from GitHub releases and pins alpha.4. A new
tag does not update it automatically. The website takes displayed versions and
release links from the same metadata used by its bootstrap integrity checks.
HTTPS status, plain-text content type, cache policy, default version and exact
response bytes are checked after deployment.

The website's separate postdeployment workflow runs on native AMD64 and ARM64
GitHub-hosted VMs. It checks the served bootstrap and a preflight manifest fetch
against the reviewed hashes, then lets the bootstrap download its manifest and
archives normally. It verifies the archive checksums, extracts the installer kit
and reaches a dry-run plan without creating installation state. This supplements
the full host checks above; it does not exercise interactive terminal input or
install through the public endpoint.
