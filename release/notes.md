Hakopod 0.1.0-alpha.90 shows the cloud VMs of a Managed Actions pool on its Pods tab.

- **VM runners on the Pods tab.** A GitHub runner pool on an Actions VM capacity has no pods, so its Pods tab used to say "No pods observed" while jobs ran on cloud VMs. The tab now lists the pool's VM runners like pods: each VM's instance ID, its state (running a job, waiting for a job, shutting down), machine type, Spot or on-demand pricing and whether it came from a warm standby, its age and the runner it registered. Below them are the pool's Spot interruptions from the last 30 days, with the run each one hit and whether it was rerun. Refresh reloads the pool's status. Pools that run as pods are unchanged.

Install this prerelease:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.90/installer.sh -o installer.sh &&
sudo sh installer.sh --version 0.1.0-alpha.90
```

Upgrade an alpha.88 or alpha.89 installation:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.90/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.90
```

Older installations need a supported intermediate release. The upgrade backs up PostgreSQL and configuration before it restarts the management API and dashboard. Keep these backups. Replacing binaries does not reverse database migrations.

The OSS release and Cloud production deployment have separate verification.
