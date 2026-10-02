# Managed MongoDB

MongoDB 8.0 is included in the `v0.1.0-alpha.47` self-hosted release.
Standalone databases and three-, five- and seven-member replica
sets have passed the native development acceptance recorded below. Creation
requires the qualified, digest-pinned controller. These results do not establish
production or independent-zone availability.

## Layout and placement

MongoDB 8.0 runs as a replica set through the MongoDB Kubernetes Controller.
Standalone means one replica-set member, without high availability. Cluster
mode has three, five or seven voting members, with no arbiters. Majority writes
require a connected majority. A replica is a full data-bearing member.

```toml
schema_version = 1
name = "events"
engine = "mongodb"
version = "8.0"
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

This configuration requires three eligible amd64 nodes. Each member requests
500m CPU, 1Gi memory and 10Gi data storage. Its agent adds 100m CPU and 256Mi
memory, with a separate 1Gi log volume. The three-member total is 1.8 CPU cores,
3.75Gi memory and 33Gi storage, before reserved operating/replacement headroom.

Use `spread = "zones"` to require a different reported zone for every member.
Provider-spanning nodes must belong to one connected Kubernetes cluster with
working private routing and suitable storage. Zone labels and this setting do
not establish physical fault independence or certify cross-cloud availability.
Latency between voting members affects majority writes.

Per-member CPU, memory, data storage, placement and engine version are fixed at
creation. Replica-count changes require a reviewed revision and enough eligible
capacity. Recover into a separate target to change member capacity.

Removing replicas retains their persistent volumes. The database also keeps its
highest CPU, memory and storage reservation until deletion. Scaling from seven
members to three therefore does not immediately return that capacity to the
workspace. This protects retained data and leaves room to replace members.

## Driver discovery and TLS

The private discovery endpoint publishes every replica-set member. Use a
replica-set-capable MongoDB driver even for standalone databases. The driver
discovers the current primary; the discovery service does not inspect queries
or route every write itself. Every advertised member hostname must be reachable.

Managed bindings require protocol `mongodb`, endpoint `cluster` and
`cluster_aware = true`. They provide the application account for the `app`
database, mount the public CA and include these URI options:

| Option | Value |
| --- | --- |
| `tls` | `true` |
| `tlsCAFile` | The mounted database CA path |
| `replicaSet` | `database` |
| `authSource` | `app` |
| `w` | `majority` |
| `readConcernLevel` | `majority` |
| `readPreference` | `primary` |
| `retryWrites` | `true` |

Clients must verify the issuer and hostname. The service requires TLS 1.2 or
newer and SCRAM-SHA-256 authentication. Application bindings contain neither
cluster credentials nor the monitoring/recovery accounts. The application
account has `readWrite` on `app`, without administrative cluster privileges.

Handle election interruptions and ambiguous write outcomes. A timeout does not
prove rollback; use driver retry semantics and application idempotency where
appropriate. Changing read preference to a secondary changes consistency and
availability behavior. Dedicated public MongoDB endpoints are not implemented.

Certificate renewal retains overlap trust for existing clients. The control
plane verifies the certificate actually served by every member. Binding trust
updates roll out the application's mounted public CA. Missing, expired or stale
verification is shown explicitly in Connections & security.

## Monitoring

Member CPU and memory samples include `mongod` and its agent. The separate log
volume is included in configured storage, but configured storage is not measured
disk usage. Resource history retains one sample per minute for 24 hours.

Native connection and operation counters cover the primary, including monitoring,
agent and replication work. Active connections are connections performing an
operation. Logical application data bytes exclude indexes and allocated files.
Replication lag, cache-hit ratio, query latency and filesystem usage are not
collected for MongoDB. Missing values remain unavailable.

## Backup and recovery

Managed backup captures the `app` database using one snapshot read timestamp
across collections. It preserves raw BSON, collection options, validators,
indexes and views, including supported capped/time-series collections. Metadata
is compared before and after capture; schema changes or expired snapshot history
fail the capture. This is a logical snapshot, not continuous oplog recovery or
point-in-time recovery.

The bounded archive supports at most 256 collections, 64 indexes per collection
and 16MiB documents. Unsupported metadata fails capture rather than silently
dropping options. Archives are encrypted and authenticated by the managed backup
pipeline. External MongoDB file import is not implemented.

Recovery requires a compatible MongoDB 8.0 archive and a separate empty target.
It closes application ingress and replaces every owned target member to revoke
existing sessions. A dedicated loopback-only account restores historical
documents that predate current validators; application privileges remain
unchanged. The restored validators still apply to future application writes.

Application ingress remains closed after restore until durable completion and
inspection are both recorded. Inspect data, indexes, views and application
behavior before cutover. The source is retained, and writes after capture are
not included. A failed restore remains isolated and cannot be retried over a
partially populated target as though it were empty.

## Current verification

On September 30, 2026, all five native acceptance cases passed in the named
`k3d-hakopod-dev` cluster against the same controller digest and source inventory.
Standalone and clustered lifecycle checks covered BSON fidelity, restricted application
permissions, TLS/plaintext refusal, issuer/hostname rejection, certificate
renewal, overlap trust and native metrics. Separate-target recovery also passed
for binary BSON, Decimal128, timestamps/dates, historical validation exceptions,
unique/hidden/TTL indexes, views, capped/time-series collections, source
preservation and old-session rejection.

Three-member application binding acceptance verified native
driver discovery, binary data and subtype fidelity, access to every member,
denial for an unbound service, scoped credentials, certificate renewal with
old-CA overlap, application trust rollout and network revocation. Bounded member,
initialization, agent and controller logs contained none of this fixture's
password or private-key values. This is not a guarantee about all future logs.

Resizing from three to five to seven members and back to three, election/quorum
behavior, and the latest recovery-ingress checks also passed. The
[controller qualification record](database-mongodb-qualification.md) describes
the exact artifact, evidence and limits.

Independent rendered UI review passed the recorded MongoDB scope across both
themes and desktop/mobile layouts, including dense topology and rejection
states. Hidden creation steps and physical touch remain unverified.
The two development nodes share one VM; these tests cannot
prove zone/provider outage resilience. No production deployment or verification
has been performed.

The development installer pins controller 1.13.0-hakopod.1, MongoDB server 8.0.32 and all
helper images by digest. It is restricted to `k3d-hakopod-dev` and is not a
production installation workflow.
