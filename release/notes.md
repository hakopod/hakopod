Hakopod 0.1.0-alpha.23 adds workspace-scoped encrypted database backups and trusted persistent-storage admission for hosted runtimes.

## Database backups

Credentials scoped to a project and environment with deployment write access can configure PostgreSQL/MySQL backup destinations, schedules and reviewed restores. Destination credentials stay encrypted. Jobs retain their accepted scope and recheck current permissions; workspace users cannot access installation recovery or another workspace. Retained artifacts can restore into a new owned database after the original application has been deleted. Scoped object-store endpoints require public HTTPS and refuse private, metadata and operator-configured networks.

Backups remain Free. Redis persistent storage is supported by its catalog template; Redis backup/restore is not implemented.

## Persistent-storage embedding

Trusted runtimes can force a storage class and enforce transactional workspace storage reservations. Retained volumes continue consuming quota, concurrent admissions cannot oversubscribe the budget, and existing claim sizes are immutable. Template planning accepts a reviewed compute size. Private Cloud provisioning and credentials are not included in public artifacts.

## Upgrade

Supported upgrade sources are alpha.8, alpha.9, alpha.10, alpha.12, alpha.13, alpha.14, alpha.15, alpha.17, alpha.18, alpha.19, alpha.20, alpha.21 and alpha.22. Back up the installation before upgrading. Download this release's installer.sh and run:

```sh
sudo sh installer.sh --upgrade --version 0.1.0-alpha.23
```

Publication does not automatically upgrade existing installations or enable hosted storage.

## Validation

The full PostgreSQL-backed Go suite, dashboard build/typecheck/tests and independent UI review passed. Named development-cluster checks verified sandboxed 1 GiB LVM persistence and allocation bounds; PostgreSQL, MySQL and Redis templates retained data across replacement under the small compute profile. Real encrypted PostgreSQL/MySQL restore checks passed, including corrupted objects, stale pods, existing database names and expired reviews. Publication additionally requires native package smoke and the declared installation/upgrade matrix.
