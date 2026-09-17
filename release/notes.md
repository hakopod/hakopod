Hakopod 0.1.0-alpha.12 adds explicit service resource budgets, increases standard profiles by 20%, and improves secret entry and HTTPS upgrade diagnostics. It replaces the incomplete alpha.11 tag, which has no installable assets.

## Service resources and secrets

Services can set CPU and memory requests and limits explicitly in TOML. The small, medium, large and compute defaults increase by 20%; memory rounds up to whole MiB. These are per-replica budgets. Explicit resource values are preserved, and the new defaults take effect when services are deployed or reconciled with this engine. Namespace quotas account for effective service resources and rolling-update overlap.

Service creation forms support entering secrets and importing or pasting dotenv content. Detected sensitive values are separated into secret entries instead of being retained as plain environment variables. Review the generated configuration and secret bindings before deployment.

## Upgrade from alpha.8, alpha.9 or alpha.10

Download this release's `installer.sh`, then run:

```sh
sudo sh installer.sh --upgrade --version 0.1.0-alpha.12
```

Confirm the displayed version. The bootstrap checks supported source versions, backs up PostgreSQL and configuration, replaces the API/dashboard binaries and verifies health. Application workloads remain running during the platform upgrade. Use this new bootstrap to receive the updated readiness checks and diagnostics; the previously installed maintenance helper remains unchanged.

HTTPS dashboard readiness now checks the configured public hostname against the local listener. Failed upgrades retain the underlying readiness error so operators can distinguish certificate, listener and service failures.

The Git/registry credential-store repair introduced in alpha.10 is retained. It verifies installation ownership and repairs affected namespace labels without replacing credentials or encryption keys. See [installation maintenance](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.12/docs/installation-maintenance.md).

Alpha.11's tag remains unchanged and is not an upgrade source because it produced no packages. Do not bypass upgrade guards or use `--resume` to change versions. Other installed versions require a separately verified upgrade path.

Publication requires fresh-install and upgrade acceptance on native Ubuntu 24.04: managed PostgreSQL on AMD64 and ARM64, plus local and TLS-verified external PostgreSQL on AMD64. Each source version is tested for account/session continuity, registry metadata, preserved secret data, backups and uninterrupted application workloads. Tests use published source installers and checksummed candidate target artifacts.

## Artifacts

Includes Linux AMD64/ARM64 server and CLI archives, macOS CLI archives, dashboard, installer, checksums, SBOMs, provenance and native acceptance reports. The readiness helper is `ghcr.io/hakopod/hakopod-probe:v0.1.0-alpha.12`; use the immutable reference in `probe-image.txt`.

This is an alpha release. Acceptance does not certify every operating system, public ACME issuance, arbitrary customer workloads or database restore procedures. Private Cloud pages are not included.
