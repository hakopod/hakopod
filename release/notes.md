Hakopod 0.1.0-alpha.77 adds two catalog presets: Plausible Analytics and Headscale.

- **Plausible Analytics** (Community Edition 3.2.1) is cookie-free web analytics. Its PostgreSQL and ClickHouse are each bundled or existing. An existing database takes a complete connection URL secret, and a ClickHouse already installed on the server can be used directly. The first account to register becomes the owner.
- **Headscale** 0.29.6 is a control server for Tailscale clients, so your own laptops, phones and servers can join one private WireGuard network. It runs without added capabilities or a public UDP port. It does not protect the Hakopod dashboard or API.
- Templates accept a new `clickhouse-url` secret format: an http or https URL with credentials and a database path.

Install this prerelease:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.77/installer.sh -o installer.sh &&
sudo sh installer.sh --version 0.1.0-alpha.77
```

Upgrade an alpha.75 or alpha.76 installation:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.77/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.77
```

Older installations need a supported intermediate release. The upgrade backs up PostgreSQL and configuration before it restarts the management API and dashboard. Keep these backups. Replacing binaries does not reverse database migrations.

The OSS release and Cloud production deployment have separate verification.
