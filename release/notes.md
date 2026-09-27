Hakopod 0.1.0-alpha.37 adds managed PostgreSQL and Redis with their own resources, credentials, operation progress and lifecycle (#120).

## Managed databases

- Run PostgreSQL 17 or 18 standalone or with replicas. Private read/write and read-only endpoints connect applications; the database controller owns replication and primary recovery independently of application replicas.
- Run Redis 8 standalone or as a cluster with shards and replicas. Reviewed shard growth and reduction require a verified backup from the last hour and healthy cluster topology and slot ownership. Redis Cluster requires a cluster-aware client.
- Back up to an eligible S3 destination manually, hourly or daily. Archives are encrypted, checksummed and read back before being marked verified. Redis backups preserve values and expiry with per-shard consistency.
- Restore into a separate database, inspect the data, then explicitly replace the application's saved connection and redeploy. Import eligible Docker PostgreSQL or Redis archives, or stage a PostgreSQL 17-to-18 logical copy, while the source remains available. Changes after the captured recovery point require a fresh capture before final cutover.
- Use the same scoped workflows through the dashboard, CLI and API. Database deletion reclaims owned storage before releasing reservations and allowing name reuse.

## Installation and upgrade

```sh
sudo sh installer.sh --upgrade --version 0.1.0-alpha.37
```

Direct upgrades are supported from alpha.35 and alpha.36. Older installations must use a supported intermediate release. This release adds management database migrations 046–050 for database resources, archives, imports, allocations and reusable names. Take and verify an installation backup before upgrading; a binary rollback does not undo schema or data changes.

Database provisioning needs a separate controller rollout. PostgreSQL requires CloudNativePG 1.30.1. Redis requires a digest-pinned controller built from upstream commit `c5017206e75f7743d79e82db47ec8c39d7410816`, which fixes credential exposure in command handling, with the documented 20-minute command deadline. The older released Redis controller is rejected. The source includes its reproducible build recipe; this release does not publish a controller image or install controllers automatically.

Read the [managed database guide](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.37/docs/managed-databases.md) before provisioning. The controller installation script is for the named development cluster only. Cloud requires its corresponding integration and a separate operator rollout; this OSS release does not enable hosted databases.

## Validation and limits

The feature passed the complete Go suite against isolated PostgreSQL, Go vet, the dashboard build, TypeScript checks and 93 dashboard tests. Independent rendered UI review covered both themes, desktop and mobile, keyboard/touch use and failure handling. Real development-cluster checks covered PostgreSQL replication and primary recovery, private bindings, same-major and 17-to-18 restores, Redis recovery and 3-to-4-to-3 shard resizing with retained values and expiry, corrupt archive rejection, and owned-volume reclamation. See the [validation record](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.37/docs/managed-databases-validation.md).

Publication is gated on fresh release source checks, packaged smoke tests and native fresh-install and upgrade acceptance on amd64 and arm64. The Redis controller was built for both architectures; its runtime acceptance was on amd64. Development tests do not establish production availability, multi-node resilience or hosted LVM byte-limit enforcement. Replicas on one worker cannot survive loss of that worker, VM or shared disk. This remains an alpha release for evaluation.
