# Encrypted object-storage backups

Hakopod uses the official AWS Go SDK for S3-compatible object storage, the age
library for encryption, and the database's own dump/restore tools. There is one
durable running backup or restore across API processes. The worker runs inside
the Go server; it does not require another daemon.

All backup APIs require an unrestricted administrator. Application deployment
keys cannot read destination metadata, create schedules, or restore databases.
Each manual operation records its actor and idempotency key. Running jobs
recheck current authority and cancellation every two seconds. Scheduled jobs
retain an administrator owner and stop being scheduled if that account loses
administration or is disabled.

## Configure storage and recovery keys

Create an existing bucket in AWS S3 or another compatible service, then create a
destination in the dashboard or `POST /api/v1/backup-destinations`. Configure its
endpoint, region, bucket, prefix and path-style setting. HTTPS is required unless
an administrator explicitly enables HTTP for trusted local storage. Redirects
are refused. Credentials need object read/write/delete and multipart operations
within the chosen prefix; Hakopod does not create production buckets.

Access credentials and the age private identity are encrypted in PostgreSQL with
the persistent authentication encryption key and a destination-specific AEAD
context. Public responses contain a credential reference and age recipient,
never stored secrets. A generated age **recovery key is returned once** during
destination creation. Save it in a separate password manager or restricted file.
An existing age X25519 identity can instead be supplied on creation. Keep the
original authentication encryption key and installer configuration separately,
especially for management-database recovery.

Destination endpoints, buckets, prefixes and encryption identities are immutable
so an edit cannot silently redirect old artifacts. Credential/name changes use
`expected_revision`; create a separate destination for a different location.
The connection test actually writes a random test object, reads and verifies it,
then deletes it. A provider error is reported as failure.

Each successful backup writes an encrypted `.age` object and a small `.age.json`
manifest containing the source, format, byte count, SHA-256 and exact recovery
scope. The manifest allows offline recovery even if the management database is
lost. It contains metadata, not credentials. Object keys use the destination
prefix plus `hakopod/<job-id>.age`.

## Database and management scope

- PostgreSQL: one consistent custom-format logical database dump, without
  original owners/ACLs. Restore uses `pg_restore --single-transaction
  --exit-on-error`. Global roles, tablespaces, server settings, WAL and
  point-in-time recovery are excluded.
- MySQL: one logical database dump with `--single-transaction --quick`, routines,
  events and triggers. Transactional InnoDB data is consistent. Avoid concurrent
  DDL; non-transactional tables need an operator maintenance window. Users,
  grants, global settings, binlogs and point-in-time recovery are excluded.
- Management: a PostgreSQL logical dump of Hakopod's management database,
  including accounts, password hashes, encrypted credential records,
  specifications, job history and audits. It **does not** include the
  authentication encryption key, installer configuration, Kubernetes state or
  Secrets, application databases, persistent-volume contents, images or external
  object data. It is not a complete host image or cluster-disaster recovery kit.

Application adapters support official `postgres`, `mysql`, and
`mysql/mysql-server` images and the database declared by their usual
`POSTGRES_DB`/`POSTGRES_USER` or `MYSQL_DATABASE` variables. They execute inside
the existing, uniquely owned running database pod with TTY disabled, preserving
binary data. Exact namespace ownership and pod UID are checked again before
execution. PostgreSQL uses the configured PostgreSQL user/password; MySQL dumps
use its configured user/password, and fresh-database restore requires the
configured root password. Missing tools, privileges, credentials or a changed
pod fail the job; no fixture result is substituted.

For installer-managed PostgreSQL, `HAKOPOD_MANAGED_POSTGRES=true` uses the exact
installer-pinned PostgreSQL client inside the owned `hakopod-system/postgres`
deployment. Namespace/deployment/ReplicaSet/pod ownership and UIDs are verified.
This flag is only for the database created by the installer.

External management PostgreSQL uses `HAKOPOD_PG_DUMP_PATH` (default `pg_dump`).
Supply a compatible official PostgreSQL client: its major version must be at
least the server major. `HAKOPOD_DATABASE_URL` must be a PostgreSQL URL. Connection
and TLS settings are resolved into libpq environment variables; passwords never
appear in command arguments. Use a compatible restore client/server for the
resulting archive. Hakopod does not install tools on the operator's Mac.

## Restore review and execution

`POST /backup-artifacts/{id}/restore-plan` accepts a target application/service
with the same engine. It returns a review valid for ten minutes, its exact
application revision and pod UID, the scope and warnings, and a generated
`hp_restore_<id>` database name. Confirm that exact name to queue the restore.
Reviews belong to their initiating administrator and are single-use, with
idempotent acceptance retries. An accepted review becomes a receipt retained
with its job until the completed job's 90-day retention ends. The exact same
administrator, review, artifact, confirmation and idempotency key returns that
original job even after the review expires or the artifact is deleted. A
different request/key cannot reuse an expired or used review, and retrying a
pruned receipt never starts a new restore.

The worker downloads the encrypted object to a private staging file, verifies
its byte count and SHA-256, then verifies every authenticated age frame before
running any SQL. It creates a **new** database in a separate command before
streaming the dump; an existing name is refused without sending SQL input.
Application connection settings remain unchanged. A failed or interrupted
restore may leave the new database partial. Hakopod neither drops it
automatically nor replays a started restore after a crash. Inspect it and create
a new review. PostgreSQL data/schema restore is transactional after database
creation; MySQL schema/DDL restoration is not transactional.

Restore only trusted logical archives: database definitions may execute code
with the restore user's privileges. Select a separate database service when
recovering across trust boundaries. The fresh database name protects against
accidental name collisions; database-engine permissions govern SQL effects.

Management restores also target a fresh, separate PostgreSQL database. They
never replace the active management database or switch the running server.
Offline recovery must deliberately reconnect a compatible Hakopod installation
using the preserved authentication encryption key and appropriate cluster and
configuration. Before reconnecting, inspect pending deployments/builds/backups,
disable schedules and review restored sessions/credentials; a historical dump
can contain work and credentials that were later cancelled or revoked.

For offline recovery, download the encrypted object and its manifest using the
provider's official client. Compare the object's SHA-256 with the manifest,
decrypt with `age --decrypt --identity /private/recovery-key.txt` into a private
file, and wait for age to succeed before invoking a restore tool. Use `umask 077`
and a new empty database. Keep the plaintext file protected and remove it after
recovery. Do not pipe an unverified encrypted stream directly into a database.

## Scheduling, retention and resource bounds

Schedules use an interval of 1–8760 hours and retain 1–100 successful artifacts.
Updating a schedule resets its next run from the update time. Missed intervals
coalesce into one run after downtime, and an active schedule never overlaps
itself. Failed backups do not replace retained successful backups. Retention
deletes only recorded artifacts from the same schedule and destination prefix;
an active restore protects its artifact. Deletion is durably marked before
storage calls, preventing a racing restore from being accepted; a failed
deletion remains pending and the worker retries it. Manual artifacts persist until an
administrator explicitly deletes them. Deleting a schedule leaves its artifacts
available. Destination deletion is refused while artifacts, schedules or active
jobs still reference it.

Use an S3 bucket lifecycle rule to abort incomplete multipart uploads after a
short period. A process crash can leave an unfinished upload or an object whose
database acknowledgement was lost; its job ID/manifest identifies it for
operator inspection. On versioned buckets, deletion creates a delete marker;
provider lifecycle policies govern historical versions and billing.

Uploads reuse a single 8 MiB part buffer and send parts sequentially. Encryption
and SQL data stream through bounded pipes. There is no full-dump RAM buffer and
no permanent S3 client cache. A job has a 30-minute total deadline, a renewable
30-second lease, and a default 8 GiB encrypted-object limit (configurable by
trusted embedding up to 64 GiB). Restore needs disk space for the encrypted
artifact plus a 512 MiB reserve in the private 0700
`HAKOPOD_BACKUP_STATE_DIR`. Staging is removed after completed/failed requests;
after process loss, an operator may remove leftover encrypted staging files
when no restore is running. The installer grants write access only to this
directory for the API service.

The queue holds at most 64 pending/running jobs. Destinations are capped at 32,
schedules at 64, discovered targets at 128, and history pages at 100 with cursors.
Completed job rows are pruned after 90 days; successful artifact manifests remain
until retention or explicit deletion. These bounds do not describe total
database-server or object-store memory use.

## Verification

Unit tests cover credential encryption/context binding, age round trips,
checksum/authentication refusal before target mutation, failed dump rejection,
sequential multipart uploads and aborts. The PostgreSQL integration test verifies
durable/idempotent acceptance, authorization, one global worker slot, crash
reporting, coalesced scheduling, retention/deletion fencing and exact accepted
restore retries after expiry/deletion. It also verifies rejection of changed
requests and cleanup of accepted receipts with the job's 90-day retention.

The opt-in real test requires `HAKOPOD_BACKUP_TEST=1`,
`HAKOPOD_TEST_DATABASE_URL`, and `HAKOPOD_TEST_KUBECONFIG` whose current context
must be `k3d-hakopod-dev`. Run `go test -p 2 -timeout 15m -run
TestLiveEncryptedDatabaseBackupsAndRestores -v ./internal/api`. It creates a
labelled disposable MinIO container and owned PostgreSQL/MySQL namespaces,
tests real encrypted objects and restores, then removes only those fixtures.
It never restores an operator database. Public AWS S3/other providers, large
production datasets, cross-version migration and full host recovery need their
own operator acceptance tests.

Development acceptance passed on `k3d-hakopod-dev`: a PostgreSQL encrypted
object of 11,536,709 bytes exercised multipart upload and restored 1,200 rows;
a 2,186-byte MySQL object restored two rows. External management PostgreSQL
backup/restoration preserved the fixture account in a separate database.
Original databases remained unchanged. Expired reviews, replaced pod UIDs,
existing database names and a corrupted S3 object were refused; encrypted
staging and disposable cluster/object-store fixtures were cleaned. The complete
live test passed in 26.29 seconds. Installer-managed ownership/fingerprint
checks have unit coverage; installer-managed dump execution and full systemd
host recovery have not been tested end to end.

References: [PostgreSQL pg_dump](https://www.postgresql.org/docs/current/app-pgdump.html),
[MySQL mysqldump](https://dev.mysql.com/doc/refman/8.4/en/mysqldump.html),
[S3 multipart](https://docs.aws.amazon.com/AmazonS3/latest/userguide/mpuoverview.html),
[age](https://filippo.io/age).
