Hakopod 0.1.0-alpha.66 lets Managed Actions run on additional K3s agents.

- Join extra Linux machines, such as cloud spot instances, as K3s agents and run GitHub, GitLab and Bitbucket runner pools on them. Adopt each agent on the server with `modules.py managed-actions --adopt-node`, then prepare it on its own host with `actions_runtime.py agent`.
- A node tainted `hakopod.io/actions-only=true:NoSchedule` runs runner pods only; application workloads never schedule there.
- `modules.py managed-actions --runtime-class-only` publishes the sandbox RuntimeClass without restarting the server, for clusters where only agents run jobs.
- Agents support the VFS workspace profile. This release is covered by unit tests. It has not been run on a real multi-node cluster.

Read the [Managed Actions guide](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.66/docs/managed-actions.md#additional-runner-nodes).

Install this prerelease:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.66/installer.sh -o installer.sh &&
sudo sh installer.sh --version 0.1.0-alpha.66
```

Upgrade an alpha.64 or alpha.65 installation:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.66/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.66
```

Older installations need a supported intermediate release. The upgrade backs up PostgreSQL and configuration before it restarts the management API and dashboard. Keep these backups. Replacing binaries does not reverse database migrations.

The OSS release and Cloud production deployment have separate verification.
