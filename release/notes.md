Hakopod 0.1.0-alpha.50 adds private managed MySQL 8.4 with a guided create flow, InnoDB Cluster and MySQL Router.

Choose a standalone database or a cluster with three, five or seven voting members. Clustered databases expose separate primary and replica routes. Clients verify the database hostname and issued CA; application credentials have access to the `app` database and stay separate from administrative credentials.

The database view shows observed members, Router instances, connected applications and available resource statistics. Logical backups hold a global read lock while capturing data. Restore into a separate empty database, inspect the result, then switch the application's connection. This release does not add point-in-time recovery or public MySQL endpoints.

Replica changes have an explicit review showing current and proposed counts. Per-member resources and placement stay fixed after creation. Removing replicas retains earlier CPU and storage reservations until verified deletion; memory follows the completed layout. Standalone MySQL and MongoDB now show their capacity policy instead of an unusable resize form.

A failed MySQL or MongoDB replica change can be reviewed and retried from Activity, the API, CLI or SDK. Each retry keeps the requested configuration revision and creates a separate operation, preserving the earlier failure. Hakopod checks whether the controller already accepted the change or still has the previous healthy layout. The retry cannot bypass native readiness or change the requested replica count.

The installer includes the digest-pinned MySQL controller. Operators install it through the managed-databases module's plan and apply workflow. MySQL requires compatible AMD64 workers, storage, capacity and the documented runtime settings. Updating Hakopod alone does not install a missing controller.

ClickHouse, Vitess and Oracle Database remain unavailable while their qualification is completed separately. Neon and Supabase remain deferred. Physical multi-zone and cross-provider resilience require independently placed infrastructure; tests on nodes sharing a VM do not establish that resilience.

The managed MySQL guide explains the stack, connection setup, recovery behavior and operating limits. The managed-database acceptance record identifies the exact tested source and runtime images.

## Installation and upgrade

```sh
curl --fail --location --proto '=https' --tlsv1.2 \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.50/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.50
```

Direct upgrades are supported from alpha.48 and alpha.49. Older installations need a supported intermediate release. Keep the upgrade's PostgreSQL and configuration backups: replacing a binary does not undo database migrations.

Cloud packages are released separately with the matching engine. Existing self-hosted installations upgrade through their operator.
