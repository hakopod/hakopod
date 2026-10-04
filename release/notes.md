Hakopod 0.1.0-alpha.48 keeps managed PostgreSQL, Redis and MongoDB available while work on the other database runtimes continues.

Database and platform pages now share navigation. The source includes guided platform configuration, certificate inspection and recovery work, but Neon, Supabase and Vitess remain disabled until their full native qualification passes. MySQL, ClickHouse and Oracle Database also remain unavailable. This release does not enable additional public database endpoints.

Installer packages now record which runtimes are enabled and which are held. Vitess controller definitions cannot enter a release bundle while its gate is closed. Enabling a runtime still requires its exact source, images and native evidence to pass the release verifier. A separate status record accompanies each held runtime.

The engine guides and managed-database acceptance record identify tested source revisions, runtime images and limits. Development checks on nodes sharing one physical VM do not establish independent-zone, cross-provider or production availability. Oracle Free is proprietary free-to-use software; Enterprise and Data Guard also require an eligible licensed image.

## Installation and upgrade

```sh
curl --fail --location --proto '=https' --tlsv1.2 \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.48/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.48
```

Direct upgrades are supported from alpha.46 and alpha.47. Older installations need a supported intermediate release. Each upgrade backs up PostgreSQL and configuration and restarts the management API and dashboard. Retain backups because swapping binaries does not undo database migrations.

Installing or upgrading Hakopod does not install missing database controllers, increase workspace capacity or approve a storage class. Operators must prepare those separately.

Release assets are published after source tests, native package smoke checks and the fresh-install and upgrade matrix on AMD64 and ARM64. Checksums, build provenance and native host reports accompany the downloads.

Cloud packages are released separately. This public engine release does not deploy or upgrade a Cloud installation.
