Hakopod 0.1.0-alpha.72 redesigns the dashboard around what needs attention and what to do next, and lets you stage changes before deploying them.

- An application page now opens on a sentence that says how it is doing, a Needs attention block with the failing service's last error line and log tail, and service cards sorted with the most urgent first. Selecting a service opens an inspector with its facts, usage, pods, recent logs and events, and buttons for the full service page, its variables and its configuration. Activity lists each revision with what changed and why it failed.
- Configuration edits are now staged by default. Each application has one shared set of staged changes. Review N changes shows the combined diff, who staged each part and the plan check, then deploys everything in one release or discards it. Deploy now remains available for a single change. If someone deploys a newer revision meanwhile, staged changes are carried forward when they do not overlap, and flagged when they do.
- Project cards show the number of applications and services, how many are failing or deploying, a health dot per environment and when the project last deployed. The counts come from stored release results, not from the cluster.
- Empty pages now explain what belongs there and the first step: projects, environments, databases, source builds, networks, backups, and the empty tabs and Settings sections inside them.
- The light theme is now cool paper: a near-white page, white cards with a soft shadow, grey chips, ink text, and ink primary buttons beside white secondary ones. It replaces the warm stone background in both editions.
- Security, the catalog, source builds, networks, backups, databases, infrastructure and Settings have new layouts. Settings is one hub with a consistent row layout. Database topology draws members as plain cards. The configuration editor uses the dashboard's colours, and its file name is the editor's title bar.
- Managed Actions jobs pull Docker Hub images through an in-cluster pull-through mirror in hakopod-actions-mirror, so service images are no longer downloaded from Docker Hub for every job. If the mirror is missing, runners pull from Docker Hub directly. GitHub and GitLab runners that already have public egress may reach the mirror on TCP 5000; Bitbucket runners are unchanged.

Install this prerelease:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.72/installer.sh -o installer.sh &&
sudo sh installer.sh --version 0.1.0-alpha.72
```

Upgrade an alpha.70 or alpha.71 installation:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.72/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.72
```

Older installations need a supported intermediate release. The upgrade backs up PostgreSQL and configuration before it restarts the management API and dashboard. Keep these backups. Replacing binaries does not reverse database migrations.

The OSS release and Cloud production deployment have separate verification.
