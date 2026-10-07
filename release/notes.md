Hakopod 0.1.0-alpha.56 adds managed DuckDB through MyDuck and Oracle Database
Free, database bindings for existing application accounts, and editable TOML
for catalog deployments.

MyDuck runs one persistent DuckDB database with two private connections: MySQL
on port 3306 and PostgreSQL on port 5432. Both reach the same data. Connections
require TLS and the database CA. MyDuck supports parts of both wire protocols;
test your application's driver, migrations and queries before moving data.
Backups stop the instance and copy its database and write-ahead log. See the
[MyDuck guide](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.56/docs/managed-myduck.md)
for compatibility, resource limits and recovery instructions.

Oracle Database Free runs one instance managed by the Oracle Database Operator.
Applications use the restricted APP schema through private TCPS on port 2484.
Backups use Data Pump to capture that schema at a selected SCN; restore uses a
separate target for inspection before applications connect. Oracle Free is
proprietary software available at no charge, with Oracle's limits of two CPUs,
2 GB of database memory and 12 GB of user data. See the
[Oracle guide](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.56/docs/managed-oracle.md)
for the stack, connection settings and backup scope.

Both additions require Linux amd64 workers and supported persistent storage.
They have one instance, private endpoints and no automatic failover. Restarts
and certificate renewal interrupt connections, so clients need reconnect
handling. Oracle Enterprise, Data Guard and public database endpoints remain
unavailable.

For engines that support custom application accounts, a database binding can
select an existing username, database, password secret and SSL mode. The
dashboard can save a new password as a scoped secret; reviews and application
revisions contain only its reference.
The same options are available in TOML, the CLI and the TypeScript SDK. Omitted
options retain the managed defaults. Binding does not create database accounts
or change their grants.

TLS and endpoint validation follow each database engine's policy. PostgreSQL
custom users and databases use direct endpoints; pooled endpoints retain the
managed app login. Clients must trust the mounted database CA when verification
is enabled. See the
[connection guide](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.56/docs/managed-databases.md#application-connections)
for supported options and client trust configuration.

Catalog reviews now include **Edit TOML**. Change or remove services, volumes,
bindings and other settings, then review the validated configuration before
deploying. Existing applications show the complete configuration and any
removals in the diff. Required secrets follow the edited configuration, failed
requests preserve drafts, and deployment retains scope and revision checks.
The editor's schema is generated from the API contract.

This release retains the managed Vitess and Outpost capabilities introduced in
[alpha.55](https://github.com/hakopod/hakopod/releases/tag/v0.1.0-alpha.55) and
[alpha.54](https://github.com/hakopod/hakopod/releases/tag/v0.1.0-alpha.54).
Their documented platform, capacity and recovery requirements still apply.

Install alpha.56 with the published installer:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.56/installer.sh -o installer.sh &&
sudo sh installer.sh --version 0.1.0-alpha.56
```

Upgrade an existing installation with:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.56/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.56
```

Direct upgrades are supported from alpha.54 and alpha.55. Older installations
need a supported intermediate release. Each upgrade backs up PostgreSQL and
configuration and restarts the management API and dashboard. Retain backups
because replacing binaries does not reverse database migrations.

Hakopod Cloud requires its own package and rollout. This public release does
not establish production Cloud availability.
