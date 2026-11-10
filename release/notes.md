Hakopod 0.1.0-alpha.87 brings Managed Actions VM capacity to the dashboard.

- **Actions VM capacity in Settings.** Installation administrators can add, edit, test and remove AWS and Google Cloud capacities under Settings → Actions VM capacity, run the launch test, and build or rebuild `hakopod-minimal` images and follow their progress. The page shows the installation's external ID and installation ID for the IAM trust policy. The API and `hakopod actions capacity` commands are unchanged.
- **Pools on VM capacity from the runner pool form.** The Capacity step of a GitHub runner pool can choose runner pods or a named capacity, and sets `spot_retries`. With a capacity, the form leaves out size, resources, placement and workspace quota, which the capacity decides. People who cannot list capacities can type the name.
- **VM slots in pool status.** Each runner registration shows its instance ID, machine type, spot or on-demand lifecycle and whether it started from a warm standby, and the pool lists its spot interruptions from the last 30 days with their rerun outcome.

Install this prerelease:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.87/installer.sh -o installer.sh &&
sudo sh installer.sh --version 0.1.0-alpha.87
```

Upgrade an alpha.85 or alpha.86 installation:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.87/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.87
```

Older installations need a supported intermediate release. The upgrade backs up PostgreSQL and configuration before it restarts the management API and dashboard. Keep these backups. Replacing binaries does not reverse database migrations.

The OSS release and Cloud production deployment have separate verification.
