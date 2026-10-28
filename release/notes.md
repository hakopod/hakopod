Hakopod 0.1.0-alpha.68 checks running applications against security advisories and adds a SyneHQ OOS template for Kubernetes.

- Hakopod matches what each application runs against a Hakopod-signed advisory feed and OSV.dev. Affected apps appear in a dashboard banner and on a Security page with the evidence and the fix. Confirmed findings go to the app's notification channels.
- Where Hakopod can fix a finding, one step creates a new revision: it resolves image tags again to pick up the fixed image, or rebuilds an app built from source. Images pinned by digest need a manual change. Automatic patching can be turned on per app and is off by default.
- This is not a vulnerability scanner. Hakopod matches image digests, tags and runtime version variables only, not operating system packages, dependencies or application code. No findings does not mean an app is safe.
- The `synehq-oos` template runs the SyneHQ database explorer in Kubernetes pod mode. Query workers have no per-query cgroups, so the 768 MiB pod limit is the only boundary and one runaway query can restart the explorer. Landlock, seccomp, non-root execution and a read-only root filesystem still apply. Operators add the engine target by hand; a deployment on a real cluster is not yet verified.

Read the [security advisories guide](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.68/docs/security-advisories.md) and the [SyneHQ explorer guide](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.68/docs/synehq-explorer.md).

Install this prerelease:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.68/installer.sh -o installer.sh &&
sudo sh installer.sh --version 0.1.0-alpha.68
```

Upgrade an alpha.66 or alpha.67 installation:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.68/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.68
```

Older installations need a supported intermediate release. The upgrade backs up PostgreSQL and configuration before it restarts the management API and dashboard. Keep these backups. Replacing binaries does not reverse database migrations.

The OSS release and Cloud production deployment have separate verification.
