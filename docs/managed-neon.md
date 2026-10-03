# Managed Neon

Managed Neon is being implemented as a self-hosted platform. It is unavailable
in the create catalog. This work does not connect to the paid Neon service and
does not rename an ordinary PostgreSQL database.

Neon uses PostgreSQL compute processes, pageservers, a safekeeper WAL quorum,
object storage, a storage controller and a connection proxy. The storage
controller also needs durable metadata. Hakopod must manage tenant and timeline
identity, compute configuration, authentication and storage placement together.
Three safekeepers alone do not make the complete service highly available.

## How the stack works

An application connects to the PostgreSQL proxy. The proxy authenticates the
connection through Hakopod and sends it to the platform's writer. The writer
runs SQL, reads pages from the pageservers and sends its write-ahead log to
the safekeepers. Pageservers turn that log into database pages and upload
durable layers to the configured object store.

| Component | What it does |
| --- | --- |
| Connection proxy | Authenticates the platform endpoint and routes application connections to compute zero. |
| Compute | Runs PostgreSQL. Compute zero accepts writes; additional computes follow the same timeline as read-only replicas. |
| Three safekeepers | Store the write-ahead log. Writes require a quorum; losing two members prevents writes from completing. |
| Pageservers | Serve database pages and upload their data to object storage. |
| Object storage | Holds the remote data layers. Its bucket and prefix belong to the reviewed platform configuration. |
| Storage controller | Manages tenant placement and membership. Its metadata lives in a separate PostgreSQL database. |
| Storage broker | Shares storage-service status used by the Neon components. |

Hakopod records the desired stack and its operations in its own PostgreSQL
store. The API and reconciler run in the same process by default. If that
process restarts, it resumes recorded work and checks ownership before making
another provider change.

## Supported shape and changes

Each platform currently owns one tenant and one timeline. Its `branch_limit`
must be `1`; creating additional branches or forks is not exposed. Compute node
zero is the writer. Additional compute nodes use Neon's read-only replica mode
and follow that timeline. Each compute permits 64 total PostgreSQL connections,
including four reserved for administration. The connection proxy routes to
compute zero; the replicas do not automatically replace it as writer.

The Configure flow changes component CPU and memory through a reviewed
revision. Storage size, object-store identity, node placement, membership and
secret references stay fixed. Applying an allocation change can restart
services and interrupt connections. Use a separate restore target to change
the storage or topology. Native acceptance must verify the first allocation
update after restore against the restored tenant, timeline and data.

A replica is not an automatic failover target in this implementation. If the
writer is unavailable, its proxy route is unavailable until the writer
recovers. The storage quorum protects a different part of the system. Placing
the three safekeepers on three Kubernetes nodes only provides independent
failure boundaries when the underlying hosts and infrastructure are also
independent.

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

Hakopod stores the controller's tenant attachment and safekeeper membership
notifications in PostgreSQL. It derives compute destinations from owned node
IDs, applies the configuration and checks the running result before acknowledging
the update. A retry can observe an update that already finished. A changed
revision or deletion prevents an old notification from changing compute.
After a compute restarts, it waits for the reconciler to replay its owned
configuration. These paths still require native acceptance with the provider.

## Ownership, backup and recovery

The API, durable operation store, worker, Kubernetes renderer and private
control clients are implemented. They share the same immutable revision and
operation lease. Provider resources carry ownership tokens as well as the
database record that owns them. Interrupted creation uses read-only provider
inspection before adopting a resource; matching configuration alone is not
proof of ownership.

The recovery adapter closes the connection proxy and computes, records the
committed WAL boundary, and waits for the attached pageserver to upload through
that boundary. It then stops storage writers while copying a deterministic object
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

Hakopod issues the internal Neon authentication tokens. Pageservers and
safekeepers share a verification key within one platform, so a compute can use
the same tenant-scoped token for both. Other platforms have different keys.
Controller administration, pageserver administration and safekeeper access
use separate scopes; pageserver callbacks receive only the generations scope.
The signing keys are derived in the control process from its encryption key
and platform identity. They are never stored in a pod, runtime snapshot or API
response. A restore receives a token for the restored tenant under the target
platform's trust. Keep the control-plane encryption key with its backups.

Compute control uses another platform-specific Ed25519 key. Go replaces the
template's public JWKS and control token before sealing an operation. The token
uses scope `compute_ctl:admin` and audience `["compute"]`, matching the pinned
provider's authorization rules. It can manage both computes in that platform;
another platform's key cannot verify it. The same platform retains its key on
restart, update and restore. Only the public key enters the compute
configuration. Go binds the control's TLS paths to the managed compute
certificate and enables the provider's TLS feature.

Go also owns the SQL port, listener, HBA policy, connection limits and WAL
settings. Optional PostgreSQL settings remain configurable; conflicting owned
settings are replaced. Raw PostgreSQL configuration accepts single-line
assignments and rejects include directives. External plaintext SQL is refused;
external TLS connections require SCRAM authentication. Compute zero sends WAL
to the safekeeper quorum. Replicas use the PostgreSQL WAL receiver with verified
TLS and an empty walproposer membership list, so routing cannot promote them.
The provider gives the PostgreSQL child its tenant token through the environment
instead of placing a password in `primary_conninfo`. Restore stops the previous
compute processes before configuring the recovered tenant and its credential.

Before sealing the compute configuration, Go copies the accepted proxy roles'
SCRAM verifiers into roles already declared in the reviewed template. An
undeclared proxy role is rejected before an operation is accepted. The pinned
provider gives newly created template roles administrative privileges, including
creating databases and roles, replication, and bypassing row-level security.
Review that role list as an administrative provisioning policy. Create roles
with limited privileges through SQL and grant only the database access they
need; [Neon's role guide](https://neon.com/docs/manage/roles) explains this
distinction. Adding a proxy credential never adds a template role.

Go validates the verifier's
salt and key encoding and preserves other reviewed role options. The provider
enables login when applying those roles. This keeps the proxy and database
passwords aligned, including when a restore target uses different credentials
from the source. A shared operator template does not share those passwords
between platforms.

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
plaintext rejection, verified TLS and HBA plaintext refusal on every compute SQL
listener, WAL quorum failure and fencing, pageserver replacement, durable
controller recovery, compute restart, owned tenant and timeline creation and deletion, credentials
and connection limits, object-store outage, restore into a separate empty
resource and complete owned cleanup. Physical zone and provider availability
need separate infrastructure failure tests. Public endpoint availability cannot
be inferred from a proxy process listening on a port.
