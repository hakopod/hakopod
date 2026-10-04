Hakopod 0.1.0-alpha.49 adds Mathesar and updates Xem with independent choices for bundled or existing data services.

- Mathesar 0.12.0 can use bundled PostgreSQL or an existing PostgreSQL database. Existing connections default to verified TLS. Its shared RWX media storage requires a suitably configured self-hosted or user-owned server; Cloud hosted compute keeps this preset unavailable.
- Xem can use bundled or existing PostgreSQL, Redis and object storage independently. Bundled MinIO keeps uploads private and serves signed downloads through the installation's HTTPS origin.
- Xem uses the upstream portable frontend image and backend fixes released from `mailxem/mail`. Its preset excludes managed SES/SMTP, payments, MCP and hosted AI; users configure their own sending provider in Xem.
- The dashboard shows connection fields only for selected external services, preserves drafts when switching options, and reviews the resulting services and scoped secrets before deployment.
- The API, CLI contract and dashboard support the same conditional template fields. RSA encryption keys can be generated securely or supplied as existing keys, with strict format and size validation.

The catalog contains 78 entries: 43 deployment presets and 35 migration guides. All preset images are pinned by digest. Migration guides remain unavailable through the deployment API; this release does not promote unqualified managed database engines.

The template guides record the precise validation scope. Mathesar passed real development-cluster acceptance on native AMD64 and ARM64, including login, uploads, restart persistence, existing-database reattachment and PostgreSQL TLS checks. Xem's upstream regressions and earlier login/form/upload checks do not establish arbitrary external-provider connectivity or real email delivery; the final bundled-MinIO cluster matrix was interrupted by development disk pressure.

Database and platform pages now share navigation. The source includes guided platform configuration, certificate inspection and recovery work, but Neon, Supabase and Vitess remain disabled until their full native qualification passes. MySQL, ClickHouse and Oracle Database also remain unavailable. This release does not enable additional public database endpoints.

Installer packages now record which runtimes are enabled and which are held. Vitess controller definitions cannot enter a release bundle while its gate is closed. Enabling a runtime still requires its exact source, images and native evidence to pass the release verifier. A separate status record accompanies each held runtime.

The engine guides and managed-database acceptance record identify tested source revisions, runtime images and limits. Development checks on nodes sharing one physical VM do not establish independent-zone, cross-provider or production availability. Oracle Free is proprietary free-to-use software; Enterprise and Data Guard also require an eligible licensed image.

## Installation and upgrade

```sh
curl --fail --location --proto '=https' --tlsv1.2 \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.49/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.49
```

Direct upgrades are supported from alpha.47 and alpha.48. Older installations need a supported intermediate release. Each upgrade backs up PostgreSQL and configuration and restarts the management API and dashboard. Retain backups because swapping binaries does not undo database migrations.

Installing or upgrading Hakopod does not install missing database controllers, increase workspace capacity or approve a storage class. Operators must prepare those separately.

Release assets are published after source tests, native package smoke checks and the fresh-install and upgrade matrix on AMD64 and ARM64. Checksums, build provenance and native host reports accompany the downloads.

Cloud packages are released separately from the same engine source. Publishing the public engine release does not automatically upgrade existing self-hosted installations.
