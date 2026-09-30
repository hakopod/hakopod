Hakopod 0.1.0-alpha.45 adds Hakopod Edge traffic rules and application-owned TLS issuers.

- Installation administrators can configure Hakopod Edge through the HAProxy editor in Settings. Ordered hostname and path rules support IPv4/IPv6 allow and deny lists, per-client rate limits and country restrictions supplied by an explicitly trusted proxy. Protection remains disabled until enabled and reviewed.
- Edge changes use durable, audited revisions and optimistic concurrency. Applied status requires acknowledgement from the running HAProxy workers; saving a ConfigMap alone does not mark a policy active. Rate limits apply separately to each HTTP/HTTPS listener and ingress process.
- Application certificate settings discover the available default issuer and application-owned issuers. Authorized users can create an application HTTP-01 issuer, select it for a service or upload a hostname-matching certificate and key. Cloud keeps application issuer access separate from shared operator settings.
- Configuration and certificate forms preserve drafts, including entered certificate data, after failed requests. Review screens, error recovery and narrow-screen layouts keep actions reachable.

Hakopod Edge uses the existing HAProxy ingress. Country rules rely on trusted proxy headers; the release does not add a GeoIP database. Automatic certificates require cert-manager, an explicit ACME contact, correct DNS and reachable challenge ports. Existing installations retain their issuer configuration; upgrading alone does not create a default issuer.

## Installation and upgrade

```sh
curl --fail --location --proto '=https' --tlsv1.2 \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.45/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.45
```

Direct upgrades are supported from alpha.42 and alpha.44, the last two published, installable versions. Older installations need supported intermediate releases. Each upgrade backs up PostgreSQL and configuration and restarts the management API and dashboard. Retain backups because swapping binaries does not undo database migrations.

Publication requires source tests, native package smoke checks and the complete fresh-install and upgrade matrix on AMD64 and ARM64. Edge and certificate behavior passed the named development-cluster acceptance checks. Public ACME issuance and production configuration still require validation on the target installation.

Cloud packages are released separately. This public engine release does not deploy or upgrade a Cloud installation.
