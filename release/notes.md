Hakopod 0.1.0-alpha.47 adds the first release candidate for expanded self-hosted managed databases.

- MySQL 8.4, MongoDB 8.0, ClickHouse 26.3 and Oracle Database Free 23.26 now use the shared managed-database API, CLI and dashboard flows for creation, inspection, resize where supported, deletion and recovery.
- Private application bindings resolve scoped credentials and CA trust at deployment time. Supported routes require native TLS and preserve visible verification state when checks are incomplete or stale.
- Recovery uses encrypted, verified archives and separate empty targets. The source remains available, target application access stays closed during restore, and consequential cutover still requires inspection.
- The dashboard includes compact engine configuration, observed topology, operations, backups, recovery and connection guidance. Runtime facts come from the API rather than development fixtures.
- Installer packages include the PostgreSQL, Redis, MySQL, MongoDB and ClickHouse controller definitions. Oracle Free uses its pinned database image and does not add a shared controller.

The retained native evidence has limits. MySQL lifecycle, Router TLS and private binding checks passed, while the latest scaling and recovery-ingress acceptance remains pending. MongoDB, ClickHouse and Oracle Free passed the development cases recorded in their engine guides. The named development nodes share one physical VM, so these checks do not establish independent-zone, cross-provider or production availability.

Managed Vitess, Neon and Supabase remain unavailable. Oracle Enterprise and Data Guard remain unavailable without a licensed image and native acceptance. Public endpoints for MySQL, MongoDB, ClickHouse and Oracle remain disabled; this release includes private application connections only.

## Installation and upgrade

```sh
curl --fail --location --proto '=https' --tlsv1.2 \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.47/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.47
```

Alpha.47 is pending publication. Direct upgrades are supported from alpha.45 and alpha.46. Older installations need a supported intermediate release. Each upgrade backs up PostgreSQL and configuration and restarts the management API and dashboard. Retain backups because swapping binaries does not undo database migrations.

Publication requires source tests, native package smoke checks and the complete fresh-install and upgrade matrix on AMD64 and ARM64.

Cloud packages are released separately. This public engine release does not deploy or upgrade a Cloud installation.
