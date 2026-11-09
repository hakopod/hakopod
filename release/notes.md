Hakopod 0.1.0-alpha.83 lets services give their temporary mounts as much space as they need.

- **Temporary mounts beyond 128 MiB.** `temporary_mounts` were capped at 128 MiB per service. A service can now ask for more, for example `temporary_mounts = [{ mount_path = "/tmp", size_mib = 20480 }]`, up to 1 TiB in total. Above 128 MiB of disk-backed scratch, Hakopod raises the container's ephemeral-storage request and limit and the namespace quota by the same amount, so the pod is placed on a node with that much free disk and is not evicted for using it. Memory-backed mounts (`memory = true`) must stay below the service's memory limit. Services at or below 128 MiB keep their current resources and do not restart. Hakopod Cloud keeps the 128 MiB cap. See docs/toml.md.

Install this prerelease:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.83/installer.sh -o installer.sh &&
sudo sh installer.sh --version 0.1.0-alpha.83
```

Upgrade an alpha.81 or alpha.82 installation:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.83/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.83
```

Older installations need a supported intermediate release. The upgrade backs up PostgreSQL and configuration before it restarts the management API and dashboard. Keep these backups. Replacing binaries does not reverse database migrations.

The OSS release and Cloud production deployment have separate verification.
