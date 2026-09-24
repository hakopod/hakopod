# Delete a service and its unused volumes

Service deletion keeps persistent data by default. The deletion dialog offers
**Permanently delete unused volumes too** when the service has eligible storage.
It lists the exact claims before review. Shared named volumes used by another
service and backup archives are kept. The final confirmation explicitly includes
volume deletion; failed requests preserve the reviewed choice.

The shared plan/deployment API accepts `delete_service_volumes`, a bounded list
of services removed by that exact revision. Go derives the claim names from the
old and new specifications; callers cannot nominate arbitrary disks. The choice
is included in the idempotency hash and requires application-management access.

Cleanup is durable and begins only after the removal deployment succeeds.
Conflicting revisions and application deletion wait for accepted cleanup. The
worker rechecks current authorization, Cloud ownership and MFA before each step,
refuses volumes still mounted by pods, checks PVC/PV ownership, and waits for
both objects to disappear before releasing their storage reservations. It does
not delete the application's namespace, other services or native secrets.

The deployment page displays waiting, reclaiming, retained or deleted status.
Failed/cancelled service-removal deployments keep their volumes. Cleanup errors
remain visible; retry uses fresh authorization through
`POST /api/v1/deployments/{id}/volume-cleanup`. Failed reclamation keeps quota
reserved. Shared named volume definitions must be removed from the new spec
before those volumes can be reclaimed.

Tests cover consent-sensitive idempotency, post-success scheduling, competing
claims, membership revocation, shared mounts and quota release. The named-cluster
`TestLiveServiceVolumeCleanupPreservesOtherServices` additionally checks real PVC
and Retain PV reclamation, protection of the remaining service and idempotent
replay. Its results, rather than mocked client behavior, establish runtime
acceptance. See the independent UI review for rendered coverage.
