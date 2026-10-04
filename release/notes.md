Hakopod 0.1.0-alpha.52 adds private managed ClickHouse 26.3 for self-hosted installations.

ClickHouse can run as one standalone data member or as a cluster with replicated shards and three Keeper members. Replication copies data within each shard. Queries across shards still need a Distributed table or explicit query design; a balanced connection does not make a local table query span the cluster.

Private native and HTTPS connections require TLS and the issued CA. Managed application bindings supply scoped credentials, endpoints and trust material through the application's service configuration. The database cockpit reports requested and observed topology, members, Keeper state, connected application bindings and bounded engine and workload metrics. Missing or stale metrics remain unavailable rather than appearing as zero.

Backups capture one native archive per shard, sequentially, and encrypt the resulting managed-backup artifact. They are not one transactionally consistent snapshot across every shard. Recovery restores into a separate empty target with the same version, mode and shard count. The source remains unchanged; the restored target stays closed to application traffic until recovery completes and a user records an inspection.

The release package carries the pinned Altinity controller. Operators must install and verify that controller, storage and eligible worker capacity before creating a database. Sandboxed ClickHouse also requires the dedicated reviewed runtime profile; installing the controller does not configure that runtime. Connections remain private. ClickHouse public endpoints and Cloud public database endpoints are not included, and development tests on nodes within one physical VM do not establish independent-zone or cross-provider availability.

The dashboard now uses browser-valid name patterns for database and related resource forms. Resource detail URLs also follow the loaded resource's project and environment, so a stale browser preference cannot silently rewrite its scope.

The [managed database acceptance record](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.52/docs/managed-database-release-acceptance.md) identifies the exact source, controller and runtime evidence for this release. Publication remains gated on that record; these notes do not claim a pending native, API recovery or rendered UI check has passed.

Install alpha.52 with the published installer:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.52/installer.sh -o installer.sh &&
sudo sh installer.sh --version 0.1.0-alpha.52
```

Upgrade an existing installation with:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.52/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.52
```

Direct upgrades are supported from alpha.50 and alpha.51. Older installations need a supported intermediate release. Each upgrade backs up PostgreSQL and configuration and restarts the management API and dashboard. Retain backups because swapping binaries does not undo database migrations.
