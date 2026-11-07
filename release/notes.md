Hakopod 0.1.0-alpha.81 stops background work from failing deploys, and lets services patch the Kubernetes objects Hakopod generates for them.

- **Deploys no longer fail a healthy database check.** On busy installations the 15-second resync and the database health loops used up the Kubernetes client's request budget, and the deploy gate reported "managed database is not healthy" while every database was fine. Background work now has its own client limit, the main client allows 50 requests per second, and database bindings refresh at most every 10 minutes (or when the database changes). The gate retries throttled or timed-out checks and names the database and the cause, for example "could not check managed database orders (ID) health: Kubernetes API client throttled". A rotated database password now reaches running pods within 10 minutes.
- **A deploy that changed nothing says so.** It skips recovery, keeps the last observation, and `plan` tells you that deploying again retries the failed release.
- **Kubernetes patches.** With the installation switch on (Settings, Kubernetes patches) and the new `kubernetes:patch` permission, a service can carry strategic-merge patches for its Deployment, Service and Ingress, such as tolerations, affinity, probes or `haproxy.org/*` annotations. A guard keeps Hakopod's security fields (images, security context, service account, host access, secret-backed env, Ingress routes and TLS) as generated. `hakopod manifests APP` shows the rendered objects. See docs/kubernetes-patches.md.
- `hakopod virtual-networks [NAME]` reads virtual networks, and planning a network change needs read access only.
- `termination_grace_seconds` accepts up to 3600; services that update by recreate get the grace period added to their rollout budget.
- Log history with `--since` reads newest first, and JSON lines cut at 16 KiB keep their level.

Install this prerelease:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.81/installer.sh -o installer.sh &&
sudo sh installer.sh --version 0.1.0-alpha.81
```

Upgrade an alpha.79 or alpha.80 installation:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.81/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.81
```

Older installations need a supported intermediate release. The upgrade backs up PostgreSQL and configuration before it restarts the management API and dashboard. Keep these backups. Replacing binaries does not reverse database migrations.

The OSS release and Cloud production deployment have separate verification.
