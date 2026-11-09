Hakopod 0.1.0-alpha.84 can run Managed Actions jobs on single-use AWS VMs, and stops unrelated Go module changes from holding managed database runtimes.

- **Managed Actions on AWS VMs (preview).** An administrator adds an Actions capacity (a role in your AWS account, subnets, security groups, instance types and a hard instance limit) with `hakopod actions capacity set`, and a GitHub pool opts in with `actions.capacity`. Each job then runs on its own EC2 instance, spot by default, and the instance is terminated after the job. Jobs lost to a spot interruption are rerun up to `actions.spot_retries` times on on-demand capacity. Hakopod only terminates instances tagged with its installation ID, and the VM receives only the job's single-use runner configuration. Instances boot from RunsOn's public build of GitHub's Ubuntu 24.04 runner image by default, or from your own AMI, or from `hakopod-minimal`, a small image Hakopod builds in your account with `hakopod actions capacity build-image`. A capacity can keep a `warm_pool` of stopped on-demand instances that are started for one job each. In us-east-2 on t4g.small, the runner started 16 s after a cold launch from the default image and 14 s after starting a standby, against 73 s on stock Ubuntu. Launch, standby claims and image builds have run against a real AWS account; a full GitHub job on a VM has not yet: try it with a small `max_instances` first. See docs/managed-actions.md.
- **Go module changes no longer hold managed runtimes.** A go.mod or go.sum change now requires a new native qualification only when it changes what a managed runtime's own Go binaries compile. Today that is only MyDuck's storage helper.

Install this prerelease:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.84/installer.sh -o installer.sh &&
sudo sh installer.sh --version 0.1.0-alpha.84
```

Upgrade an alpha.82 or alpha.83 installation:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.84/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.84
```

Older installations need a supported intermediate release. The upgrade backs up PostgreSQL and configuration before it restarts the management API and dashboard. Keep these backups. Replacing binaries does not reverse database migrations.

The OSS release and Cloud production deployment have separate verification.
