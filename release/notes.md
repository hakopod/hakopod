Hakopod 0.1.0-alpha.78 adds eight catalog presets for monitoring, calendars, newsletters and database administration.

- **Radicale** 3.9 is a small CalDAV and CardDAV server for calendars, contacts and task lists, with one login from a secret.
- **Grafana Loki** 3.7 stores logs for Grafana on the local filesystem, kept for seven days.
- **VictoriaMetrics** 1.155 is a single-node time-series database that takes Prometheus remote write and answers PromQL, kept for 30 days.
- **Beszel** 0.21 is a lightweight server monitoring hub. Agents run on the machines you watch; the first administrator is created from the configured email and a secret.
- **pgAdmin** 9.19 is the PostgreSQL web administration tool.
- **listmonk** 6.2 is a newsletter and mailing list manager with a bundled PostgreSQL. Set the root URL and an SMTP server before sending.
- **OpenTelemetry Collector** 0.164 receives OTLP over HTTP; bring your own exporters.
- **Alertmanager** 0.34 groups and routes Prometheus alerts; bring your own receivers.

Loki, VictoriaMetrics, the collector and Alertmanager have no login, so they stay private and are reached by other apps over virtual networks, like the Prometheus preset.

Install this prerelease:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.78/installer.sh -o installer.sh &&
sudo sh installer.sh --version 0.1.0-alpha.78
```

Upgrade an alpha.76 or alpha.77 installation:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.78/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.78
```

Older installations need a supported intermediate release. The upgrade backs up PostgreSQL and configuration before it restarts the management API and dashboard. Keep these backups. Replacing binaries does not reverse database migrations.

The OSS release and Cloud production deployment have separate verification.
