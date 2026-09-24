# Retained application data

Removing a service preserves its persistent volume. Deleting an empty application
also keeps persistent data by default, including native secrets. This protects
users from losing a database while changing their deployment configuration.

To remove that data permanently, select the data-deletion option while confirming
application deletion, or open the project's application list and use **Reclaim
data** on an already deleted application. Enter the exact application name to
confirm. Existing backup archives are kept; restore requires a usable backup and
its destination credentials. An empty application list still shows retained data.

The versioned API supports the same flow:

- `DELETE /api/v1/applications/{id}` accepts the optional `delete_data` boolean
  alongside the reviewed revision and application-name confirmation.
- `GET /api/v1/storage/retained?project=PROJECT&environment=ENVIRONMENT` lists
  retained data in the authorized scope.
- `DELETE /api/v1/storage/retained/{id}` with `{"confirm_name":"APPLICATION"}`
  queues reclamation and returns 202. Acceptance is not completed reclamation.

A durable worker checks current authorization before each runtime step. It checks
namespace and PVC ownership, verifies each backing PV's claim identity, changes
its reclaim policy to Delete, then waits for namespace and PV removal before
releasing storage reservations. Failures remain visible and keep quota reserved.
A new authorized request can retry after permissions or provisioner issues are
resolved. A missing namespace with an unmarked retained PV requires operator
review; the worker does not guess disk ownership. Cleanup inventory is bounded.

Hosted compute memory and persistent storage are separate reservations. Deleting
an application or reclaiming its data does not release its workspace's compute
allocation. Release compute separately after deleting applications and reclaiming
retained storage. Cloud checks current workspace ownership and MFA policy for
hosted reclamation; BYOD uses its scoped gateway and node authorization. Nodes
must run an engine version containing the retained-storage API.

## Verification

PostgreSQL tests cover explicit consent, cross-scope denial, competing worker
claims, failure/retry accounting and revoked authority. Kubernetes tests cover
ownership rejection and the missing-namespace retained-disk case. The opt-in
`TestLivePreviewDeletesOnlyOwnedRuntime` uses the named `k3d-hakopod-dev` cluster
for real namespace, PVC, PV and native-secret cleanup and idempotent replay.
The independent [UI review](retained-storage-ui-review.md) records rendered
coverage and distinguishes synthetic UI evidence from runtime acceptance.

On 2026-09-24 the PostgreSQL suite, both dashboard builds, and the named-cluster
cleanup test passed. The cluster acceptance used local-path storage; it does not
claim verification of every CSI driver or any customer disk deletion.
