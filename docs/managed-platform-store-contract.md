# Managed platform store integration contract

Migration 069 adds PostgreSQL-backed managed-platform resources, server-issued
reviews, operations, and external resource ownership claims. It adds no CRD,
controller, service, or external infrastructure.

Migration 073 adds durable CPU, memory, and storage reservations. Each row is
bound to a rendered workload or PVC and an approved node. Admission merges the
current and proposed envelopes, so failed updates and placement moves retain
both sides until reconciliation succeeds. A successful create or update
contracts the rows to the verified revision. A delete keeps them until owned
resources are gone and the delete operation succeeds.

`SaveManagedPlatformReview` stores the exact scope, resource ID, operation
kind, expected revision, desired spec, resolved plan, request hash, issuing
identity and key, expiry, and a canonical authority fingerprint. The
fingerprint covers key and identity scopes and permissions, applicable project
roles and custom-role revisions, administrator and MFA state, profile revision,
license revision, and organization-security revision. When capacity policy is
configured, the review also records its canonical fingerprint. Review creation
uses a serializable transaction. Acceptance takes the shared environment lock
and uses read-committed statements so a request that waited for an application
or database allocation sees that committed reservation before it spends the
same capacity.

`AcceptManagedPlatform` locks and consumes that persisted review in the same
transaction that advances the resource, inserts its immutable operation, and
writes the audit event. A fabricated, altered, expired, previously consumed,
cross-key, cross-scope, or authority-stale review conflicts. An idempotent
replay by the same currently authorized identity remains readable after review
expiry only when the immutable semantic request hash matches. Encrypted runtime
snapshots and authority fingerprints are not returned by API-facing operation
methods.

Create and update fail closed unless the server-resolved plan reports both
available and cluster-qualified. Delete preserves the current desired spec and
remains reviewable after qualification is withdrawn. Capacity admission also
requires a strict policy, exact node UIDs, the approved storage class, existing
durable reservations, and physical node headroom. These checks do not qualify
the database platform: its native lifecycle acceptance gate remains separate.

Managed platform acceptance uses the same environment lock and trusted Cloud
admission lock order as application and managed database acceptance. Platform
memory and storage count in application and database quota checks. Platform
CPU, memory, and storage also count in the managed database grant; the node
calculation follows the renderer, including Neon compute TLS sidecars and one
bounded runtime-memory allowance per pod, plus one reservation per PVC. A
changed policy fingerprint rejects a stale review.

Startup, acceptance, recovery acceptance, and every create or update
reconciliation recheck the current grant and the configured node UIDs. A pod
uses a durable platform envelope only when its namespace UID, controller UID,
one-replica rollout shape, scheduled node, labels, images, container names, and
resource requests match the claimed rendered workload. Pod requests and
runtime overhead are aggregated for that controller and node; anything above
the durable envelope counts as external workload usage.

Startup capacity discovery is bounded to 1,000 active project/environment
scopes and fails closed if that bound is exceeded. The 64-platform admission
limit remains per project/environment scope rather than applying globally.
Each capacity policy has an explicit `pool`. Admission takes a transaction-wide
lock for that pool and aggregates managed platform, managed database,
application, and retained-volume reservations across project and environment
scopes, closing concurrent cross-scope allocation races. The durable scope-to-
pool binding can only come from trusted operator policy, is backfilled during
startup for existing allocations, and cannot silently change or disappear.
The first trusted policy for a pool also records an immutable fingerprint in
PostgreSQL. Every later review, admission, and startup check must match it;
changing the physical envelope is refused for an existing pool. Existing scopes
cannot switch pools automatically. Capacity expansion or node replacement needs
an explicitly reviewed migration of the installation and its durable bindings;
this implementation does not provide that migration.
Per-project and per-environment tenant quotas remain separate from this shared
operator envelope. Cloud uses separately validated workspace pools whose
aggregate node and storage envelopes are checked before the runtime starts.

Workers call `ClaimManagedPlatformOperation`, then heartbeat immediately before
each external mutation. Claim, heartbeat, step recording, ownership claim,
advance, verification, and release repeat current authorization and the exact
operation lease and platform revision inside the same serializable database
transaction. Any change to the reviewed authority prevents reconciliation.
The deleting state remains eligible until the final successful step records the
soft deletion.

`PlatformResourceClaims` accepts only the leased operation's current revision
or its immediately prior revision. It returns unreleased claims ordered by
component and resource kind, repeats the exact lease/revision fence in SQL, and
fails closed above `MaxManagedPlatformResources`. `AdvancePlatformResourceClaim`
transfers only the immediately prior exact owner tuple. Resource creation uses
an immutable external ID and Hakopod ownership generation; provider generations
that can change belong in observations.

Observations, specs, plans, reviews, encrypted snapshots, list results,
attempts, concurrent workers, and resource claims are bounded. Operation
history remains inspectable after soft deletion.

The adapter does not infer availability from configuration alone. Runtime
qualification still requires the named development cluster and the exact
operator-reviewed images and identities.
