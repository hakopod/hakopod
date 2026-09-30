Hakopod 0.1.0-alpha.43 improves managed GitHub Actions runner logs, placement and workspace qualification.

- Integrated workflow logs are wrapped by default and preserve GitHub secret masking. The runner observer withholds incomplete physical records and records larger than 16 KiB instead of forwarding fragments that could expose masked values. Live output stays withheld when redaction is incomplete; completed logs remain available from GitHub.
- Runner pool setup guides placement through approved nodes, architectures and resource limits while preserving the selected scope and entered values on failure.
- VFS remains the default workspace profile. Operators may explicitly select `shared-overlay2-v1` only after the named runtime passes its bounded qualification probe; this release never enables the profile automatically.
- The pinned BuildKit candidate is an explicit opt-in for qualified workloads. It does not change the global BuildKit default.
- Existing GitHub cache acceptance from alpha.42 remains valid.

A real ARM64 representative workload passed the shared-overlay2 qualification. Its measured phases were 503.061 seconds for the cold build, 5.338 seconds with the fresh registry cache and 113.304 seconds for the cold export. The resulting image contents, database migrations and an HTTP 200 health response were verified. Startup outside the inner workload still used Docker, and this acceptance did not verify a GitHub cache token or fleet-scale operation.

GitLab and Bitbucket runner support remains disabled and is not included in this release.

## Installation and upgrade

```sh
curl --fail --location --proto '=https' --tlsv1.2 \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.43/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.43
```

Direct upgrades are supported from alpha.41 and alpha.42. Older installations need supported intermediate releases. Each upgrade backs up PostgreSQL and configuration and restarts the management API and dashboard. Retain backups because swapping binaries does not undo database migrations.

Publication requires source tests, native package smoke checks and the complete fresh-install and upgrade matrix on AMD64 and ARM64. The measured runner checks do not establish arbitrary fleet-scale readiness; the supported topology remains one active Managed Actions API/controller process per installation.

Cloud alpha.24 is pending separate two-node acceptance and publication. This public engine release does not deploy or upgrade a Cloud installation.
