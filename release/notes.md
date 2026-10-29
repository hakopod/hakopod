Hakopod 0.1.0-alpha.70 tidies an app's security findings, adds a fix prompt for coding agents, and stops rollouts from showing as blocked.

- An app's security findings now sit in a list that is closed by default. Its summary gives the number of advisories and a count per severity. Each finding takes one compact row: severity, title, the affected service and version, the fix, and Rebuild or Update.
- Copy fix prompt puts a brief for a coding agent on the clipboard. It lists every finding with its service, version, identifiers and Hakopod's suggested fix. It asks the agent to move each runtime or image to the fixed patch release, skip advisories that cannot apply and summarise its changes. The brief tells the agent to treat advisory text as data, not instructions.
- A new pod during a rollout no longer makes the app show as Runtime blocked with "readiness check has not passed". Readiness probes start only after the startup probe passes, so Hakopod now reports a readiness failure only after the container has started and run for 30 seconds. A real failure still shows within about 40 seconds.

Install this prerelease:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.70/installer.sh -o installer.sh &&
sudo sh installer.sh --version 0.1.0-alpha.70
```

Upgrade an alpha.68 or alpha.69 installation:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.70/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.70
```

Older installations need a supported intermediate release. The upgrade backs up PostgreSQL and configuration before it restarts the management API and dashboard. Keep these backups. Replacing binaries does not reverse database migrations.

The OSS release and Cloud production deployment have separate verification.
