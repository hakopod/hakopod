Hakopod 0.1.0-alpha.63 lets an administrator approve a node profile for a service that manages its own sandbox, such as a Kelvo node.

- Approve a relaxed pod security profile for one exact service from an administrator file (`HAKOPOD_NODE_PROFILES_FILE`). The profile can set an unconfined seccomp profile and a privileged container, so the service can use its own cgroup subtree and Landlock policy.
- A profiled service must set `node_name` to the approved node and run one replica. It always uses the Recreate strategy, so a restart never runs two copies.
- Only the service container is relaxed. The application's namespace moves to the privileged pod security level, so give the service an application of its own.
- Self-hosted installations only. Previews and service transfers never inherit a profile.
- This release is covered by unit tests. It has not been run on a real cluster.

Read the [node profiles guide](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.63/docs/node-profiles.md), including the kernel checks and the fixed-egress setup.

Install this prerelease:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.63/installer.sh -o installer.sh &&
sudo sh installer.sh --version 0.1.0-alpha.63
```

Upgrade an alpha.61 or alpha.62 installation:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.63/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.63
```

Older installations need a supported intermediate release. The upgrade backs up PostgreSQL and configuration before it restarts the management API and dashboard. Keep these backups. Replacing binaries does not reverse database migrations.

The OSS release and Cloud production deployment have separate verification.
