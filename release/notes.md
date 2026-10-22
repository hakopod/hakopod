Hakopod 0.1.0-alpha.65 lets a sandbox session template set its helper call timeout.

- Set `session.call_timeout_seconds` on a session template to allow helper calls longer than the default 150 seconds. The value must be between 10 and the template's `idle_seconds`, and at most 900.
- The API, the exec stream and the stored call lease all use the template's value. Sessions created before the upgrade keep 150 seconds.
- This release is covered by unit tests and real PostgreSQL store tests. It has not been run on a real cluster.

Read the [sandbox sessions guide](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.65/docs/sandbox-sessions.md).

Install this prerelease:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.65/installer.sh -o installer.sh &&
sudo sh installer.sh --version 0.1.0-alpha.65
```

Upgrade an alpha.63 or alpha.64 installation:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.65/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.65
```

Older installations need a supported intermediate release. The upgrade backs up PostgreSQL and configuration before it restarts the management API and dashboard. Keep these backups. Replacing binaries does not reverse database migrations.

The OSS release and Cloud production deployment have separate verification.
