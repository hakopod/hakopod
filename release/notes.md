Hakopod 0.1.0-alpha.55 adds self-hosted Managed Vitess for applications that
need an explicit MySQL sharding key. It supports standalone Vitess and fixed
cluster layouts with one, two, four or eight shards and up to five replicas per
shard. Applications connect privately through vtgate with verified TLS and
select `app@primary` or `app@replica` explicitly.

The native qualification covers lifecycle, recovery, replica reseeding, backup
approval revocation and the maximum supported topology. Anonymous image
verification passed against the recorded binary checksums. The official HTTP
workflow passed creation, encrypted backup, separate-target restore, inspection,
verified application access and scoped deletion with complete fixture cleanup.

The service runs Vitess 23.0.6 with MySQL 8.4.6, Vitess Operator 2.16.0 and a
three-member etcd topology. Hakopod owns authorization and durable operations.
A namespace-scoped operator reconciles each database, and applications never
receive Kubernetes credentials.

Creation requires a dedicated, operator-approved native backup destination.
Native backups seed replacement tablets. Downloadable logical archives are
separate and restore into an empty compatible target while application access
remains closed for inspection. Removing backup approval stops approved backup
work; revoke the provider key separately when it must become unusable.

Vitess requires Linux amd64 workers with the fixed capacity described in the
Managed Vitess guide. ARM64 is not qualified. Dedicated public endpoints,
online resharding, in-place capacity changes, automatic SQL read/write
splitting, cross-shard transactions and arbitrary custom vindexes remain
outside this release contract.

The core install or upgrade does not install a missing Vitess controller. After
upgrading, use the matching extracted release kit and follow the reviewed plan
and apply procedure in the
[installer guide](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.55/installer/README.md#managed-database-controllers).
Creation remains closed until the installation has approved workers, enough
capacity and the database's dedicated native backup destination.

Alpha.55 retains the Outpost webhook delivery template and its verified scope
from [alpha.54](https://github.com/hakopod/hakopod/releases/tag/v0.1.0-alpha.54).
See the [runtime acceptance record](https://github.com/hakopod/hakopod/blob/main/docs/outpost-template-runtime.md)
for its dependency combinations, limits and evidence.

Install alpha.55 with the published installer:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.55/installer.sh -o installer.sh &&
sudo sh installer.sh --version 0.1.0-alpha.55
```

Upgrade an existing installation with:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.55/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.55
```

Direct upgrades are supported from alpha.53 and alpha.54. Older installations
need a supported intermediate release. Each upgrade backs up PostgreSQL and
configuration and restarts the management API and dashboard. Retain backups
because replacing binaries does not reverse database migrations.

This OSS release does not establish Hakopod Cloud availability. Cloud requires
a separate package, pinned engine revision, controller rollout, qualified
workers, capacity approval and production verification.
