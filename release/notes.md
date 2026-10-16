Hakopod 0.1.0-alpha.62 makes private database access, such as Amazon RDS in the same VPC, approvable without SSH.

- Approve **Private destinations** from Settings. Bindings live in the database, and changes apply without restarting the API.
- Warn in `hakopod plan` when a service environment value points at a private address with no `private_egress` approval.
- `HAKOPOD_PRIVATE_EGRESS_FILE` remains supported as a read-only source.
- Support named PostgreSQL databases and owner logins.
- Exclude stopped services from deployment compute capacity, and reserve complete readiness budgets during rollout.
- MCP tool input schemas no longer serialize `required` as null.

Read the [private database access guide](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.62/docs/private-database-access.md).

Install this prerelease:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.62/installer.sh -o installer.sh &&
sudo sh installer.sh --version 0.1.0-alpha.62
```

Upgrade an alpha.60 or alpha.61 installation:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.62/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.62
```

Older installations need a supported intermediate release. The upgrade backs up PostgreSQL and configuration before it restarts the management API and dashboard. Keep these backups. Replacing binaries does not reverse database migrations.

The OSS release and Cloud production deployment have separate verification.
