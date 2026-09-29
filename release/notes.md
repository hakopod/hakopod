Hakopod 0.1.0-alpha.42 improves managed GitHub Actions runners and their dashboard.

- Service details and topology show images observed on running runner pods, including mixed images while older jobs drain. Missing observations remain explicit.
- Workflow output uses collapsible groups and bordered log rows, with bounded paging, search, wrapping and permission-aware loading.
- Runner pool setup follows guided steps with compact choices, cache guidance, scoped node selection and a review before creation or changes. Failed requests preserve entered values.
- Scheduling uses durable PostgreSQL leases, bounded concurrency, shared GitHub request budgets and bounded runner inventories. Selected nodes are checked again before registration and pod creation.
- Official GitHub Actions dependency caching is verified across fresh managed runners. Docker builds can use private registry caches; runner workspaces and Docker daemons remain isolated per job.

Cross-architecture Docker builds use BuildKit userspace emulation inside the existing gVisor sandbox. The documented setup was tested on AMD64 and ARM64 hosts. Kernel binfmt registration, including ordinary `docker/setup-qemu-action` installation, remains unavailable in gVisor; use the BuildKit configuration in [Managed Actions](https://github.com/hakopod/hakopod/blob/main/docs/managed-actions.md). Native cross-compilation is also covered and can reduce build time.

GitLab and Bitbucket runner support is deferred and is not included in this release.

## Installation and upgrade

```sh
curl --fail --location --proto '=https' --tlsv1.2 \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.42/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.42
```

Direct upgrades are supported from alpha.40 and alpha.41. Older installations need supported intermediate releases. Each upgrade backs up PostgreSQL and configuration and restarts the management API and dashboard. Retain backups because swapping binaries does not undo database migrations.

Publication requires source tests, native package smoke checks and the complete fresh-install and upgrade matrix on AMD64 and ARM64. GitHub job execution, fresh-runner cache reuse, sandbox cross-builds and control-plane failure tests are recorded in [Managed Actions qualification](https://github.com/hakopod/hakopod/blob/main/docs/managed-actions-qualification.md). Independent UI reviews cover grouped logs and guided pool setup in both themes at desktop and mobile sizes. Those checks do not establish arbitrary fleet-scale readiness; the supported topology remains one active Managed Actions API/controller process per installation.

The corresponding Cloud package is [Hakopod Cloud alpha.22](https://github.com/hakopod/hakopod-cloud/releases/tag/v0.1.0-alpha.22). Publishing these packages does not upgrade an existing server.
