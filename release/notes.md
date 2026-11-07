Hakopod 0.1.0-alpha.82 routes thousands of customer domains to one service, and lets Claude connect to Hakopod's MCP endpoint by signing in.

- **SaaS custom hostnames.** A public HTTP service with `[services.NAME.custom_hostnames] enabled = true` can carry up to 1000 customer domains by default, added through the API, CLI or MCP with no deployment per hostname. The customer points a CNAME at the service. Hakopod checks DNS, routes the hostname through shard Ingresses of up to 100 hosts, and issues a certificate per hostname. A hostname that resolves by address instead of CNAME also needs a TXT ownership record. Administrators turn the feature on per installation, and attaching hostnames needs the new `hostnames:manage` permission. See docs/custom-hostnames.md.
- **Sign in to HTTP MCP from Claude.** Add `https://HOST/api/v1/mcp?project=P&environment=E` as a custom connector, sign in to Hakopod and approve. The connection gets read and log access, and deploy access with `allow_deploy=true`. Exec, SQL and terminal connections still use an API key. See docs/http-mcp.md.
- The MCP `logs` tool returned no entries; it now returns them. MCP tools accept an application name as well as its ID, the `logs` description gives the query syntax, and `deployment` no longer sends the specification twice.

Install this prerelease:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.82/installer.sh -o installer.sh &&
sudo sh installer.sh --version 0.1.0-alpha.82
```

Upgrade an alpha.80 or alpha.81 installation:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.82/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.82
```

Older installations need a supported intermediate release. The upgrade backs up PostgreSQL and configuration before it restarts the management API and dashboard. Keep these backups. Replacing binaries does not reverse database migrations.

The OSS release and Cloud production deployment have separate verification.
