# Managed MySQL

Status: included in the `v0.1.0-alpha.47` self-hosted candidate, pending
publication. Lifecycle, Router TLS and private application binding checks have
passed. The latest scaling and recovery-ingress acceptance remains pending, as
recorded below. This status does not claim production or independent-zone
availability.

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
revision. Capacity must remain available during the change.

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
8.4.10 by digest. The verified manifests currently require amd64. The installer
at `scripts/install-development-mysql-controller.sh` targets only the named
development cluster; it is not a production installation workflow.

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

The retained development logs were inspected again on September 30, 2026.
`mysql-lifecycle-fixed-live.log` records passing lifecycle and clustered logical
recovery; recovery took 1272.77 seconds. `mysql-quorum-fixed-live.log` records a
passing quorum refusal/recovery and bounded credential-log audit in 158.82
seconds. These are historical passes, not verification of subsequent source
changes or a production deployment.

`mysql-scaling-fixed-live.log` verified five, seven and three voting members,
then timed out waiting after Router replacement. The retained fixture currently
reports database and Router pods evicted for ephemeral-storage pressure. This
supports resolving development capacity before repeating the test; it does not
establish a database recovery or rejoin implementation defect.

Router keeps warning-level logs so metadata certificate failures remain visible.
Debug logging stays disabled. The native tests inspect complete bounded member,
initialization, Router and operator logs for this fixture's credential and
private-key values without printing those logs.

The latest recovery-ingress gates and explicit recovery checks against every
replica still need native acceptance. The standalone
`TestManagedMySQLRouterBackendTLSLive` test checks the effective Router TLS
configuration and injects unrelated-issuer and wrong-hostname server identities.
It must prove that direct server authentication remains available, Router
rejects the backend identity, and normal routing recovers after restoring the
certificate. An earlier strict-certificate run passed on October 1, 2026, in
360.724 seconds. Both unrelated-issuer and wrong-hostname rejection passed;
normal fixture cleanup left no namespace or persistent-volume reference. This
result predates the latest source. Reruns exposed hidden certificate details at
the previous ERROR-only log level and repeated-error suppression in Router.
The current test uses a fresh UID-checked Router process for each fault and
requires new certificate-specific evidence. That native rerun passed on
October 1, 2026, in 262.371 seconds. Both faults, restored routing and complete
fixture credential-log audits passed. The namespace and its persistent-volume
references are absent. The audit excludes only proven pre-fixture evictions,
inspects overlapping current and previous container logs, and rejects lost
restart history or an operator process change. Scaling and recovery acceptance
remain pending.
