# Neon provider ownership requirement

Neon creation remains disabled until the pinned provider implements and
advertises `hakopod-ownership-v1` for tenant, timeline, pageserver registration,
and safekeeper registration mutations. A local pending intent, matching
configuration, or deterministic external identifier is not provider ownership.

Each Hakopod durable intent supplies a unique token in
`Hakopod-Ownership-Token`. The provider binds that token to the resource kind,
external key, canonical request hash, and exact response identity. Replays with
the same tuple return the stored response. Key/token/hash conflicts fail.
Describe returns the token. Delete requires it and leaves a tombstone.

Compute uses the durable create intent ID as its provider token. The token is
stored with the endpoint identity in the resource claim, remains stable when
`OwnerOperationID` advances, is bound to upstream `spec.operation_uuid`, and is
sent only on owned `/configure` and `/terminate` calls. Hakopod checks `/status`
when recovering an intent, when reusing a claim, during deletion preflight, and
again immediately before termination. No attached compute may be confirmed by
tenant, timeline, and configuration equality alone.

The compute ownership record is stored at
`/var/db/postgres/hakopod-ownership/record.json` on the compute PVC. Before the
provider starts, a credential-free init container running as the compute user
creates the parent with mode `0700`, or fails if the existing path is a symlink,
is not a directory, has another owner or group, or has another mode. It never
changes ownership or permissions on an existing path. The pinned provider v4
is still required to reject an unsafe or replaced parent when it opens the
record; the renderer preparation is not a substitute for that provider check.

The paired compute provider implementation is frozen separately. Storage
ownership remains incomplete, so creation and availability gates stay closed.
