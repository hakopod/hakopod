Hakopod 0.1.0-alpha.61 adds shared login and automatic connection sync for configured SyneHQ explorers.

- Open **Explore databases** from the Databases page without a second login.
- Sync eligible PostgreSQL, MySQL, MongoDB, ClickHouse, and Oracle connections. OOS encrypts imported hosts and passwords.
- Keep each explorer tab bound to its project and environment. Cloud also binds the tab to its workspace.
- Recheck current access before execution. Database writes retain browser approval, and audit records retain the human actor.
- Include service suspension changes in deployment plans.

Each scope needs an operator-configured OOS instance built from the merged managed-session adapter. Automatic Kubernetes installation remains disabled because the restricted-pod containment probe failed. This release does not weaken Kelvo containment or claim network qualification for a provisioned managed database.

Read the [explorer integration guide](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.61/docs/synehq-explorer.md) for configuration and verification limits.

Install this prerelease:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.61/installer.sh -o installer.sh &&
sudo sh installer.sh --version 0.1.0-alpha.61
```

Upgrade an alpha.59 or alpha.60 installation:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.61/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.61
```

Older installations need a supported intermediate release. The upgrade backs up PostgreSQL and configuration before it restarts the management API and dashboard. Keep these backups. Replacing binaries does not reverse database migrations.

The OSS release and Cloud production deployment have separate verification.
