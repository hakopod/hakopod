Hakopod 0.1.0-alpha.75 lets GitHub runner pools scale to zero between jobs and allows up to 256 environment variables per service.

- A Managed Actions pool for GitHub can stop all its runners while no jobs are waiting. In the pool's Capacity step choose **Scale to zero**, or set `idle_seconds` under `[services.<name>.actions]`; `replicas` then becomes the maximum number of concurrent jobs. A queued job whose `runs-on` labels match the pool starts a runner, and idle runners stop after the idle period. `min_replicas` keeps some runners online while idle. Busy jobs are never interrupted. GitLab and Bitbucket pools keep a fixed size.
- Scale to zero wakes from GitHub `workflow_job` webhook deliveries. Enable the **Workflow jobs** event on the GitHub App or webhook that sends this repository's events to Hakopod; new GitHub Apps created with builds enabled subscribe to it automatically. GitHub does not retry a failed delivery, so a job queued while Hakopod is unreachable waits until another job event, a runner kept online or a manual redelivery. See docs/managed-actions.md.
- A service can now have up to 256 environment variables, up from 128. Their names and values together are limited to 512 KiB, which keeps each deployment revision well inside Kubernetes object limits.

Install this prerelease:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.75/installer.sh -o installer.sh &&
sudo sh installer.sh --version 0.1.0-alpha.75
```

Upgrade an alpha.73 or alpha.74 installation:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.75/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.75
```

Older installations need a supported intermediate release. The upgrade backs up PostgreSQL and configuration before it restarts the management API and dashboard. Keep these backups. Replacing binaries does not reverse database migrations.

The OSS release and Cloud production deployment have separate verification.
