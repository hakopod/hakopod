# Managed Oracle Database

Oracle Database Free is included in the `v0.1.0-alpha.47` self-hosted candidate,
pending publication. Standalone has passed native development checks for
lifecycle, TCPS renewal, private application bindings and isolated schema
recovery. These checks do not establish production availability.
Enterprise and Data Guard have a separate source implementation. Their runtime
gate stays closed until the hardened controller and a licensed customer image
pass native acceptance. Passing the Free tests does not open that gate.

## Editions and images

Oracle Database Free is proprietary, free-to-use software. It is not open source.
Hakopod's standalone implementation uses the full Oracle Database Free 26ai image,
version `23.26.3.0`, pinned to an immutable multi-platform digest. The full image
includes SQL*Plus, Data Pump and wallet tooling; the Lite image omits tools needed
by this implementation. The platform version in a database revision is `23.26`.

Free is limited by Oracle to two CPUs, 2 GB of database memory and 12 GB of user
data. The container reservation is larger because it also runs the listener and
management tools. These upstream limits do not become larger when a user reserves
a larger container. Free does not provide a Data Guard cluster.

The Enterprise configuration contract accepts an explicit image with a SHA-256
digest, an optional registry-credential reference scoped to the database's project
and environment, and license confirmation. It never asks for a license document,
registry password or token inside database TOML. The runtime currently rejects
Enterprise deployments: validating an image reference does not prove image
compatibility, entitlement or failover safety. RAC, Active Data Guard, TDE and
licensed monitoring packs are separate support decisions.

## Standalone configuration

```toml
schema_version = 1
name = "orders-oracle"
engine = "oracle"
version = "23.26"
mode = "standalone"
replicas = 0
shards = 1
cpu = "1"
memory = "4Gi"
storage_gib = 10

[tls]
mode = "required"

[oracle]
edition = "free"
```

The shared API and CLI parse the same versioned configuration. API creation still
checks authorization, scope, allocation and runtime availability. Enterprise image,
edition and resource changes cannot bypass the reviewed migration restrictions.

Each standalone database owns one StatefulSet, a data volume and a backup-staging
volume of the same size. Initialization may take several minutes. Failed or
partial initialization retains its data for inspection; the startup script must
not silently replace it. Hosted placement uses an authorized sandboxed worker.

The APP schema's quota is the smaller of 10 GiB and the data volume minus 8 GiB.
At the 10 GiB minimum volume, APP can use 2 GiB; the remaining space is reserved
for system tablespaces, undo, redo and temporary work. This is a schema quota,
not a guarantee that system activity can never exhaust a volume. Diagnostics
use a bounded 512 MiB temporary volume and are not a persistent log archive.
Writable-layer and temporary storage have an aggregate 2 GiB container limit.

## Connections and trust

The private endpoint uses Oracle TCPS on port 2484 and the `FREEPDB1` service.
Applications receive the `APP` schema account. The independent CDB administrator
and wallet credentials remain inside the database namespace. The application
cannot create users, change instance settings or read Oracle's credential tables.

Use a client that verifies both the CA and hostname. Download the database's
public CA through `/api/v1/databases/{id}/trust` or mount the trust supplied by a
managed binding. A connection URL alone does not install a private CA in a driver.
SQL*Plus clients need a wallet containing that public CA and server-name matching
enabled. They do not need the server's wallet or private key.

The Go runtime uses `go-ora/v3` with an explicit TLS configuration. Version 2.9.0
failed authentication against this pinned 26ai image in native testing. Version
3.0.1 passed repeated application connections with `FAST LOGIN=false`; every
physical connection negotiates afresh. Keep `SSL=enable` and `SSL VERIFY=true`,
load the public CA into the client's root pool, and set the endpoint hostname.
Never use `InsecureSkipVerify` or replace hostname verification with encryption
alone.

Readiness verifies native database role, PDB state, supported version, authenticated
APP access and the issued certificate actually served. It also checks that
plaintext access is rejected. Metrics use ordinary dynamic performance views;
they do not query AWR, ASH, ADDM or separately licensed diagnostic packs.

## Public endpoints in development

The unreleased Oracle Free public endpoint uses TCPS to reach the `APP` account
in one standalone `FREEPDB1` database. Its backend listens on port 2484; the
public port comes from operator-owned inventory. Publication stays disabled
until the release candidate passes native TCPS, identity-transition and
revocation checks. Enterprise and Data Guard require separate licensed native
acceptance. See the [route definitions](../internal/database/public_endpoint.go).

A publication review would bind the approved hostname, CA and SANs to the
current single-member workload, service and persistent-volume identities. Oracle
loads its server wallet at startup, so changing public names replaces the only
pod. The route must stay closed during that durable, reviewed identity transition;
existing sessions end and clients must reconnect after the replacement. The
implementation records and checks these identities in the
[Oracle endpoint workflow](../internal/cluster/database_public_endpoint_oracle.go).

Cloud public database endpoints remain unavailable. This under-development
engine contract does not provision a Cloud listener, address, DNS record or
firewall rule.

## Backup and recovery contract

The initial implementation captures the APP schema with Data Pump and a flashback
SCN. It is not a physical RMAN backup, archived-redo recovery or point-in-time
recovery. A SYS-owned DDL guard coordinates with the capture session before the
SCN is selected. Application schema changes temporarily fail with a retry message
while a capture holds that guard; ordinary data changes continue against the
SCN-based snapshot. Native acceptance has verified that guard, ordinary DML during
capture, and cancellation after a server-side Data Pump job starts.

The bounded archive carries version, edition, source identity, revision, SCN,
dump size and a SHA-256 checksum. The managed-backup service provides the outer
encryption. Restore stages and validates the whole input before database writes,
requires a separate empty target with the same version and edition, closes
application ingress and replaces the target pod to revoke existing sessions.
Import runs as APP with temporary directory access. Failed job cleanup or grant
revocation prevents a successful recovery result. Access stays closed until the
completed recovery has been inspected.

Native acceptance covered binary and Unicode data, concurrent DDL, corrupted
input, empty-target checks, cancellation, revoked sessions, independent source
and target writes, cleanup and inspection gates. Production qualification must
also cover the supported workload sizes and schema features.

## Current evidence and remaining work

On September 29, the standalone development test passed native initialization,
authenticated reads and writes, required TCPS, plaintext refusal, schema privilege
boundaries and ordered deletion. The development environment is one physical VM;
it establishes no multi-zone or multi-provider availability guarantee.

Subsequent runs verified fresh-target initialization and hardened replacement
startup. The script waits for the image's completion hook and data marker before
configuring TCPS and APP. It tolerates an already-stopped listener during wallet
replacement but still requires the new listener to start successfully. Native
renewal replaced the single Oracle pod with a new pod UID, reached ready without
a container crash, preserved data, and kept old-CA overlap working. The
replacement ends existing sessions, so clients must reconnect.

The final September 29 native recovery run passed in 617.44 seconds, including
owned resource cleanup. It verified binary and Unicode data, a view, sequence,
stored function and trigger, separate source/target writes, session revocation,
incomplete-archive and nonempty-target refusal, and inspection-gated ingress.
It also passed the public-CA-only application wallet, unbound-service denial,
binding revocation, SCN/DDL guard and cancellation cleanup checks.

An earlier import failed with `ORA-31685` because account grants, default roles
and quotas are separate metadata paths from object grants. Capture and import
now exclude each of those account-policy paths. The native test used a different
source quota and confirmed that restore retained the target's managed quota and
restricted application privileges. Diagnostics expose only error codes and
known metadata categories, without SQL, credentials or object names.
The passing log is `work/database-enterprise/oracle-recovery-live-v6.log`.
This is development acceptance, not a production release or proof of recovery
for every Oracle schema feature and workload size.

The Enterprise adapter includes scoped image-pull credentials, per-member
storage and placement, primary routing, native transport/apply checks, schema
recovery and reviewed switchover methods. The source, store and API tests passed
on the development VM, with an isolated PostgreSQL database for durable-operation
checks. Synthetic wallet tests passed without exposing credential values.
The hardened Oracle operator also passed its database-common, single-instance
controller and Data Guard controller package tests against the pinned source.
These checks do not establish compatibility with a licensed database image.
No licensed Enterprise image or Data Guard cluster has passed native acceptance
in this work. Forced failover, fencing an unreachable primary,
reinstatement and physical recovery remain separate implementation requirements.
Free standalone testing cannot validate those Enterprise behaviors. Enterprise
schema recovery now waits for every unchanged standby to apply the SCN captured
after import and temporary privilege cleanup. That replay boundary has passed
source checks and still requires native Enterprise acceptance.

## How the Enterprise stack fits together

The Enterprise path runs Oracle's Database Operator 2.2.0, pinned to source commit
`ff6f9178c1650df30afbf203ebdb633e9b80760a`, with the changes in
`scripts/apply-managed-oracle-patches.py`. The operator is licensed under UPL 1.0.
That open-source license covers the operator, not Oracle Database Enterprise.
The customer remains responsible for the database image and its Oracle license.

The Go API owns authorization, immutable database revisions, resource allocation
and durable operations. It creates an owned namespace and a
`SingleInstanceDatabase` resource for each member. Cluster mode also creates one
`DataguardBroker`. The initial primary has SID `HPDB0`; physical standbys use
`HPDB1`, `HPDB2` and so on. The application schema lives in `APPDB`.

Each member has three persistent volumes: database files, the fast recovery area
and temporary backup staging. Each volume uses the requested storage size. A
three-member cluster with 100 GiB per volume therefore requests 900 GiB before
the storage provider's own replication overhead. The broker helper separately
requests 100 millicores and 256 MiB. Requests and limits are explicit, and the
helper does not receive a Kubernetes service-account token.

Node and zone placement comes from the same authorized allocation rules as other
managed engines. A database cannot select arbitrary nodes outside its allocation.
Distinct zone labels are checked against the observed pods. Nodes in several
cloud providers still need a working Kubernetes network and compatible storage.
Their labels alone do not establish cross-cloud availability. Synchronous redo
transport also makes network delay part of transaction commit time.

Data Guard uses synchronous redo transport and Maximum Availability protection.
Readiness requires one writable primary, the expected database ID and unique
names, and healthy redo apply on every physical standby. It verifies the exact
set of synchronized destinations. Standbys remain mounted. Read-only standby
access is not included because Active Data Guard has its own entitlement and
qualification requirements.

The stable write service selects only the pod that passed those native checks.
If the topology, placement or served certificate fails validation, routing
closes. There is no read/write SQL classifier and no Oracle replica read endpoint.

## Licensed image and transport requirements

An Enterprise image must be pinned by digest and compatible with the operator's
Kubernetes extension. The current source contract accepts versions `19` and
`23.26`. It requires SQL*Plus, Data Pump, Oracle wallet tools, OpenSSL, Python 3,
`flock` and `timeout`, with the Oracle process running as user and group 54321.
An arbitrary Oracle image is not assumed to satisfy this contract.

Private image credentials use an existing project and environment scoped
registry reference. The runtime copies only Docker authentication into the owned
database namespace. It rejects a different registry host, unrelated scope and
credential revision rollback. A copied credential counts as in use while a
database is still provisioning.

The platform mounts an immutable bootstrap script, issued certificate, private
key and separate administrator and wallet credentials. The bootstrap configures
TCPS on port 2484 and a local IPC listener. It removes the TCP listener and
disables the HTTP and HTTPS management ports. A failed configuration step stops
the listener; it does not fall back to plaintext.

The operator patch enables certificate name verification for broker connections.
It passes shell content through standard input so credentials do not enter the
Kubernetes exec URL. A small Python helper handles native wallet prompts through
a terminal, with bounded time and output. Credentials never become native tool
arguments. Unknown or ambiguous prompts fail rather than guessing a password
order. This helper still needs acceptance against each supported licensed image.

Server wallets contain the issued private identity. The public client wallets
contain the CA certificate only. Certificate renewal uses the same bootstrap
path, then the Go runtime checks the served leaf certificate and rejects any
remaining listeners on ports 1521 or 5500. Application connections verify both
the CA and the database hostname.

## Planned role changes and outage recovery

A graceful switchover moves the primary role while every member is reachable and
healthy. The source review binds the request to the database revision, broker UID,
all member UIDs, current primary and selected standby. Its approval expires after
ten minutes unless the durable request has already started. The write service
holds a request-specific guard, which prevents maintenance from reopening it
while the change is unresolved.

A stale review is checked before the endpoint is closed. If no broker request
was submitted, the operation ends in the review phase and needs a new review.
A previous request with an uncertain outcome retains its guard. A fresh review
cannot bypass a failed broker operation, and retry refuses both terminal review
failures and confirmed broker failures.

The broker receives a one-time request token. Repeating the durable operation
uses the same token. Completion requires the selected database to be the native
primary, every standby to apply redo, and all issued certificates to pass checks.
The original token remains in broker state for retry safety. The API stores the
review, operation and audit event in PostgreSQL. A role change preserves the
configuration revision because it does not change the saved database settings.

The source API has three routes:

- `POST /api/v1/databases/{id}/switchover-plan` takes `target_member` and returns
  the saved review ID, exact topology and connection warning.
- `POST /api/v1/databases/{id}/switchover` takes `review_id`,
  `expected_revision` and `confirm_name`, with an `Idempotency-Key` header. It
  returns the accepted operation for polling.
- `POST /api/v1/databases/{id}/switchover-retry` takes the existing `operation_id`,
  `expected_revision` and `confirm_name`. It resumes the same approved target
  after a worker timeout. It cannot change the target, bypass a changed revision
  or turn a failed broker action into another promotion.

Every route requires database write authority in the resource's project and
environment. Application-scoped keys cannot perform these actions. Acceptance
excludes concurrent backups, recovery and certificate maintenance. Each runtime
mutation checks the durable lease and reauthorizes the accepting key. The
Enterprise native-acceptance gate also applies to these routes.

An unreachable primary is a different case. A timeout or deleted Kubernetes pod
does not prove that the old database has stopped writing. Forced failover needs
a verified node or provider fencing operation and a recovery process for the old
primary. The current platform has no accepted fencing contract for Oracle, and
the pinned operator has no tokenized reinstatement API. These actions stay
unavailable; fast-start failover and its observer are disabled.

Enterprise schema recovery uses the same bounded Data Pump format as Free, with
the Enterprise edition and exact supported version recorded in the archive. It
selects the verified primary, stages into its backup volume and restarts that
target pod after application ingress closes. Other member identities and the
primary's place in the topology must remain unchanged. Import uses the target's
restricted APP account, and the final checks verify replication and transport.
The staging archive can be at most 32 GiB. Larger databases need a separately
implemented and tested recovery path; increasing a data volume does not change
this archive limit.

References: [Oracle Database Free](https://www.oracle.com/database/free/),
[Oracle container image sources](https://github.com/oracle/docker-images/tree/main/OracleDatabase),
[Oracle Database Operator 2.2.0](https://github.com/oracle/oracle-database-operator/releases/tag/2.2.0),
and [go-ora](https://github.com/sijms/go-ora).
