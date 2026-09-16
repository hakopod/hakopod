Hakopod 0.1.0-alpha.10 fixes self-hosted Git and registry credential storage and adds verified bootstrap upgrades from alpha.8 and alpha.9.

## Credential storage

The dashboard's Let's Encrypt setup could remove a namespace ownership label, causing both “Git connection credential storage is unavailable” and “registry credential storage is unavailable.” The installer now preserves the label. The new bootstrap upgrades repair affected namespaces after checking installation ownership and taking backups, without replacing credentials or encryption keys.

The verified installer kit also contains `installer/credentials.py` for repairing the label without upgrading or restarting services. It refuses foreign namespaces. See [installation maintenance](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.10/docs/installation-maintenance.md).

## Upgrade from alpha.8 or alpha.9

Download this release's `installer.sh`, then run:

```sh
sudo sh installer.sh --upgrade --version 0.1.0-alpha.10
```

Confirm the displayed version. The bootstrap checks supported source versions before asking for confirmation. It backs up PostgreSQL and configuration, replaces the API/dashboard binaries and verifies health. Application workloads remain running. The installed privileged maintenance helper and runtime dependencies remain unchanged; use this new bootstrap to get the namespace repair.

Alpha.9's empty upgrade manifest remains unchanged. Do not bypass its guard or use `--resume` to change versions. Versions other than the two listed sources need a separately verified upgrade path.

Publication requires native Ubuntu 24.04 installation and upgrade tests for both CPU architectures, with managed, local and TLS-verified external PostgreSQL cases. Upgrade tests start from published source artifacts and check account/session continuity, registry metadata, preserved secret data, database/configuration backups and an uninterrupted workload. Candidate release downloads are routed to checksummed local assets during testing; migrations and host operations run against real services.

## Artifacts

Includes Linux amd64/arm64 server and CLI archives, macOS CLI archives, dashboard, installer, checksums, SBOMs, provenance and native acceptance reports. The readiness helper is `ghcr.io/hakopod/hakopod-probe:v0.1.0-alpha.10`; use the immutable reference in `probe-image.txt`.

This is an alpha release. Tests do not certify every operating system, public ACME issuance, arbitrary customer workloads or database restore procedures. Private Cloud pages are not included.
