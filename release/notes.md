Hakopod 0.1.0-alpha.76 makes the dashboard stop promptly, which takes about 30 seconds off every dashboard restart, host reboot and upgrade.

- The dashboard now exits within five seconds of being asked to stop. Before, it ignored the stop signal, and systemd waited its full 30-second timeout before killing it, so a reboot or upgrade sat idle for half a minute before K3s could stop. In-flight requests still get up to five seconds to finish.
- The packaged installer smoke test now fails if the dashboard is still running eight seconds after it is told to stop.

Install this prerelease:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.76/installer.sh -o installer.sh &&
sudo sh installer.sh --version 0.1.0-alpha.76
```

Upgrade an alpha.74 or alpha.75 installation:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.76/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.76
```

Older installations need a supported intermediate release. The upgrade backs up PostgreSQL and configuration before it restarts the management API and dashboard. Keep these backups. Replacing binaries does not reverse database migrations.

The OSS release and Cloud production deployment have separate verification.
