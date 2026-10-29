Hakopod 0.1.0-alpha.71 makes Managed Actions jobs faster.

- A first-time server installation now tries the shared overlay2 workspace for Managed Actions, instead of Docker's VFS driver, which copies every image layer in full. In the cold service image benchmark, pulling PostgreSQL fell from 95 to 15 seconds on AMD64 and from 71 to 12 seconds on ARM64, and Redis from 30 to 5 seconds. If the node's qualification probe fails, it keeps VFS and the next maintenance run tries again. Existing installations keep their saved profile; enable overlay2 with `sudo python3 ./installer/modules.py managed-actions --workspace-profile shared-overlay2-v1`.
- GitHub runner steps now get half of each slot's CPU and memory instead of a quarter, with the Docker sidecar taking the other half. With the default compute size, shell steps get about 2.35 cores instead of 1.18. GitLab runners keep their split because their jobs run inside Docker.
- A slot whose job has finished is replaced sooner. Hakopod checks the pool again after a second while the finished pod shuts down, instead of waiting a full 10-second pass.

Install this prerelease:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.71/installer.sh -o installer.sh &&
sudo sh installer.sh --version 0.1.0-alpha.71
```

Upgrade an alpha.69 or alpha.70 installation:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.71/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.71
```

Older installations need a supported intermediate release. The upgrade backs up PostgreSQL and configuration before it restarts the management API and dashboard. Keep these backups. Replacing binaries does not reverse database migrations.

The OSS release and Cloud production deployment have separate verification.
