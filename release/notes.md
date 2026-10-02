Hakopod 0.1.0-alpha.47 adds managed MongoDB alongside PostgreSQL and Redis, with guided creation and a database dashboard built around observed state.

- MongoDB 8.0 uses the shared managed-database API, CLI and dashboard for creation, inspection, resizing, deletion and recovery. Choose a single replica-set member or a three-, five- or seven-member replica set.
- Private application bindings resolve scoped credentials and CA trust at deployment time. Supported routes require native TLS and preserve visible verification state when checks are incomplete or stale.
- Recovery uses encrypted, verified archives and separate empty targets. The source remains available, target application access stays closed during restore, and consequential cutover still requires inspection.
- The dashboard includes compact engine configuration, observed topology, operations, backups, recovery and connection guidance. Runtime facts come from the API rather than development fixtures.
- Installer packages include the qualified PostgreSQL, Redis and MongoDB controller definitions. Installing or upgrading Hakopod does not install missing database controllers; operators must prepare them separately.
- Redis uses the pinned 8.2.10 runtime with native TLS, upstream license texts and source references. PostgreSQL images are pinned to 17.11 and 18.6.

The engine guides and managed-database release acceptance record identify tested source revisions, runtime images and limits. The named development nodes share one physical VM, so these checks do not establish independent-zone, cross-provider or production availability.

MySQL, ClickHouse, Oracle Database, Vitess, Neon and Supabase remain unavailable while native qualification is incomplete. Their creation paths stay closed in the backend and dashboard. MongoDB public endpoints remain disabled; its release includes private application connections only. Oracle Free is proprietary free-to-use software; Enterprise and Data Guard also require an eligible licensed image.

## Installation and upgrade

```sh
curl --fail --location --proto '=https' --tlsv1.2 \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.47/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.47
```

Direct upgrades are supported from alpha.45 and alpha.46. Older installations need a supported intermediate release. Each upgrade backs up PostgreSQL and configuration and restarts the management API and dashboard. Retain backups because swapping binaries does not undo database migrations.

Release assets are published after source tests, native package smoke checks and the complete fresh-install and upgrade matrix on AMD64 and ARM64. Checksums, build provenance and native host reports accompany the downloads.

Cloud packages are released separately. This public engine release does not deploy or upgrade a Cloud installation.
