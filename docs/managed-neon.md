# Managed Neon implementation

Managed Neon is being implemented as a self-hosted platform. It is unavailable
in the create catalog. This work does not connect to the paid Neon service and
does not rename an ordinary PostgreSQL database.

Neon uses PostgreSQL compute processes, pageservers, a safekeeper WAL quorum,
object storage, a storage controller and a connection proxy. The storage
controller also needs durable metadata. Hakopod must manage tenant and timeline
identity, compute configuration, authentication and storage placement together.
Three safekeepers alone do not make the complete service highly available.

The source foundation requires a complete component allocation, versioned
secret references, object-store scope and digest-pinned images. Its availability,
cluster and public endpoint gates stay closed. These checks describe required
inputs; they do not create resources or prove recovery.

## Why the example Compose file is insufficient

The inspected upstream snapshot is
`fa504217c61bbcaf5c512d75830564541f917f8f` under the Apache 2.0 license.
The [upstream Compose README](https://github.com/neondatabase/neon/blob/fa504217c61bbcaf5c512d75830564541f917f8f/docker-compose/README.md)
states that its configuration tests images and is not a usable deployment. It
omits the storage controller. The [local control plane](https://github.com/neondatabase/neon/blob/fa504217c61bbcaf5c512d75830564541f917f8f/control_plane/README.md)
is also a development tool.

Hakopod needs a durable adapter for the storage controller's tenant attachment
and safekeeper membership notifications. Each update must resolve the owned
tenant and timeline, persist the new configuration, configure the correct
compute process and acknowledge only after applying it. A process restart or
duplicate notification must not redirect another tenant's data.

## Remaining implementation and acceptance

The shared managed-platform API, PostgreSQL operation store and bounded worker
are implemented and VM-tested. Neon-specific work is still being integrated: a
Kubernetes renderer, authenticated storage-controller and safekeeper clients,
compute management, and the production connection proxy. The first candidate
passed VM compilation, race tests, cluster tests and API checks. Review found
startup, placement, deletion and networking gaps. The corrected candidate
passed those source checks. Independent review then found pending-intent
recovery, resumable deletion, proxy network isolation and registration-claim
gaps. A later review found that provider resources lack a persisted operation
marker: an authenticated response with matching configuration cannot prove
ownership after an interrupted create. The compute API's operation UUID also
needs durable restart and mutation enforcement. Changes to the provider's
storage and compute processes are required; a controller-only ledger cannot
fence every downstream mutation. Those corrections and native acceptance
remain required. Qualified component images,
credential rotation, backup and restore are still release requirements.

The object-store transport foundation checks every DNS answer before dialing a
checked public address and retains the original TLS hostname. Redirects and
private or metadata destinations are rejected. It is not wired into a storage
worker yet. Private object stores require a separately reviewed egress policy.
The placement input describes separation within each stateful group; the
renderer must apply that rule to pageservers and safekeepers separately.

Acceptance must prove tenant isolation, authenticated storage protocols, verified
client TLS, WAL quorum failure and fencing, pageserver replacement, durable
controller recovery, compute restart, branch creation and deletion, credentials
and connection limits, object-store outage, restore into a separate empty
resource and complete owned cleanup. Physical zone and provider availability
need separate infrastructure failure tests. Public endpoint availability cannot
be inferred from a proxy process listening on a port.
