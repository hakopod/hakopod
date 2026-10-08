Hakopod 0.1.0-alpha.57 gives Oracle Database Free provisioning and health checks
separate reconciliation steps. Creating the Kubernetes resources no longer uses
up part of the health check's time allowance. The handoff is saved in PostgreSQL,
so it survives an API restart. Each step keeps the existing timeout and operation
lease.

This release includes managed DuckDB through MyDuck and Oracle Database Free,
introduced in alpha.56. MyDuck offers MySQL and PostgreSQL connections to the
same persistent DuckDB database. Oracle Free uses the Oracle Database Operator
and a restricted application schema. Both require TLS, Linux amd64 workers and
supported persistent storage. Both run one instance with private endpoints;
neither provides automatic failover. See the
[MyDuck guide](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.57/docs/managed-myduck.md)
and [Oracle guide](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.57/docs/managed-oracle.md)
for client compatibility, backup procedures and resource limits.

Oracle Free is proprietary software available at no charge. Oracle Enterprise,
Data Guard and public Oracle endpoints remain unavailable.

MyDuck and Vitess retain their existing runtime images and native test evidence.
The release records the exact reviewed source changes instead of relabeling old
test runs. Oracle retains its three cluster test cases and requires a new HTTP
API acceptance result for the provisioning change.

Install this prerelease with:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.57/installer.sh -o installer.sh &&
sudo sh installer.sh --version 0.1.0-alpha.57
```

Upgrade an existing alpha.55 or alpha.56 installation with:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.57/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.57
```

Older installations need a supported intermediate release. The upgrade backs up
PostgreSQL and configuration, then restarts the management API and dashboard.
Retain those backups; replacing binaries does not reverse database migrations.

Hakopod Cloud has a separate package and rollout. This OSS release does not
establish production Cloud availability.
