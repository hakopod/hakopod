Hakopod 0.1.0-alpha.46 improves certificate recovery, configuration editing and runner setup.

- Application certificate issuers can be created after a failed or cancelled deployment, allowing missing issuer configuration to be repaired before redeployment. Deployment, revision, deletion and volume-resize fences remain enforced.
- New HTTP-01 issuers use RuntimeDefault seccomp for restricted solver pods. Identical retries reuse older managed issuers and account keys without silently changing their configuration; operators must correct older solver profiles where restricted Pod Security requires it.
- Trusted embedded operators can disable the private HAProxy HTTPS redirect behind an HTTPS-enforcing front proxy. Self-hosted and tenant runtimes retain redirects by default; certificate and issuer attachments are preserved.
- TOML and YAML configuration editors use Monaco with Catppuccin themes. Platform configuration and runner setup use compact pages, clear help and draft-preserving error recovery.
- The qualified MongoDB TLS scaling controller is published with content verification before its immutable version tag is assigned.

Automatic certificates require cert-manager, an explicit ACME contact, correct DNS and reachable challenge ports. Existing installations retain their issuer configuration; upgrading alone does not create or migrate issuers.

## Installation and upgrade

```sh
curl --fail --location --proto '=https' --tlsv1.2 \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.46/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.46
```

Direct upgrades are supported from alpha.44 and alpha.45, the last two published, installable versions. Older installations need supported intermediate releases. Each upgrade backs up PostgreSQL and configuration and restarts the management API and dashboard. Retain backups because swapping binaries does not undo database migrations.

Publication requires source tests, native package smoke checks and the complete fresh-install and upgrade matrix on AMD64 and ARM64. Public ACME issuance and production configuration require validation on the target installation.

Cloud packages are released separately. This public engine release does not deploy or upgrade a Cloud installation.
