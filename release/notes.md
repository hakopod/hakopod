Hakopod 0.1.0-alpha.89 runs Managed Actions jobs on Azure Spot VMs and stops VM runner pools from reserving node capacity.

- **Managed Actions on Azure Spot VMs (preview).** An Actions capacity can now set `cloud = "azure"` to run each job's single-use runner on a VM in your subscription and resource group, Spot by default. Hakopod uses its own Azure identity: the managed identity of the VM it runs on, AKS workload identity, or an app registration whose `AZURE_TENANT_ID`, `AZURE_CLIENT_ID` and `AZURE_CLIENT_SECRET` are in Hakopod's environment. Nothing is stored per capacity, and runner VMs get no identity. Each runner gets a Standard public IP, which admits nothing inbound, and its NIC, IP and disk are deleted with it. A deallocated Spot VM counts as an interruption, and the runner stops taking work on a Preempt scheduled event. The image is stock Ubuntu 24.04 by default, or your own marketplace URN or image ID. Warm standbys and `hakopod-minimal` are not available on Azure yet. This has been tested against a fake Azure Resource Manager API only, not a real subscription: try it with a small `max_instances` first. Settings → Actions VM capacity walks through the `az` setup. See docs/managed-actions.md.
- **VM runner pools reserve no node capacity.** A GitHub pool on an Actions capacity runs its jobs on cloud VMs, but the plan preflight, placement and the managed platform's reservations still counted pod-sized requests for every slot, so a release on a busy cluster was refused with "CPU short by…". These checks now skip such pools, and the runner pool form's summary no longer shows pod reservations, placement or a workspace quota for them.
- **Clearer AWS setup in the capacity sheet.** The steps now name the exact console paths for the permissions policy and the role's custom trust policy, add the `hakopod-control-plane` instance role that Hakopod assumes from, and give `aws` commands that find the default VPC's public subnets and create an outbound-only security group.

Install this prerelease:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.89/installer.sh -o installer.sh &&
sudo sh installer.sh --version 0.1.0-alpha.89
```

Upgrade an alpha.87 or alpha.88 installation:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.89/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.89
```

Older installations need a supported intermediate release. The upgrade backs up PostgreSQL and configuration before it restarts the management API and dashboard. Keep these backups. Replacing binaries does not reverse database migrations.

The OSS release and Cloud production deployment have separate verification.
