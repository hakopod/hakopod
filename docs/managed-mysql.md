# Managed MySQL

Private managed MySQL 8.4 is included in self-hosted `v0.1.0-alpha.50`.
Install the matching pinned controller before creating a database. Native
engine and replica-retry tests passed on the named development cluster; their
exact scope is recorded below. Cloud provisioning requires a separate operator
rollout and approved capacity.

## Choose a layout

MySQL 8.4 uses Oracle's MySQL Operator, InnoDB Cluster and MySQL Router. Standalone
has one voting database member and one Router; it cannot survive loss of its
database member. Cluster mode has one primary and two, four or six voting
replicas, plus two Routers. A majority of voting members must remain connected
for writes. Routers do not vote and do not hold application data.

```toml
schema_version = 1
name = "orders-mysql"
engine = "mysql"
version = "8.4"
mode = "cluster"
replicas = 2
shards = 1
cpu = "500m"
memory = "1Gi"
storage_gib = 10

[tls]
mode = "required"

[placement]
spread = "nodes"
```

This example requires three eligible amd64 nodes. `spread = "zones"` instead
requires three distinct reported zones. Nodes may belong to different providers
inside one connected Kubernetes cluster. Labels do not prove physical failure
independence, sufficient network performance or resilient storage. Independent
Kubernetes clusters cannot form one managed database through this API.

Create through the guided dashboard or the shared API/CLI:

```sh
hakopod database create --project demo --environment development --file mysql.toml
hakopod database show DATABASE_ID
```

Each database member needs at least 500m CPU and 1Gi memory. Its sidecar adds
100m CPU and 256Mi memory. Each Router adds 100m CPU and 128Mi memory. At the
minimum size, three members and two Routers request 2 CPU cores and 4Gi memory,
plus three persistent volumes. Admission also reserves replacement, sandbox
and recovery headroom. These allocations are distinct from measured usage.

Per-member CPU, memory, storage, placement and engine version are fixed at
creation. Recover into a separate database to change them. The selected native
operator does not safely reconcile arbitrary pod-resource edits. A supported
replica-count change requires a current healthy observation and a reviewed
revision. Capacity must remain available during the change. Reducing replicas
does not release the earlier CPU or storage reservation; both remain reserved
until verified database deletion. After the replica change succeeds, the memory
reservation follows the completed layout.

### Retry a failed replica change

Open the database's Activity tab and select **Review retry** on the latest
failed replica change. Hakopod checks whether the controller accepted the
requested revision. If it did, the retry checks that rollout again. If it did
not, the previous layout must pass fresh health and topology checks before
Hakopod can retry the change.

Review the warnings and type the database name to confirm. A retry creates a
new operation while preserving the requested replica count, configuration
revision and reservations. Activity keeps the earlier attempt and its failure.
The database stays unready until native checks pass.

The CLI uses the same review:

```sh
hakopod database resize-retry-plan DATABASE_ID \
  --operation-id FAILED_OPERATION_ID --revision CURRENT_REVISION
hakopod database resize-retry DATABASE_ID \
  --operation-id FAILED_OPERATION_ID --revision CURRENT_REVISION \
  --review-id REVIEW_ID --name orders-mysql --idempotency-key SAVED_RETRY_KEY
```

Keep the exact request and its 8–128 character key before submitting. If the
connection fails, resend the same request and key to retrieve that attempt.
If the attempt itself fails, review the latest failed operation and use a new
key. An expired review must be refreshed. A retry cannot select a different
replica count or bypass a lost quorum; recover native health first.

## Connect with verified TLS

The application database and account are both named `app`. The account owns
`app.*`, requires TLS and permits at most 100 connections. It cannot create
global accounts or administer the server. Administrative bootstrap credentials
remain in a separate owned Secret and are never bound to applications.

Use the observed write endpoint on port 6446 for writes and primary reads. A
cluster also exposes a replica endpoint on port 6447. These are explicit route
choices; the Router does not parse arbitrary SQL and choose a destination for
each statement. Existing sessions must reconnect after failover. Applications
must handle aborted transactions and retry only when safe. Replica routing is
not a separate database permission role and does not guarantee read-after-write
consistency.

Download the public CA from Connections & security, or obtain its
`certificate_pem` through `hakopod database trust DATABASE_ID`. Native clients
must verify both the issuer and endpoint hostname:

```sh
mysql --host=OBSERVED_HOST --port=6446 --user=app --password \
  --database=app --ssl-mode=VERIFY_IDENTITY --ssl-ca=database-ca.crt
```

Application bindings use protocol `mysql` and an explicit `read_write` or
`read_only` endpoint. They mount the public CA at the path shown in the
connection instructions. Configure your driver's TLS options with that CA and
hostname verification; a generic MySQL URL does not configure every driver.
Do not disable certificate verification to work around a connection failure.

The security view reports verification only after native connections succeed,
plaintext is rejected and the served certificate matches the issued identity.
Renewal keeps an overlap trust chain for existing clients and reloads MySQL and
Router certificates. Application trust renewal rolls out the binding's public
CA. Stale or incomplete checks remain unverified in the dashboard.

## Public endpoints in development

The unreleased MySQL public endpoints route through MySQL Router: `read_write`
uses backend port 6446, and clustered databases also offer `read_only` through
backend port 6447. The public port comes from operator-owned inventory.
Publication stays disabled until the release candidate passes native Router
TLS, routing and revocation checks. TLS is required, and the database layout
determines the available routes. See the
[route definitions](../internal/database/public_endpoint.go).

Before any eventual publication, the engine verifies the current owned InnoDB
Cluster, Router service, Router deployment and every ready Router target. It
also verifies the issued hostname certificate at each Router before it treats
the backend as usable. See the
[MySQL endpoint checks](../internal/cluster/database_public_endpoint_mysql.go).
Native verification of the public listener remains outstanding.

Cloud public database endpoints remain unavailable. This development contract
does not create a Cloud listener, address, DNS record or firewall rule.

## Monitor and recover

Member resource samples include the MySQL server and its sidecar. Separate
Router resources are not included in those charts. The member limit displays
the server and sidecar allocations separately.

Native activity statistics describe the observed primary. Connection counts
cover the `app` account; data size estimates application table and index bytes.
The Server queries counter is server-wide and includes administrative queries.
Filesystem usage, query latency, replication lag and cache-hit ratio are not
collected for MySQL. Missing statistics remain unavailable.

Managed backups capture application tables, binary values, views, routines,
events and triggers into an encrypted, verified logical archive. Capture holds
a global read lock: writes and DDL wait while the bounded dump runs. This is not
an online backup or point-in-time recovery. Topology changes invalidate capture.

Recovery requires a verified MySQL 8.4 archive and a separate empty MySQL 8.4
target. Application access is isolated during restore; existing app sessions
are disconnected and the target is checked again for emptiness. Archive SQL
runs as the application account, without global privileges. The source is
retained. Application ingress remains closed until durable recovery completion
and inspection; maintenance reconciliation then restores the private network grant.
Inspect recovered data before connecting an application, and account
for writes after the capture point. External MySQL archive import is not yet
supported by the import API.

Deletion first asks the MySQL controller to remove its database object while
pods and credentials are still present. Namespace removal follows controller
finalization. The durable allocation remains reserved until persistent volumes
are reclaimed. An interrupted or stalled deletion must remain visible as such.

## Operator prerequisites

The implementation pins MySQL Operator 26.7.0-2.3.0, server 8.4.12 and Router
8.4.10 by digest. The verified manifests currently require amd64. Alpha.50
packages the pinned controller as `mysql.json`; packaging requires that exact
payload and fails on a missing or unexpected controller file. Production
installation follows the reviewed installer-module plan and apply workflow.
The script at `scripts/install-development-mysql-controller.sh` remains limited
to the named development cluster.

The packaged controller keeps debug logging at `0`, disables automatic password
storage and rejects extra helper containers. Installing it satisfies one MySQL
admission prerequisite. Creation still requires the current pinned controller,
a healthy controller rollout, required permissions and valid placement and
capacity.

On gVisor workers, MySQL and its sidecar share a bounded 16Mi socket volume.
Containerd must pass these exact pod annotations to runsc:

```toml
pod_annotations = [
  "dev.gvisor.spec.mount.rundir.share",
  "dev.gvisor.spec.mount.rundir.type",
  "dev.gvisor.spec.mount.rundir.options",
]
```

Place this setting in the existing runsc runtime table appropriate to the
installed containerd version. Preserve the runtime's other settings and follow
the operator's node-maintenance procedure when reloading it. Without shared
mount hints, a sidecar can see lock files but cannot connect to the server's
Unix socket. Native readiness checks prevent that database from becoming ready.
See [gVisor's shared-volume configuration](https://gvisor.dev/docs/user_guide/containerd/configuration/#enabling-inotify-for-shared-volumes).

MySQL public endpoint descriptors are under development and remain disabled as
described above. MySQL Router and managed Vitess are distinct capabilities;
enabling MySQL does not imply that Vitess is available.

## Current verification

The six native engine cases passed against `7b3842c` on 2026-10-04. They cover
lifecycle and private application routes; one-, three-, five- and seven-member
layouts; quorum loss and rejoin; encrypted logical backup and separate-target
recovery; Router backend identity rejection; bounded credential-log audits;
and cleanup with the namespace and persistent-volume inventories restored.
The [release acceptance record](managed-database-release-acceptance.md#mysql-84)
contains source revisions, immutable images, durations and evidence hashes.

The added replica-retry orchestration uses the unchanged MySQL runtime. Its
native API/reconciler test passed against `2675ccf`: a retry from the prior
layout reached five members, and a retry of an accepted change reached three.
Both retained committed binary data, the original failure and exact-request
replay. Cleanup reclaimed the fixture namespace and volumes.

The full Go suite against disposable PostgreSQL, Go vet, CLI/API/store
regressions, 52 SDK tests, and dashboard build and 218 tests passed. Independent
rendered review passed 86 retry UI cases across both themes and desktop/mobile
widths. The installer candidate passed fresh-install and alpha.48/alpha.49
upgrade checks on AMD64 and ARM64. These checks do not establish a production
installation.

Public MySQL endpoints are a separate unresolved qualification area. Private
engine qualification does not enable a public listener or Cloud public access.
