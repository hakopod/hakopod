# Resizing persistent volumes

The shared engine supports increasing and decreasing persistent filesystem volume sizes. Cloud hosted compute, self-hosted installations and Cloud-connected BYOD servers use the same maintenance API and dashboard controls. Shrinking uses an offline copy into a new volume; it never truncates a filesystem or asks Kubernetes to shrink a PVC in place.

## Using the dashboard

Open a service's storage section and choose **Resize**. Review the target capacity, affected services, downtime and temporary storage. The engine stops all services sharing the volume before measuring and copying data. The target must fit the files plus the greater of 10% or 64 MiB of free-space headroom. The review cannot promise that a shrink fits before this offline check.

The original stays intact while the helper copies and verifies file hashes, links and metadata. Only a verified copy can become the application's new volume. After the application is healthy and you have checked its data, explicitly confirm **Delete original volume** to reclaim the old allocation. Both copies remain charged until physical reclamation finishes. Existing backups are not deleted by this action.

The resulting application revision uses a new named volume and a `data` subdirectory. Update the volume and mount settings in your committed Git configuration before the next deployment. Reusing the retained original claim is blocked until its maintenance record is completed.

## Capacity and supported workloads

Targets are whole GiB from 1 through 200. Cloud applies the workspace storage allowance to the final target while reserving temporary capacity for both copies. The review's combined capacity is for these two volumes, not the entire workspace. The storage provider must also have enough physical free space; a quota reservation does not provision additional disks. Provider-specific capacity enforcement still applies: a local-path directory does not become a hard filesystem quota just because its PVC requests a smaller capacity.

Affected services must run one replica, with autoscaling and serverless scaling disabled. Shared volume users must have compatible UID, GID and filesystem group settings. Applications containing scheduled or deployment jobs, expiring previews and showcase applications are currently excluded. Block-device volumes are unsupported.

The helper runs without root, capabilities, service-account credentials or cloud credentials. It rejects inaccessible ownership, special files, unsafe set-id files, and inventories above 50,000 entries, 128 levels or 8 MiB of path names. Unsupported files stop migration while preserving the original. Some existing databases may need a maintenance window and ownership repair by their administrator before they qualify.

## Recovery

The database stores operation status, original and target identities, copy verification and immutable deployment revisions. Engine restarts resume that journal. Ordinary deployments, backups, restores and application deletion cannot race active maintenance. Every advancing step rechecks the initiating identity's current authority.

Before switching, **Cancel resize** stops the helper, resumes services on the original, waits for health and then reclaims staging storage. **Retry** retries a failed step. If a helper has not stopped, original services remain stopped until a subsequent retry or cancellation can safely resume them.

After switching, new writes may exist on the new volume, so cancellation and automatic rollback are unavailable. Retry startup, or choose **Keep both and end maintenance** to permit an ordinary corrective deployment while preserving both copies. Original deletion remains blocked until a current deployment succeeds and the engine observes healthy services. Replaced, missing or untrusted volume/helper identities stop maintenance rather than silently creating empty storage.

## Verification

Unit and PostgreSQL tests cover migration planning, quota accounting, maintenance exclusion, permissions and journal transitions. The independent [UI review](volume-resize-ui-review.md) records synthetic rendered evidence. The opt-in `TestLiveVolumeResizeShrinkGrowAndRejectOverflow` runs only against `k3d-hakopod-dev`; it checks shrinking, growing, preserved bytes/links, retained originals and safe rejection of insufficient target capacity. Passing local mocks is not evidence of a working storage provider or a production resize.
