Hakopod 0.1.0-alpha.64 adds sidecar containers.

- Declare up to four sidecars per service with `[[services.<name>.sidecars]]`. Each has a digest-pinned image, its own environment, secrets, CPU and memory, and can mount the service's volumes. Sidecars start before the main container and count toward capacity and quota.
- Sidecars run with the restricted security baseline. An administrator can grant a named sidecar `NET_ADMIN`/`NET_RAW`, the host `/dev/net/tun` device and root through a node profile, for example a Tailscale exit-node sidecar.
- Node profiles can also let the profiled service container start as root, for an entrypoint that sets up its own cgroup subtree before dropping privileges.
- The terminal and exec accept a sidecar's container name. Logs still show the main container.
- This release is covered by unit tests. It has not been run on a real cluster.

Read the [sidecars guide](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.64/docs/sidecars.md).

Install this prerelease:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.64/installer.sh -o installer.sh &&
sudo sh installer.sh --version 0.1.0-alpha.64
```

Upgrade an alpha.62 or alpha.63 installation:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.64/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.64
```

Older installations need a supported intermediate release. The upgrade backs up PostgreSQL and configuration before it restarts the management API and dashboard. Keep these backups. Replacing binaries does not reverse database migrations.

The OSS release and Cloud production deployment have separate verification.
