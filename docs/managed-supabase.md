# Managed Supabase source foundation

This candidate describes a self-hosted Supabase platform. It is separate from
Hakopod managed PostgreSQL. It does not represent a hosted Supabase connection
or a PostgreSQL alias.

The source model covers the services in the audited upstream self-hosted
Compose release: PostgreSQL, Envoy, Auth, PostgREST, Realtime, Storage,
imgproxy, postgres-meta, Studio, Edge Runtime and Supavisor. Every service must
have a bounded resource allocation and an immutable image digest before a plan
can be produced.

This release supports the fixed application database name `postgres`. Its
pinned initialization assets target that database explicitly, so other names
are rejected during review instead of reaching Kubernetes.

Candidate admission reserves at least 500m CPU and 2 GiB memory for PostgreSQL,
and 250m CPU and 512 MiB memory each for Realtime and Supavisor. These are
Hakopod safety floors for later acceptance, not upstream sizing guarantees or
evidence that the stack has run successfully.

The candidate is unavailable. Its cluster and public qualification fields stay
false. It must not appear as a selectable catalog product or be described as a
working managed service.

## Security and ownership contract

The candidate reconciler owns one namespace for each Supabase resource. Every
namespaced object has an owner reference to the already observed Namespace
UID; no unimplemented custom-resource owner is invented. Before it changes an
object, the reconciler must compare that UID and verify the resource labels and
complete owner-reference chain. Workload and Service selectors remain stable
across revisions, while immutable ConfigMap names include the platform
revision. Applications never receive Kubernetes credentials.

Secrets are references to versioned Hakopod secrets. Both the legacy symmetric
JWT/API keys and the modern signing, verification, publishable and secret keys
are explicit references; none may silently fall back to sample values. Secret bodies must never
enter the desired spec, status, operation record, event, log, ConfigMap or
evidence bundle. The initial set includes PostgreSQL, JWT, API, dashboard,
Realtime, pooler, postgres-meta and Storage credentials. Email signup remains
disabled unless a separate SMTP secret is configured and verified.

Only Envoy may serve the application API. Studio needs separate administrator
authorization. PostgreSQL and Supavisor remain private until a database
endpoint passes its own review. Internal services use ClusterIP networking and
default-deny policies. Internal access is limited to each component's declared
dependencies and ports; adding a listener does not grant access to unrelated
components. DNS egress selects the `kube-dns` workload in `kube-system` on
TCP and UDP port 53. NodeLocal DNS and other DNS deployments require a
separately qualified destination policy. Public routing requires HTTPS, exact
redirect origins and the existing Hakopod certificate and routing controls.

PostgreSQL data, its pgsodium encryption key, object data, edge functions and
Studio snippets use separate owned persistent storage. PostgreSQL creates its
data directory at `/var/lib/postgresql/data/pgdata`, below the mounted claim.
The key claim mounts at `/var/lib/postgresql/pgsodium-volume`. Its private
`keyring` directory holds `pgsodium_root.key`; only that file is projected into
`/etc/postgresql-custom`. This keeps
the image's WAL, replica and extension configuration visible. A bounded
initializer creates a private key once and rejects unsafe existing key files.
Recovery validates and restores that one key file before restarting the
database. Losing the small encryption claim can make encrypted database values
unrecoverable even when the PostgreSQL data claim survives. This candidate
requires an operator-approved encrypted StorageClass for every claim; the
class name is an admission contract and must be qualified against the target
cluster's encryption-at-rest configuration. The two shared ReadWriteOnce
claims are safe only because admission requires one named node and every
component is pinned to it. Shared claims use one image-qualified storage GID
and supplemental group, so different container users cannot flip ownership.
The database's private data and key claims use its qualified database group.
The keyring directory has mode `0700`, and its key has mode `0600`.
Studio mounts Edge Functions read-only. This
candidate does not use host paths. A backup is
not complete unless one manifest binds the PostgreSQL backup and object-data
snapshot to the resource revision, platform release, image digests and content
hashes. Restore targets a separate empty Supabase resource.

## Renderer candidate

The renderer starts from the audited assets at upstream commit
`d6c81b66c9999cb121dd8876f541313d484f157f` and verifies the SHA-256 digest
of every reviewed asset. Hakopod changes the Compose-only Realtime hostname,
makes the isolated `pgbouncer` role own only the `_supavisor` metadata schema,
adds a separate owned Realtime post-migration step, and freezes the Edge Runtime
router and its exact JOSE dependency into a reproducible offline bundle. The
source, resolved JSR metadata and dependency license remain in the reviewed
inventory. Non-secret bootstrap and service files use immutable ConfigMaps. It deliberately does not run the
upstream Envoy entrypoint because that script substitutes credentials through
`sed`. A trusted secret-resolution step must instead create the immutable
`envoy-runtime-config` Secret snapshot containing `lds.yaml` before apply.

Run `scripts/build-supabase-edge-bundle.py` on the build VM to reproduce the
Edge bundle. It uses the digest-pinned runtime image twice with separate empty
module caches, requires byte-identical ESZIP output, fetches only the pinned
JOSE 6.2.12 metadata and license, and records all source and output digests.
The frozen bundle has also started in a container with networking disabled:
an unauthenticated request and a malformed bearer token were rejected with
HTTP 401, while a correctly signed token passed authentication and reached the
missing-function response with HTTP 404. This proves the bundled router can
authenticate requests without downloading dependencies at startup. It does
not prove Kubernetes NetworkPolicy enforcement, function execution, external
egress, or a full-stack deployment.

Database clients do not share the owner password. Auth, Edge Runtime,
PostgREST, Realtime, Storage, postgres-meta and Supavisor each receive a
separately scoped credential snapshot. A trusted bootstrap step must render
those same distinct role credentials into the `database-role-bootstrap`
Secret as `99-roles.sql`; the upstream shared-password `roles.sql` remains in
the audited inventory but is not mounted. Secret bodies never enter the plan,
ConfigMaps or renderer output other than Secret references.

The renderer requires an image-qualified non-root UID and GID for every pinned
component rather than guessing image users. It produces owned Deployments, a PostgreSQL StatefulSet, ClusterIP
Services, claims and default-deny NetworkPolicies. It requires the caller to
provide the namespace UID observed immediately before apply. The reconciler
must reject a changed UID and verify each object's complete owner chain. Only
the API gateway accepts traffic from a namespace labelled as Hakopod managed
ingress; the renderer creates no public route, and public routing must require
TLS. Kubernetes Service names match the upstream internal names.

Email signup and every non-empty SMTP secret reference are rejected by
admission until a bounded SMTP egress contract is implemented. Edge Functions have no external network access by default. An
operator may supply at most 16 reviewed CIDRs; the renderer permits only TCP
443 to unique, canonical public IPv4 `/24` or narrower and IPv6 `/64` or
narrower ranges. Private, loopback, link-local, metadata and default routes
are rejected. Temporary space is capped at 256 MiB per Pod and the Edge
Runtime Deno cache is capped at 1 GiB at `/var/cache/deno`.

Initial provisioning requires platform revision 1 and an observed absence of
the database claim. Once a database claim UID exists, rendering requires the
previous complete spec. The database name remains immutable. Runtime settings,
database TLS and gateway TLS may change through a reviewed revision. A JWT
expiry change updates the database setting in the same durable transaction and
completion journal used for database credential changes. Gateway
TLS and its Envoy configuration must change together. Database credentials
rotate only as one complete bundle containing the role bootstrap and every
database client reference. Reconciliation creates the immutable snapshots,
applies the bounded bootstrap transaction through the exact owned database pod,
records an immutable non-secret completion marker, rolls clients, verifies the
revision and then removes old snapshots. A retry may repeat the idempotent SQL
when the transaction committed before its marker was recorded.

Legacy JWT and API keys remain immutable until dual-key verification and token
retirement are implemented. The pg-meta, Realtime and pooler encryption keys
remain immutable until their stored data can be re-encrypted. Storage access
keys remain immutable until the object store can prove an overlap window.
`jwt.sql` remains first-boot-only; the database does not receive the JWT signing
secret. After a successful rollout, reconciliation prunes owned ConfigMaps
older than the reported revision threshold and owned Secret snapshots outside
the reported retain set. It never prunes before all workloads reference and
observe the new revision.

Readiness follows the upstream component health checks, including migrations
and dependency-aware endpoints. Startup and liveness use the local listening
socket, with startup allowing five minutes, so a database or downstream outage
does not cascade-restart healthy processes.

## Lifecycle and remaining qualification

The candidate includes review and accept API routes, PostgreSQL operation
leases and UID resource claims, authenticated encrypted runtime snapshots, a
bounded worker lane, and apply, observe, update, prune and foreground-delete
reconciliation. Existing objects without an exact current or immediately
prior UID claim or a matching pending creation intent are refused. Before each
Kubernetes Create, reconciliation durably reserves the exact external key and
places the unpredictable intent ID on the object. A retry may confirm only an
object carrying that exact operation-bound intent; a racing or merely
look-alike object still fails closed. Confirmation atomically records its UID
claim, and claims remain until namespace absence is observed during deletion.

The runtime configuration accepts only operator-supplied digest-pinned images,
qualified non-root identities, an approved encrypted StorageClass and bounded
public HTTPS CIDRs. It is a strict versioned TOML regular file with exact mode
0600, a one MiB limit, symlink refusal, bounded inventories and bounded secret
bytes. Secret snapshots are nested under their project and environment and
named with an immutable name and revision, so another project cannot retrieve
one by guessing its name. Secret bodies are authenticated and encrypted before
database storage and redacted from API reads.

VM validation passed the current renderer and lifecycle tests, the store and
API suites, worker tests and focused vet checks. The CLI, SDK and dashboard
also passed their checks. These development checks used the reviewed source;
they are not availability evidence. A database-only native preflight passed empty-volume
startup, role isolation, private encryption-key permissions and persistence
through pod replacement. It did not run the other ten components or test a
full platform restore. Full-stack native acceptance remains required.

Certificate routing and database public endpoints remain unqualified. The
gateway source validates its immutable TLS certificate and Envoy listener
configuration. The database-only native preflight reported PostgreSQL
`ssl=off` before the database TLS correction. The current source requires a
separate CA-signed database certificate, verifies its key and both database DNS
names, stages TLS files atomically, and configures PostgreSQL to refuse plaintext
TCP connections. Auth, REST and Storage URLs must verify the database hostname
against the mounted CA. Source tests and independent review passed for these
controls. The prepared full-stack source and input receipt are
review-window-bound and must be regenerated from the final source immediately
before the run; an older receipt must not be reused. These controls have not
yet passed native full-stack acceptance.

Supavisor is configured with a separately generated `pooler-api-jwt-secret`
for its administrative API, while NetworkPolicy denies application traffic to
that listener. Native proof that application JWTs are refused for tenant
changes remains part of the full-stack acceptance gate. The reviewed Supavisor
v2.9.12 source patch replaces its upstream `verify_none` database connection
with CA and hostname verification, and its digest-pinned OCI artifact has
reproducible source provenance. Its native database connection is still
unverified. Edge Runtime and Studio have no direct database route; native
NetworkPolicy proof for both is also pending. Verified database connections
from every direct client must pass before the platform can be enabled. A TLS
listener or source review alone does not resolve those dependencies.

Backup and restore have a separately reviewed implementation. Its durable cleanup
keeps a paused source recoverable after a worker crash or revoked authority,
and execution rechecks resource ownership before every command. Recovery v13
passed archive and cluster tests, 17 real PostgreSQL store/API test events
without skips, and vet on the isolated validation VM. These tests cover durable
operation and archive behavior; they do not prove a full Supabase stack restore.
Native backup/restore tests remain required. Database credential rotation and
recovery remain unavailable for release until their native lifecycle tests
pass.

The development-only native harness under
`examples/supabase-native-acceptance` refuses any context except
`k3d-hakopod-dev` and checks exact image digests. Before availability can
change, native acceptance must verify secure bootstrap,
JWT and role isolation, row-level security through PostgREST, Auth redirect and
signup rules, Realtime authorization, Storage upload and download, image
transformation, Edge Runtime isolation, Studio administrator isolation,
Supavisor connection bounds, TLS and plaintext refusal, restart persistence,
secret rotation and revocation, separate-resource backup restore, and complete
owned cleanup. Tests must use the exact candidate source and image digests.
