Hakopod 0.1.0-alpha.80 lets a public service set its own ingress timeout.

The ingress closes a request when the backend sends nothing for 30 seconds, the installation default. LLM gateways streaming long answers, report builders and slow uploads hit that limit and returned 504 while the service was healthy. A service can now raise it in its specification:

```toml
[services.app]
public = true
port = 8080
ingress_timeout_seconds = 600
```

The value is 5 to 3600 seconds and applies to that service's own ingress only. Serverless services keep using `serverless.request_timeout_seconds`. See docs/toml.md and docs/haproxy.md.

Install this prerelease:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.80/installer.sh -o installer.sh &&
sudo sh installer.sh --version 0.1.0-alpha.80
```

Upgrade an alpha.78 or alpha.79 installation:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.80/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.80
```

Older installations need a supported intermediate release. The upgrade backs up PostgreSQL and configuration before it restarts the management API and dashboard. Keep these backups. Replacing binaries does not reverse database migrations.

The OSS release and Cloud production deployment have separate verification.
