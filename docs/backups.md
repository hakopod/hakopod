# Encrypted object-storage backups

Hakopod uses the official AWS Go SDK for S3-compatible object storage, the age
library for encryption, and the database's own dump/restore tools. There is one
durable running backup or restore across API processes. The worker runs inside
the Go server; it does not require another daemon. ClickHouse is the exception to
most of this page: the ClickHouse server writes its own backup straight to object
storage and Hakopod never sees the bytes. Read
[Backups the database engine performs](#backups-the-database-engine-performs)
before relying on anything below for ClickHouse.

Installation administrators can manage installation backups. Credentials scoped
to a project and environment with deployment write permission can manage that
workspace's database backups. Application-only credentials cannot manage backups.
Destinations, schedules, jobs and artifacts are filtered before pagination;
workspace credentials cannot access the management database or another workspace.
Each operation records its actor and original scope. Running jobs recheck current
membership and cancellation every two seconds. Schedules stop when their owner's
current permission is revoked. Trusted Cloud embedding also rechecks workspace
access, including custom-role permissions.

## Configure storage and recovery keys

Create an existing bucket in AWS S3 or another compatible service, then create a
destination in the dashboard or `POST /api/v1/backup-destinations`. Configure its
endpoint, region, bucket, prefix and path-style setting. HTTPS is required unless
an administrator explicitly enables HTTP for trusted local storage. Redirects
are refused. Workspace destinations require public HTTPS on port 443; every
connection resolves DNS and dials the validated address, blocking private,
metadata and operator-configured networks without using environment proxies.
Trusted embedding can set `BackupConfig.BlockedEndpointCIDRs` for additional
public infrastructure addresses. Credentials need object read/write/delete and multipart operations
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

Each successful logical dump writes an encrypted `.age` object and a small
`.age.json` manifest containing the source, format, byte count, SHA-256 and exact
recovery scope. A backup the database engine performs itself writes neither: it
writes the engine's own tree of objects under a prefix. The manifest allows offline recovery even if the management database is
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
- ClickHouse: no logical dump. The ClickHouse server runs `BACKUP DATABASE` to
  the destination itself and Hakopod polls `system.backups`. It covers the tables
  ClickHouse includes in that database backup; server users, grants, settings and
  other databases are excluded. See
  [Backups the database engine performs](#backups-the-database-engine-performs).
- Management: a PostgreSQL logical dump of Hakopod's management database,
  including accounts, password hashes, encrypted credential records,
  specifications, job history and audits. It **does not** include the
  authentication encryption key, installer configuration, Kubernetes state or
  Secrets, application databases, persistent-volume contents, images or external
  object data. It is not a complete host image or cluster-disaster recovery kit.

The logical dump adapters support official `postgres`, `mysql`, and
`mysql/mysql-server` images and the database declared by their usual
`POSTGRES_DB`/`POSTGRES_USER` or `MYSQL_DATABASE` variables. Official
`clickhouse/clickhouse-server` images are also recognised, but they take the
engine-performed path described below rather than a dump. They execute inside
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

## Backups the database engine performs

Some database engines back themselves up. ClickHouse is the one implemented here.
There is no snapshot-consistent logical dump to stream, so instead of moving bytes
Hakopod issues `BACKUP DATABASE ... TO S3(...)` inside the owned ClickHouse pod
and then polls `system.backups` until the server reports a terminal status. The
ClickHouse server writes the objects directly to the destination bucket. **The
bytes never pass through this server.** Restore is the same shape: Hakopod issues
`RESTORE DATABASE ... AS ...` and polls.

The statement is written to the client's standard input, never into a command
line. Every element of an exec command becomes a repeated `command` query
parameter on the Kubernetes exec URL, which kube-apiserver audit and any proxy in
front of it record verbatim, and the statement carries the destination's access
key and secret. Failure text is written by Hakopod rather than repeated from the
engine, for the same reason: a database exception echoes the statement that
caused it, and a job's error is served by the API and rendered in a browser.

This requires a ClickHouse whose native protocol is reachable on port 9000 inside
its own pod, because `clickhouse-client` speaks only that protocol. A
blueprint-provisioned ClickHouse currently exposes no native listener, so
engine-performed backups do not work against one. A fix is proposed upstream in
the templates repository.

### What the bounds are

The size and time bounds in
[Scheduling, retention and resource bounds](#scheduling-retention-and-resource-bounds)
do not apply, because nothing streams through Hakopod: there is no staging file,
no free-space reserve, no multipart part buffer, no encrypted-object size limit
and no 30-minute streaming deadline. A job waiting on the engine parks in the
`queued` status carrying the engine's own reference for the work, so it holds no
streaming slot and does not block other backups, restores or workspaces. Polling
re-claims it.

The bound that does apply is elapsed time: **two hours** measured from when the
job first started, across every re-claim. Past that Hakopod stops waiting and
records a failure that says plainly the engine may still be running and may have
written objects under the backup prefix.

Two hours is deliberately generous. A retryable object-store error keeps
ClickHouse at `CREATING_BACKUP` for roughly **40 to 80 minutes** before it reports
a failure, because it retries 500 times at five-second intervals. A tighter bound
would declare failure on a backup that is still making progress. The practical
consequence for monitoring: a stuck or doomed backup looks healthy for over half
an hour, so alert on the **absence of a success** within your expected window
rather than on a failure status.

### What Hakopod confirms, and what it does not

A logical dump earns its result three ways: Hakopod counts the bytes, computes a
SHA-256 over the encrypted object, and authenticates every age frame before any
SQL runs. An engine-performed backup has none of those. A terminal success is the
engine's own word.

The one piece of independent evidence is taken before the artifact is recorded:
Hakopod lists the destination prefix itself and refuses to record an artifact when
the listing is empty or holds fewer objects than the engine said it wrote. That
gate is the whole of the confirmation. Hakopod does not read, decrypt or checksum
the contents, then or at restore time. The artifact therefore carries an empty
`sha256`, a `format` beginning `engine:`, a byte count taken from the engine's own
report, and an `object_key` that is a prefix rather than a single object. The
restore review states all of this in its own warnings, per kind.

### These objects are not encrypted by Hakopod

Age encryption happens in the streaming path, which this kind does not use. The
ClickHouse server writes its files to the bucket in whatever form it writes them,
and **the destination's recovery key does not cover them.** There is no `.age`
object and no manifest to verify offline. Encrypting these objects at rest is the
operator's responsibility: enable bucket-side encryption (SSE-S3, SSE-KMS or the
provider's equivalent) on the destination bucket, and treat the bucket's access
policy as the only thing protecting the data.

### Cancellation cannot stop the engine

Cancelling a waiting backup stops Hakopod polling and deletes what was written
under the prefix, so a cancelled backup does not leave a tree of objects that no
artifact row will ever clean up. But the engine interface has no abort: **the
engine may still be writing.** If the deletion cannot confirm the prefix is empty,
the job says so and the objects need operator cleanup. Cancelling a restore
deletes nothing, because those objects are the backup itself; the engine may
already have written into the new database, which is then the operator's to
inspect and drop.

## Restore review and execution

`POST /backup-artifacts/{id}/restore-plan` accepts a target application/service
with the same engine. It returns a review valid for ten minutes, its exact
application revision and pod UID, the scope and warnings, and a generated
`hp_restore_<id>` database name. Confirm that exact name to queue the restore.
Reviews belong to their initiating actor and are single-use, with
idempotent acceptance retries. An accepted review becomes a receipt retained
with its job until the completed job's 90-day retention ends. The exact same
administrator, review, artifact, confirmation and idempotency key returns that
original job even after the review expires or the artifact is deleted. A
different request/key cannot reuse an expired or used review, and retrying a
pruned receipt never starts a new restore.

For a logical dump the worker downloads the encrypted object to a private staging
file, verifies its byte count and SHA-256, then verifies every authenticated age
frame before running any SQL. None of those three checks exist for a backup the
database engine performed, and the restore review says so per kind. It creates a **new** database in a separate command before
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
authorized workspace writer or installation administrator explicitly deletes them. Deleting a schedule leaves its artifacts
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
trusted embedding up to 64 GiB). None of the bounds in this paragraph apply to a
backup the database engine performs itself, which streams nothing through this
server and is bounded by elapsed time instead. Restore needs disk space for the encrypted
artifact plus a 512 MiB reserve in the private 0700
`HAKOPOD_BACKUP_STATE_DIR`. Staging is removed after completed/failed requests;
after process loss, an operator may remove leftover encrypted staging files
when no restore is running. The installer grants write access only to this
directory for the API service.

The queue holds at most 64 pending/running jobs. Destinations are capped at 32,
schedules at 64, discovered targets at 128, and history pages at 100 with cursors.
Workspace limits are four destinations, eight schedules and four queued/running
jobs, within these installation-wide bounds. A full workspace queue does not
block another workspace's schedules. Retained artifacts can restore into a new
owned application after their original application has been deleted.
Completed job rows are pruned after 90 days; successful artifact manifests remain
until retention or explicit deletion. These bounds do not describe total
database-server or object-store memory use.

## Verification

For the logical dump path, unit tests cover credential encryption/context
binding, age round trips, checksum/authentication refusal before target mutation,
failed dump rejection, sequential multipart uploads and aborts. The PostgreSQL integration test verifies
durable/idempotent acceptance, authorization, one global worker slot, crash
reporting, coalesced scheduling, retention/deletion fencing and exact accepted
restore retries after expiry/deletion. It also verifies rejection of changed
requests and cleanup of accepted receipts with the job's 90-day retention.

Workspace integration tests additionally exercise cross-scope destination,
artifact, schedule and restore-target rejection, revoked scheduled authority,
and restore after the original application is deleted. They use real isolated
PostgreSQL schemas with synthetic database-runtime responses; real database
execution is covered separately by the live acceptance below.

The opt-in real test requires `HAKOPOD_BACKUP_TEST=1`,
`HAKOPOD_TEST_DATABASE_URL`, and `HAKOPOD_TEST_KUBECONFIG` whose current context
must be `k3d-hakopod-dev`. Run `go test -p 2 -timeout 15m -run
TestLiveEncryptedDatabaseBackupsAndRestores -v ./internal/api`. It creates a
labelled disposable MinIO container and owned PostgreSQL/MySQL namespaces,
tests real encrypted objects and restores, then removes only those fixtures.
It never restores an operator database. Public AWS S3/other providers, large
production datasets, cross-version migration and full host recovery need their
own operator acceptance tests.

Earlier development acceptance passed on `k3d-hakopod-dev`: a PostgreSQL encrypted
object of 11,536,709 bytes exercised multipart upload and restored 1,200 rows;
a 2,186-byte MySQL object restored two rows. External management PostgreSQL
backup/restoration preserved the fixture account in a separate database.
Original databases remained unchanged. Expired reviews, replaced pod UIDs,
existing database names and a corrupted S3 object were refused; encrypted
staging and disposable cluster/object-store fixtures were cleaned. The complete
live test passed in 26.29 seconds. Installer-managed ownership/fingerprint
checks have unit coverage; installer-managed dump execution and full systemd
host recovery have not been tested end to end.

Engine-performed ClickHouse backups have unit coverage for parking and polling,
the prefix-listing gate before an artifact is recorded (removing it fails three
tests), prefix deletion, status parsing including unrecognised and missing rows,
and a test that fails if the statement is moved back into argv. Against a real
ClickHouse server it was verified directly that `BACKUP ... TO S3(...)` is
accepted with inline credentials and no server configuration change, that `ASYNC`
returns an id immediately, that `RESTORE ... AS ...` restores with matching row
counts and full-row hashes, and that the 40 to 80 minute retry window above is the
measured behaviour. **Not verified:** any of this at a hundred gigabytes, and the
whole path end to end inside Hakopod on a cluster. There is no live acceptance
test for ClickHouse yet, and the missing native listener on a
blueprint-provisioned ClickHouse is why.

References: [PostgreSQL pg_dump](https://www.postgresql.org/docs/current/app-pgdump.html),
[MySQL mysqldump](https://dev.mysql.com/doc/refman/8.4/en/mysqldump.html),
[S3 multipart](https://docs.aws.amazon.com/AmazonS3/latest/userguide/mpuoverview.html),
[age](https://filippo.io/age).
