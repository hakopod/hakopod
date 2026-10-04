Hakopod 0.1.0-alpha.53 repairs database network maintenance and Managed Actions stop deadlines after host maintenance.

Database health observation now refreshes the owned network policy for every lifecycle-ready database, including ordinary PostgreSQL databases without recovery metadata. When a Kubernetes API endpoint changes after a host restart, the next observation replaces its stale exact address before probing database health. Maintenance validates namespace and policy ownership, holds the existing revision lease, and preserves the isolation of restored databases awaiting inspection. Unavailable endpoint discovery leaves the previous policy unchanged and records a retry status.

A deployment that only suspends GitHub or GitLab Actions services can wait for their bounded runner lifetimes, including retained runners from an older configuration. The ordinary deployment deadline previously cut that wait short. Other deployment and recovery deadlines, cancellation checks and busy-runner protections remain unchanged. An operator's backup drain timeout is a separate maximum wait; this change does not guarantee that every long-running job fits the backup window.

This release retains the private self-hosted ClickHouse support added in [alpha.52](https://github.com/hakopod/hakopod/releases/tag/v0.1.0-alpha.52). Its controller, storage, runtime and connectivity requirements continue to apply.

Install alpha.53 with the published installer:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.53/installer.sh -o installer.sh &&
sudo sh installer.sh --version 0.1.0-alpha.53
```

Upgrade an existing installation with:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.53/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.53
```

Direct upgrades are supported from alpha.51 and alpha.52. Older installations need a supported intermediate release. Each upgrade backs up PostgreSQL and configuration and restarts the management API and dashboard. Retain backups because swapping binaries does not undo database migrations.
