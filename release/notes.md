Hakopod 0.1.0-alpha.88 makes Actions VM capacity easier to set up from the dashboard.

- **Capacity editor in a side sheet with setup steps.** Adding or editing a capacity under Settings → Actions VM capacity now opens a side sheet that starts with what to prepare in the cloud account. On AWS, it shows the trust policy with this installation's external ID and the permissions policy with its `hakopod-installation` tag. On Google Cloud, it shows the `gcloud` commands for the service account, custom role and token creator binding. Each snippet has a copy button and is followed by network notes and what Test checks. Every field now says what it is for and what it accepts, and the fields are grouped into account and network, machines, and limits.

Install this prerelease:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.88/installer.sh -o installer.sh &&
sudo sh installer.sh --version 0.1.0-alpha.88
```

Upgrade an alpha.86 or alpha.87 installation:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.88/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.88
```

Older installations need a supported intermediate release. The upgrade backs up PostgreSQL and configuration before it restarts the management API and dashboard. Keep these backups. Replacing binaries does not reverse database migrations.

The OSS release and Cloud production deployment have separate verification.
