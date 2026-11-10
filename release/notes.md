Hakopod 0.1.0-alpha.86 lets sandbox sessions reach the internet through one proxy service, and gives sessions disk-backed scratch mounts.

- **Session egress through one proxy service.** Sessions still have no network access by default. Set `session.egress_proxy` to another service in the same application, which must have a `port`, must not be `public` and must not be a session itself, and the worker can reach that one service and nothing else. The session network policy allows TCP only to the proxy's Pods and Service IP on its port. Hakopod sets `HTTP_PROXY`, `HTTPS_PROXY`, their lowercase forms and `HAKOPOD_EGRESS_PROXY` to the proxy's cluster address and resolves it through a Pod host alias, so session DNS stays local. The worker runs under the guard's new `run --allow-ip-sockets` mode, which permits only AF_INET and AF_INET6 sockets beyond UNIX sockets; io_uring and every other socket family stay denied, and the helper command and readiness probe stay offline. What the proxy forwards, and to where, is the proxy service's own configuration. Without `egress_proxy`, sessions are unchanged. See docs/sandbox-sessions.md.
- **Disk scratch mounts for sessions.** Session temporary mounts with `memory=false` are now disk-backed scratch directories limited to their `size_mib`, up to 16384 MiB combined. Memory mounts keep their 128 MiB combined limit.
- **Managed Actions on Compute Engine Spot VMs (preview).** An Actions capacity can now set `cloud = "gcp"` to run each job's single-use runner on a Compute Engine VM in your Google Cloud project, Spot by default. Hakopod impersonates the capacity's service account, so no key is stored per capacity, and runner VMs get no service account. Launch tries each machine type in each zone until one has capacity, preempted VMs are marked interrupted, and stopped runners outside a slot are reaped. `hakopod-minimal` is the default image on Google Cloud. This has been tested against a fake Compute Engine API only, not yet a real project: try it with a small `max_instances` first. See docs/managed-actions.md.

Install this prerelease:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.86/installer.sh -o installer.sh &&
sudo sh installer.sh --version 0.1.0-alpha.86
```

Upgrade an alpha.84 or alpha.85 installation:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.86/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.86
```

Older installations need a supported intermediate release. The upgrade backs up PostgreSQL and configuration before it restarts the management API and dashboard. Keep these backups. Replacing binaries does not reverse database migrations.

The OSS release and Cloud production deployment have separate verification.
