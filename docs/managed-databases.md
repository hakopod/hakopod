# Managed databases

Managed databases have their own project and environment, resource allocation, credentials, revision history, operation progress and lifecycle. Application replicas do not control database replication. The PostgreSQL and Redis controllers own replication and recovery of failed members.

PostgreSQL and Redis are included in the self-hosted development release starting
with `v0.1.0-alpha.37`. MongoDB joins them in `v0.1.0-alpha.47`, and private
MySQL 8.4 support is included in `v0.1.0-alpha.50`. Private ClickHouse 26.3 joins
them in `v0.1.0-alpha.52`. Private Vitess 23 is an alpha.55 candidate for
Linux amd64 workers. Its five native cases passed on the current source;
the official HTTP run and package checks remain required before publication. Use a published release's
verified assets; a tag or candidate build alone is not an installation package.
Provisioning requires the controller installation described below; upgrading
Hakopod does not install missing controllers. Cloud workspace admission, quotas,
trusted placement and approvals require the corresponding Cloud integration and
a separate operator rollout. This OSS release does not enable hosted provisioning.

The expanded [MySQL](managed-mysql.md), [MongoDB](managed-mongodb.md),
[ClickHouse](managed-clickhouse.md), [Vitess](managed-vitess.md) and [Oracle Database](managed-oracle.md)
implementations have separate guides with their exact development evidence and
remaining limits. Oracle Database remains held. Neon and Supabase are deferred.
MySQL, MongoDB, ClickHouse and Vitess public endpoints remain unavailable.
See the
[release acceptance record](managed-database-release-acceptance.md) for tested
source revisions and immutable runtime references.

## Database configuration

Use `Databases` in the dashboard, or a strict version 1 TOML file with the CLI:

```toml
schema_version = 1
name = "orders"
engine = "postgresql"
version = "17"
mode = "cluster"
replicas = 1
shards = 1
cpu = "500m"
memory = "1Gi"
storage_gib = 10
```

```sh
hakopod database create --project demo --environment production --file database.toml
hakopod database list --project demo --environment production
hakopod database show DATABASE_ID
```

PostgreSQL supports major versions 17 and 18, either standalone or with one to six replicas. Redis 8 supports standalone operation or three to sixteen shards with one or two replicas per shard. CPU, memory and storage apply to each member. A database has at most 48 configured members; an environment has at most 64 databases. Images are pinned by digest.

PostgreSQL exposes private read/write and, when replicas exist, read-only endpoints. Redis Cluster exposes private cluster endpoints and requires a cluster-aware client. Observations come from controller and database health checks; configured member counts are not reported as running members.

## PostgreSQL public endpoints

This section describes the unreleased self-hosted implementation. Native public
endpoint acceptance is still pending. Released PostgreSQL support uses private
endpoints; Cloud public database endpoints remain unavailable.

Self-hosted operators can provision a bounded public listener pool. Set all three endpoint settings together and include every database port in the existing HAProxy TCP port inventory:

```toml
schema_version = 1

[server]
deployment_mode = "self-hosted"
public_tcp_ports = [15432, 15433]
database_public_address = "192.0.2.10"
database_public_domain = "database.example.com"
database_public_ports = [15432, 15433]
```

The operator owns the address, DNS suffix and ports. A project member can choose only a PostgreSQL route, 1–16 explicit IPv4 CIDRs and a connection cap from 1–256. DNS for each deterministic `database-PORT.DOMAIN` name must resolve exclusively to the configured IPv4 address before review and again before publication.

Use the dedicated dashboard page under **Connections & security**, or the CLI review flow:

```sh
hakopod database public-endpoint-list DATABASE_ID
hakopod database public-endpoint-plan DATABASE_ID --purpose read_write --source-cidrs 192.0.2.0/24 --max-connections 32
hakopod database public-endpoint-publish DATABASE_ID --review-id REVIEW_ID --revision DATABASE_REVISION --endpoint-revision ENDPOINT_REVISION --idempotency-key RETRY_KEY
hakopod database public-endpoint-operation OPERATION_ID
hakopod database public-endpoint-revoke DATABASE_ID --public-endpoint-id ENDPOINT_ID --endpoint-revision ENDPOINT_REVISION --idempotency-key RETRY_KEY
```

Every publish or change closes the previous frontend and its existing sessions before adding certificate names, ingress policy and the reviewed route. A revoke closes and acknowledges the frontend before deleting its claim. The operation reports `configured` only after every owned HAProxy worker exposes the exact bind, ACL, connection cap and backend. `externally_verified` remains false until a separate outside-in probe records evidence; configured state alone is not an Internet reachability claim.

External clients need the downloaded public CA, the published hostname and database credentials. The acceptance helper verifies native PostgreSQL TLS, hostname validation, the selected writer or reader role, and optionally that an already-open session is closed during revocation:

```sh
PGPASSWORD='from-a-secure-source' go run ./examples/postgres-public-endpoint-acceptance --host database-15432.database.example.com --port 15432 --ca ./database-ca.crt --purpose read_write
PGPASSWORD='from-a-secure-source' go run ./examples/postgres-public-endpoint-acceptance --host database-15432.database.example.com --port 15432 --ca ./database-ca.crt --purpose read_write --expect-revocation-within 2m
```

Run the second command from an allowed external address, wait for `READY`, then revoke the endpoint. Keep the client running across an interrupted worker retry and verify that it prints `REVOKED`. Also exercise a deliberately unavailable HAProxy master socket and a missing TCP CRD; neither case may release the port claim or report revocation success.

## Member placement

New dashboard clusters default to one member per node. Existing specifications
without placement retain their scheduler defaults. To select placement in TOML:

```toml
[placement]
spread = "nodes"
node_names = ["worker-a", "worker-b"]
```

`node_names` is optional and restricts the eligible nodes. Hosted allocation
restrictions still apply. `spread = "nodes"` requires a separate node for every
member; `spread = "zones"` requires a separate labeled zone for every member.
For example, a two-member PostgreSQL cluster requires two zones; a six-member
Redis cluster requires six zones with the strict zone policy. The scheduler
leaves members pending when it cannot maintain separation. Admission checks
ready nodes and domain labels; resource capacity, taints, storage availability
and later node failures can still prevent scheduling.

Placement is immutable. Use a separate recovery target to move data, especially
with node-local volumes. Increasing member count rechecks placement capacity.
Zone labels come from `topology.kubernetes.io/zone`; region labels come from
`topology.kubernetes.io/region`. Missing metadata is shown as not reported.

Nodes from different providers may participate in one connected Kubernetes
cluster when its operator supplies private connectivity, compatible storage,
appropriate network encryption and reliable control-plane access. Independent
Kubernetes clusters are not managed as one database. Provider and zone counts
describe observed placement; they do not certify network, storage or quorum
availability. PostgreSQL replication is asynchronous by default and may lose
recent writes after a primary failure. Redis Cluster requires a majority of
primaries and reachable replicas for automatic recovery.

## Database cockpit

The catalog identifies engines with their icons. The detail page shows observed
member roles, private endpoints, placement, resource samples, recent operations
and backup records. The isometric topology supports member selection by pointer
and keyboard; Redis groups show observed shard identities.

CPU and memory come from bounded metrics-server or kubelet requests, matched to
owned member pods. Missing, incomplete, stale or previous-revision observations
are not displayed as current usage. The Monitoring tab includes persisted source samples for 1-hour, 6-hour and
24-hour ranges. Native activity counters and PostgreSQL replication lag appear
when the engine supplies them. Query latency and traffic rates are not
collected; replication lines do not represent measured traffic.

## PostgreSQL connection pooling

Optional managed PgBouncer exposes separate pooled write and replica endpoints.
Add this table to a PostgreSQL specification with required TLS:

```toml
[pooling]
mode = "session"
instances = 2
max_client_connections = 200
default_pool_size = 10
read_only = true
```

`instances` is 1–3 **per route**. A write route is always created; `read_only`
adds a second route and requires a replica. The example allocates four poolers,
each with 250m CPU and 256Mi memory. Capacity admission also reserves 50Mi
runtime overhead per pooler and one replacement instance per route. Database
member monitoring excludes poolers; creation totals include their requested
compute. The topology shows only observed pooler instances.

Session mode retains a server connection for the client's session. Transaction
mode releases it after each transaction and requires compatible clients: do not
rely on session variables, `LISTEN`, or temporary tables across transactions.
Use a direct or session endpoint for those operations. The pool limits each
instance to 20–2000 clients and 1–20 server connections per database/user.
The pooling policy is immutable after creation.

Use `pooled_read_write` or `pooled_read_only` when reviewing a managed binding.
Direct `read_write` and `read_only` endpoints remain available. PgBouncer does
not inspect SQL to choose a route; applications must select one. Replica reads
can lag. Endpoints are routing choices, not read-only permission roles. Existing
sessions must reconnect after failover, including sessions on a replica that is
promoted. Idle server connections close after 10 seconds; reusable server
connections retire after 60 seconds when released. Active sessions and
transactions can live longer. Clients must implement bounded retries and avoid
blindly replaying transactions whose commit outcome is unknown.

Clients require TLS and verify the pooler hostname against the database CA.
Poolers verify the backend certificate and hostname. Certificate SANs include
all configured pooler service names. Health checks verify both TLS and the
observed primary/replica role through every endpoint. Strict placement spreads
poolers for each route across distinct nodes or zones; poolers may share those
locations with database members. Admission needs the larger of the database
member count and poolers per route.

The named development cluster passed both pooling modes with required node
separation, primary and pooler replacement, replica write rejection, certificate
renewal, application trust rollout and access revocation. A separate recovery
target passed authenticated backup/restore and retained source checks. These
checks use two Kubernetes nodes on one physical VM; they do not establish
physical zone/provider resilience or a throughput guarantee.

## Application connections

The database detail page can review a connection replacement, save it to the application specification and queue a redeployment in one transaction. It requires the application's exact name as confirmation. A review expires after ten minutes and becomes invalid when either revision or the recovery evidence changes.

```sh
hakopod database connection-plan DATABASE_ID --application-id APP_ID --service api --variable DATABASE_URL --endpoint read_write
hakopod database connect DATABASE_ID --review-id REVIEW_ID --name orders-app
```

Redis Cluster connections require `--endpoint cluster --cluster-aware`. PostgreSQL replica connections use `--endpoint read_only`.

The saved application specification contains a reference, not a password:

```toml
[services.api.bindings.DATABASE_URL]
managed_database = "DATABASE_ID"
protocol = "postgres"
endpoint = "read_write"
```

At deployment time, the worker verifies live database health and resolves application credentials into that service's environment Secret. Namespace and service network rules permit the connection. Removing a binding removes its grant. Application-scoped keys can redeploy an unchanged, previously authorized binding; adding or changing a binding requires project deployment permission. Referenced databases cannot be deleted.

## TLS and client trust

New database specifications default to `[tls] mode = "required"`. A stored database
without that policy keeps its legacy behavior; changing TLS policy during resize
is rejected. Recover into a secure target, inspect it, and review application
cutover. Certificate presence alone never marks a connection verified.

Connections & security shows the issued certificate identity and the last
runtime verification. Verification checks the CA, endpoint hostname, validity,
served fingerprint, protocol floor and plaintext rejection. An expired or stale
observation is never shown as currently verified. The public trust API requires
the database's project scope and returns only public CA material:

```sh
hakopod database trust DATABASE_ID
```

The same operation is available at `GET /api/v1/databases/{id}/trust` and through
`client.database(id).trust()` in the TypeScript SDK. No private issuer or server
key is returned. Save the `certificate_pem` field as a CA file for an external
client; do not use a certificate from an unrelated database.

Managed application bindings mount each bound database's public CA at
`/var/run/secrets/hakopod-database/DATABASE_ID.crt`. This is a read-only,
service-scoped ConfigMap containing no private key. Unbound services receive no
trust mount. PostgreSQL connection URLs set `sslmode=verify-full` and the mounted
`sslrootcert` path. External PostgreSQL clients must set that path to their own
downloaded CA file and connect to the endpoint hostname.

Redis bindings use `rediss://`. Redis drivers do not share a universal CA URI
option: configure the driver to read the mounted CA explicitly. For example,
Python redis-py uses `Redis.from_url(url, ssl_ca_certs=ca_path,
ssl_cert_reqs="required", ssl_check_hostname=True)`. Cluster clients must also
verify the advertised member names and reach all members. Never disable
verification to make a private CA connection work.

PostgreSQL certificate issuance and renewal use CloudNativePG. Redis uses one
private issuer per database and rotates its 30-day server identity seven days
before expiry. Its CA renews 30 days before its one-year expiry while retaining
the signing key for client overlap. CA key replacement requires a separate target
and reviewed cutover. Redis reloads the projected certificate through its
restricted local socket; no plaintext TCP listener is opened for maintenance.
Maintenance claims fence renewal against lifecycle, backup and restore operations.
Bound deployments renew immutable trust mounts through application maintenance;
CronJobs update future runs, while already-created one-off Jobs retain their
original trust snapshot.

PostgreSQL TLS, renewal, application trust rollout and recovery have passed the
named development-cluster tests. Redis standalone TLS, renewal and recovery,
and six-member cluster TLS enforcement, renewal, primary replacement and
separate-target recovery have also passed development acceptance. These
checks do not establish availability through loss of a physical zone or provider.
See the validation record for the exact tested source and coverage.

## Guided creation

The creation page separates Engine, Topology, Resources, Security and Review.
Engine cards identify the database using its logo and data model. The deployment
summary shows the requested member layout and total CPU, memory and storage;
it does not display those members as running before creation. You can return to
earlier steps without losing entered values. A failed request keeps the reviewed
configuration and its retry key until you change the configuration.

Alpha.50 supports guided creation for PostgreSQL, Redis, MySQL and MongoDB;
alpha.52 adds ClickHouse. The alpha.55 candidate adds guided standalone and
sharded Vitess creation with a scoped native backup destination and explicit
table routing.
MySQL, ClickHouse and Vitess creation require
their pinned controller, a healthy controller rollout, required permissions,
eligible placement and sufficient capacity. Sandboxed ClickHouse also needs
the dedicated verified runtime profile. Vitess also needs an exact operator
approval for the destination revision, project, environment and database name.
Vitess acceptance records bind the exact tested source and image digests. The
current alpha.55 candidate passed native acceptance after rebasing and still
requires the official HTTP workflow and package checks.
Oracle Database remains held.
Its engine guide describes implementation details
separately from released availability.

## Monitoring history

The control plane persists at most one resource and database-statistics sample
per minute for each database and retains 24 hours. Collection continues when
the dashboard is closed. The Monitoring tab offers one-hour, six-hour and
24-hour windows. Missing samples and revision changes break chart lines;
unavailable data is never shown as zero or as a fresh observation. Four bounded
observation workers have durable, expiring claims and run separately from the
lifecycle reconciler. Slow probes cannot occupy the lifecycle lane, and expired
workers cannot publish over a newer claim. This bounds collection work; it does
not guarantee a fresh sample within 30 seconds at every database/member count.

PostgreSQL statistics include application-database sessions, active sessions,
database size, transaction count, cache hit ratio, uptime and the largest
reported replica replay backlog. A missing replay position remains unavailable.
Redis statistics aggregate primary shards: sessions, connection limits, dataset
memory, commands, evictions, rejected connections and the shortest member uptime.
Multi-shard cache ratios are unavailable rather than averaged incorrectly.
These counters reset when database processes restart. Neither query text nor
client identities are collected. Dataset size is not filesystem utilization.

Use `hakopod database metrics DATABASE_ID --range 6h` or
`GET /api/v1/databases/DATABASE_ID/metrics?range=6h` for the same persisted history.
The SDK database resource exposes `.metrics(range)`. History has the same project and
environment access controls as database detail and cannot be enumerated with an
application-only key.

## Resize Redis

Change the shard count in the database TOML, review the plan, and accept that exact plan:

```sh
hakopod database resize-plan DATABASE_ID --file database.toml
hakopod database resize DATABASE_ID --file database.toml --review-id REVIEW_ID --revision REVISION
```

The API checks actual Redis cluster health, complete slot ownership, the observed topology and a verified backup captured within the last hour. Acceptance and reconciliation recheck that evidence. A change cannot proceed using stale health or a backup from another database revision. Redis Cluster clients must handle topology changes.

## Backups and recovery

Configure an eligible S3 destination under Backups. Credentials are encrypted at rest; streamed PostgreSQL and Redis archives are age-encrypted, checksummed and read back for authentication before verification is recorded. Manual, hourly and daily schedules use durable backup jobs. Project-scoped destinations follow the existing endpoint restrictions.

Recovery requires a separate, unused database at revision 1. Choose Recover on that target, select a verified compatible archive, review the recovery point and confirm the target name. The target cannot already have an application connection.

```sh
hakopod database restore-plan TARGET_ID --artifact-id ARTIFACT_ID
hakopod database restore TARGET_ID --artifact-id ARTIFACT_ID --review-id REVIEW_ID --name target-name
```

The archive is completely authenticated before restoration. PostgreSQL restores run in one transaction, authenticated as the application role. Redis archives preserve values and absolute expiry and are consistent per shard, rather than one transaction across the entire cluster. Recovery supports individual Redis values up to 64 MiB; larger values are rejected explicitly. A failed Redis restore can leave the separate target partial; discard it and use a fresh target for another attempt.

Inspect the recovered data before acknowledging inspection. The acknowledgement is tied to the completed recovery job and the target revision:

```sh
hakopod database inspect TARGET_ID --job-id JOB_ID --revision REVISION --name target-name --inspected
```

Inspection does not connect an application. Use the reviewed connection replacement flow afterwards. The source remains available throughout recovery, and changes after the captured recovery point require a fresh capture before final cutover.

The current implementation keeps application ingress closed until the recovery
job records success and inspection is acknowledged, including after a controller
restart. Background reconciliation then restores the private network grant under
a maintenance lease. This gate covers PostgreSQL, Redis, MySQL and MongoDB;
the latest cross-engine hardening has unit coverage, with development-cluster
reverification still pending. It does not publish a public endpoint.

PostgreSQL upgrades use this same sequence: capture PostgreSQL 17, create a separate PostgreSQL 18 target, restore the logical archive, inspect it, then explicitly replace the saved application connection and redeploy. Same-major recovery and 17-to-18 upgrades are supported; downgrades and unknown source versions are rejected.

## Import Docker archives

Export an eligible PostgreSQL 17/18 custom archive with `pg_dump -Fc`, or capture a standalone Redis 8 RDB file. Hakopod imports the supplied file without contacting or modifying the Docker application. The operator supplies the original capture time; this is recorded as an attestation, not inferred from the file modification time.

The dashboard's dedicated import page supports files up to 64 MiB. The CLI streams up to 2 GiB, subject to the server's configured backup size limit:

```sh
hakopod database import-plan --file orders.dump --destination-id DESTINATION_ID --name docker-orders --engine postgresql --source-version 17 --captured-at 2026-09-27T10:00:00Z
hakopod database import IMPORT_ID --file orders.dump --name docker-orders
```

Review preparation calculates the raw file's SHA-256 and size. Upload rejects a different file, unsupported archive header or mismatched source major version. Redis format 12 alone is insufficient: the original `redis-ver` metadata must identify Redis 8. Verified imports become ordinary recovery artifacts. Continue with a separate target, inspection and explicit connection replacement.

Import reviews expire after 30 minutes. The server permits two concurrent uploads and bounds each request to 15 minutes. Upload leases, destination revision checks, current authorization and cleanup of interrupted staging objects prevent a retry from racing an earlier attempt. A completed retry returns the original artifact.

## Controllers and acceptance

The implementation expects CloudNativePG 1.30.1 in `cnpg-system` and the Opstree Redis operator built from upstream commit `c5017206e75f7743d79e82db47ec8c39d7410816` in `redis-operator`. That commit removes passwords from command arguments; the released 0.26.0 image can log them when a command fails and is not accepted. `Dockerfile.redis-controller` verifies the upstream archive checksum, applies the checked-in TLS patch, runs credential and TLS regression tests, and builds from pinned builder and runtime images. The patch removes insecure CLI verification, prevents fallback to plaintext when configured TLS material is missing, and advertises scoped member DNS names for hostname-verifying cluster clients.

Build the controller for the installation's architecture, publish it to the operator's registry, and retain the resulting image digest. For a registry serving both supported architectures:

```sh
docker buildx build --platform linux/amd64,linux/arm64 -f Dockerfile.redis-controller --tag "$REGISTRY/redis-controller:c501720" --metadata-file controller-build.json --push .
```

Use the resulting `repository:tag@sha256:digest` as `HAKOPOD_REDIS_CONTROLLER_IMAGE`. The development installer verifies downloaded manifest/chart checksums and configures a bounded 20-minute controller command timeout. Five minutes was insufficient for a real shard reduction on the bounded development worker. Admission checks the pinned image reference, source annotation, timeout and completed controller rollout before allowing database work:

```sh
HAKOPOD_TEST_KUBECONFIG=/path/to/development-kubeconfig HAKOPOD_REDIS_CONTROLLER_IMAGE="$REDIS_CONTROLLER_IMAGE" scripts/install-development-database-controllers.sh
```

This script only targets `k3d-hakopod-dev`; it must not be used against an operator cluster. Controller installation for other environments requires an explicit operator rollout with the same verified build, `hakopod.io/redis-controller-source` and `hakopod.io/redis-tls-policy=ca-verified-v1` pod annotations, `GenerateConfigInInitContainer=true`, and `EXEC_COMMAND_TIMEOUT=20m`. A supplied source annotation is an installation-operator attestation; customers cannot set it. The release installer uses the published, digest-pinned controller and Redis 8.2.10 runtime. The runtime includes native TLS, upstream license texts and source references.

Validation records distinguish unit/store checks, rendered dashboard review, real development-cluster acceptance and released availability. No production deployment or release is implied by passing development tests.

## Cloud workspaces

Managed databases share the workspace's compute and persistent storage allocation with applications. Admission serializes both kinds of reservation. Memory includes each database member, one extra member-sized working allocation for replacement or recovery, 50 MiB of sandbox overhead per member, another 128 MiB for recovery overhead and 256 MiB of shared application/readiness headroom. The review shows requested resources; quota errors include the required operational headroom. Failed or unfinished application changes keep their previous allocation reserved.

Shrinking Redis does not release storage for retained shard volumes. Confirmed database deletion changes the reclaim policy only for volumes bound to its exact owned claims, then removes the database namespace. Its reservation is released only after those volumes have disappeared; provisioning or storage failures keep deletion pending. Hosted compute cannot be released while databases remain. The single hosted worker supports recovery of failed database processes, but does not provide availability through worker or VM loss.

When workspace approvals are required, create, resize, delete, restore, inspection acknowledgement and connection replacement follow that review policy. Archive import metadata is approved before upload. Open the executed approval and choose **Continue archive upload**, select the exact reviewed file and confirm its checksum. The file itself is streamed without buffering it into an approval. A changed file or metadata requires another review.

Cloud supports raw archive uploads to hosted workspaces and directly reachable HTTPS BYO node APIs. Uploads through the BYO relay are unavailable; use the CLI against that node's direct API. Browser uploads remain limited to 64 MiB, and direct CLI uploads to 2 GiB.

## Reading application connections in the topology

The Overview tab draws applications, private endpoints and observed database
members in separate lanes. Select an application to see its services, binding
variables, endpoint purposes and revision evidence. Select an endpoint to copy
its address, or a member to inspect its role, placement and current resources.
The Monitoring, Connections & security, Backups, Activity and Settings tabs keep
other operations separate from the graph.

Application edges come from `GET /api/v1/databases/{id}/connections`. This endpoint
requires database read access in the database's project and environment; an
application-scoped key cannot use it to enumerate other applications. It returns
only connection metadata, never application environment values or credentials.

A binding can have several kinds of evidence:

- **Saved:** the application configuration currently requests this connection.
- **Last successful deployment:** a completed deployment used this binding.
- **Latest attempt:** a queued, running or unsuccessful deployment contains the
  binding, including its recorded recovery configuration. Older pods may still
  use the previous successful binding after a failed replacement.

These records do not prove that an application currently has an open database
session. Connections supplied manually through environment variables or external
clients cannot be discovered from managed binding records. A binding whose
endpoint is not currently observed receives an explicit notice; its missing
route is not represented as healthy.

The API bounds each response to 250 binding records and reports truncation. The
canvas displays up to 15 applications per page, with search across the returned
application names, services and variable names. Every observed member in the
selected shard remains available in the graph and inventory. Scroll within the
canvas, or use **Inspect topology** and **Locate selected** on a narrow screen.

The dense visual-review fixture contains 15 applications, one primary and six
replicas. These are explicitly artificial UI records. A separate development
acceptance test provisions seven PostgreSQL members and 15 application writers
to exercise real TLS bindings and writes through primary replacement; its
runtime result is recorded separately from visual coverage.
