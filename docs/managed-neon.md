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

## What the implementation contains

The API, durable operation store, worker, Kubernetes renderer and private
control clients are implemented. They share the same immutable revision and
operation lease. Provider resources carry ownership tokens as well as the
database record that owns them. Interrupted creation uses read-only provider
inspection before adopting a resource; matching configuration alone is not
proof of ownership.

The recovery adapter closes the connection proxy and computes, records the
committed WAL boundary, and waits for every pageserver to upload through that
boundary. It then stops storage writers while copying a deterministic object
inventory. The encrypted archive contains tenant identity, timeline identity
and remote storage. A zero commit LSN, divergent safekeeper observations or a
pageserver behind the committed boundary prevents capture.

Restore uses a separate target and an operation-specific object prefix. A
PostgreSQL journal records each original provider resource, its replacement,
generation and cleanup state. Recovery never overwrites the ordinary lifecycle
owner. A successful restore carries its identity into later updates, so the
first update cannot select the target's old bootstrap identity.

Cancellation cleanup uses the exact restore lease and binding. It keeps the
proxy closed, inspects interrupted provider effects, removes owned replacements
and clears the staging prefix. Lease loss stops further effects. An incomplete
cleanup retains durable work for retry; a cleaned partial target reports an
isolated failure instead of remaining marked ready.

The object-store transport checks every DNS answer before dialing a checked
public address and retains the original TLS hostname. It rejects redirects and
private or metadata destinations. Private object stores require a separately
reviewed egress policy. Placement separates pageservers from each other and
safekeepers from each other.

Internal control traffic is encrypted as part of the rendered runtime. The
broker exposes only HTTPS, and pageservers and safekeepers use its HTTPS service
name with their existing CA bundles. The controller PostgreSQL server uses a
certificate for both its short and namespace-qualified service names, rejects
plaintext IPv4 and IPv6 clients, and admits only the `storage_controller` role
over TLS with SCRAM. Its local Unix socket remains available for image bootstrap
under `/tmp`. The storage controller enables its upstream certificate checks,
uses the mounted public database CA, and does not receive the database private
key. All certificate snapshots are checked for matching keys, a valid CA,
current lifetime and every owned service hostname before workloads are applied.
The pinned broker defines its plaintext and HTTPS listeners as separate optional
arguments, so the manifest supplies only the HTTPS listener. The pinned storage
controller uses `rustls-native-certs` 0.8 and loads native roots when
`STORCON_DB_CERT_CHECKS` is present; that crate reads the explicitly mounted
`SSL_CERT_FILE`.

The connection proxy also calls Hakopod's private HTTPS authentication API.
Its operator configuration supplies that server's CA certificate bundle through
`neon_proxy_control_plane_ca_pem`. The renderer mounts only the public CA bundle
into the proxy, in an immutable ConfigMap for the reviewed revision, and sets
`SSL_CERT_FILE` to that mount. This trust is separate from the proxy's own
managed server certificate and its authentication token. Native acceptance
must prove that the pinned proxy accepts the configured issuer and rejects a
different issuer. See the [operator guide](managed-platform-operations.md) for
the hostname, bundle and update requirements.

## Verification and release requirements

The implementation has passed Go package tests, recovery race tests, API
checks and actual PostgreSQL tests for ownership, cancellation, immutable
lineage and stale leases. These checks exercise source behavior. They do not
establish that the complete Neon stack works with the built provider images.
Neon stays unavailable until native lifecycle and recovery acceptance pass.

Acceptance must prove tenant isolation, authenticated storage protocols,
verified broker and controller-database TLS with wrong-CA, wrong-hostname and
plaintext rejection, WAL quorum failure and fencing, pageserver replacement, durable
controller recovery, compute restart, branch creation and deletion, credentials
and connection limits, object-store outage, restore into a separate empty
resource and complete owned cleanup. Physical zone and provider availability
need separate infrastructure failure tests. Public endpoint availability cannot
be inferred from a proxy process listening on a port.
