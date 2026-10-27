Hakopod 0.1.0-alpha.67 keeps application logs after pods are replaced and adds eleven app templates.

- Logs from restarted, OOM-killed, evicted, deleted and rolled-out pods stay readable for 7 days and up to 512 MiB per application by default. Administrators can set 1–30 days and 64–4096 MiB in Settings › Logs. Read them in the Logs explorer, with `hakopod logs --history`, `--from`, `--to` and `--pod`, with `hakopod logs pods`, or through the MCP `logs` and `log_sources` tools. Without history, all of them show live logs as before and say why.
- Lines are masked before they are stored, while the pod and its secret references still exist. Anything Hakopod could not collect is recorded as a gap and shown, never hidden. No additional services are installed; the management API collects and stores the logs itself.
- Log masking is about 50 times faster, which also speeds up live logs.
- New templates: FreshRSS, NocoDB, Stirling PDF, Meilisearch, Umami, Wiki.js, Ghost, Directus, it-tools, Excalidraw and an internal-only Kafka. Every new template passed a runtime smoke test on amd64 and arm64.
- The installer gives the management API read-only access to `/var/log/pods` and keeps kubelet logs at 3 files of 20 MiB. An upgrade applies the access change; the new log size takes effect at the next K3s restart, such as a reboot. A native install with log history passed restart, deletion, OOM, rollout and MCP checks on a single-node host; multi-node clusters and long-running hosts are not yet verified.

Read the [application logs guide](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.67/docs/application-logs.md).

Install this prerelease:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.67/installer.sh -o installer.sh &&
sudo sh installer.sh --version 0.1.0-alpha.67
```

Upgrade an alpha.65 or alpha.66 installation:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.67/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.67
```

Upgrade with `installer.sh --upgrade`, not only from the dashboard, so the log access change is applied. Until then, log history is shown as not set up and live logs keep working.

Older installations need a supported intermediate release. The upgrade backs up PostgreSQL and configuration before it restarts the management API and dashboard. Keep these backups. Replacing binaries does not reverse database migrations.

The OSS release and Cloud production deployment have separate verification.
