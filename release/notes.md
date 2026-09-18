Hakopod 0.1.0-alpha.13 adds administrator-approved access to private databases and corrects resource profile labels for services with custom CPU or memory settings.

## Private database access

Self-hosted services can reference `private_egress = ["orders-db"]` in TOML. The installation administrator approves exact project, environment, application and service scopes, private destination CIDRs and TCP ports in a protected file configured through `HAKOPOD_PRIVATE_EGRESS_FILE`.

Hakopod validates grants during planning and reconciliation and applies them to its managed network policies. Removing a reference and successfully deploying removes that service's grant. Internal-only services and jobs can use approved destinations; other private networks remain blocked. Database secrets never grant network access. Managed Cloud does not support these grants.

See [private database configuration](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.13/docs/private-database-access.md) for setup, failover and revocation. Changing the operator file requires restarting the API and deploying to update existing policies. A restart alone does not revoke running pods' access. Existing operator workaround policies remain additive and must be removed separately after verifying their replacement.

## Custom resource labels

Services with explicit CPU or memory settings now show **Custom** on application cards, the service detail header and the topology inspector, including partial overrides and jobs. The selected base size still supplies defaults for fields without overrides. This label fix does not change resource requests or limits.

## Upgrade

Supported source versions are alpha.8, alpha.9, alpha.10 and alpha.12, subject to this release's native installer acceptance. Alpha.11 has no installable assets and is not an upgrade source.

Download this release's `installer.sh`, then run:

```sh
sudo sh installer.sh --upgrade --version 0.1.0-alpha.13
```

The installer checks the supported source version, backs up PostgreSQL and configuration, replaces the API/dashboard and verifies readiness. Application workloads remain running during the platform upgrade. Do not bypass upgrade guards or use `--resume` to change versions.

## Verification and artifacts

The private-egress runtime tests passed on native AMD64 and ARM64: default denial, approved destination/port access, denial for other ports and services, and removal of access. The Custom label passed independent light/dark desktop/mobile UI review, and Go/dashboard CI passed.

Publication additionally requires fresh-install and upgrade acceptance with managed PostgreSQL on AMD64/ARM64 and local/TLS-verified external PostgreSQL on AMD64. Packaged artifacts include Linux server/CLI, macOS CLI, dashboard, installer, checksums, SBOMs, provenance and acceptance reports. The readiness helper is `ghcr.io/hakopod/hakopod-probe:v0.1.0-alpha.13`; use the immutable reference in `probe-image.txt`.

This is an alpha prerelease. It does not certify arbitrary customer workloads, physical RDS configurations, public ACME issuance or database restores. Private Cloud pages are not included.
