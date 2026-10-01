# Managed platform store integration contract

Migration 069 adds PostgreSQL-backed managed-platform resources, server-issued
reviews, operations, and external resource ownership claims. It adds no CRD,
controller, service, or external infrastructure.

`SaveManagedPlatformReview` stores the exact scope, resource ID, operation
kind, expected revision, desired spec, resolved plan, request hash, issuing
identity and key, expiry, and a canonical authority fingerprint. The
fingerprint covers key and identity scopes and permissions, applicable project
roles and custom-role revisions, administrator and MFA state, profile revision,
license revision, and organization-security revision. Review creation and
acceptance use serializable transactions and current database authority.

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
remains reviewable after qualification is withdrawn. Current Neon and Supabase
plans report false, so this store does not expose either unfinished runtime.

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

The adapter does not qualify Neon or Supabase availability. Compilation,
database migration, runtime behavior, failure recovery, and real-cluster
acceptance remain to be verified on the named development environment.
