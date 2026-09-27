# Managed PostgreSQL and Redis

Managed databases have their own project and environment, resource allocation, credentials, revision history, operation progress and lifecycle. Application replicas do not control database replication. The PostgreSQL and Redis controllers own replication and recovery of failed members.

Managed databases are included in the self-hosted development release starting with `v0.1.0-alpha.37`. Provisioning requires the controller installation described below; upgrading Hakopod does not install those controllers. Cloud workspace admission, quotas, trusted placement and approvals require the corresponding Cloud integration and a separate operator rollout. This OSS release does not enable hosted provisioning.

## Database configuration

Use `Databases` in the dashboard, or a strict version 1 TOML file with the CLI:

```toml
schema_version = 1
name = "orders"
engine = "postgresql"
version = "17"
mode = "cluster"
replicas = 1
shards = 1
cpu = "500m"
memory = "1Gi"
storage_gib = 10
```

```sh
hakopod database create --project demo --environment production --file database.toml
hakopod database list --project demo --environment production
hakopod database show DATABASE_ID
```

PostgreSQL supports major versions 17 and 18, either standalone or with one to four replicas. Redis 8 supports standalone operation or three to sixteen shards with one or two replicas per shard. CPU, memory and storage apply to each member. A database has at most 48 configured members; an environment has at most 64 databases. Images are pinned by digest.

PostgreSQL exposes private read/write and, when replicas exist, read-only endpoints. Redis Cluster exposes private cluster endpoints and requires a cluster-aware client. Observations come from controller and database health checks; configured member counts are not reported as running members.

## Application connections

The database detail page can review a connection replacement, save it to the application specification and queue a redeployment in one transaction. It requires the application's exact name as confirmation. A review expires after ten minutes and becomes invalid when either revision or the recovery evidence changes.

```sh
hakopod database connection-plan DATABASE_ID --application-id APP_ID --service api --variable DATABASE_URL --endpoint read_write
hakopod database connect DATABASE_ID --review-id REVIEW_ID --name orders-app
```

Redis Cluster connections require `--endpoint cluster --cluster-aware`. PostgreSQL replica connections use `--endpoint read_only`.

The saved application specification contains a reference, not a password:

```toml
[services.api.bindings.DATABASE_URL]
managed_database = "DATABASE_ID"
protocol = "postgres"
endpoint = "read_write"
```

At deployment time, the worker verifies live database health and resolves application credentials into that service's environment Secret. Namespace and service network rules permit the connection. Removing a binding removes its grant. Application-scoped keys can redeploy an unchanged, previously authorized binding; adding or changing a binding requires project deployment permission. Referenced databases cannot be deleted.

## Resize Redis

Change the shard count in the database TOML, review the plan, and accept that exact plan:

```sh
hakopod database resize-plan DATABASE_ID --file database.toml
hakopod database resize DATABASE_ID --file database.toml --review-id REVIEW_ID --revision REVISION
```

The API checks actual Redis cluster health, complete slot ownership, the observed topology and a verified backup captured within the last hour. Acceptance and reconciliation recheck that evidence. A change cannot proceed using stale health or a backup from another database revision. Redis Cluster clients must handle topology changes.

## Backups and recovery

Configure an eligible S3 destination under Backups. Credentials are encrypted at rest; streamed PostgreSQL and Redis archives are age-encrypted, checksummed and read back for authentication before verification is recorded. Manual, hourly and daily schedules use durable backup jobs. Project-scoped destinations follow the existing endpoint restrictions.

Recovery requires a separate, unused database at revision 1. Choose Recover on that target, select a verified compatible archive, review the recovery point and confirm the target name. The target cannot already have an application connection.

```sh
hakopod database restore-plan TARGET_ID --artifact-id ARTIFACT_ID
hakopod database restore TARGET_ID --artifact-id ARTIFACT_ID --review-id REVIEW_ID --name target-name
```

The archive is completely authenticated before restoration. PostgreSQL restores run in one transaction, authenticated as the application role. Redis archives preserve values and absolute expiry and are consistent per shard, rather than one transaction across the entire cluster. Recovery supports individual Redis values up to 64 MiB; larger values are rejected explicitly. A failed Redis restore can leave the separate target partial; discard it and use a fresh target for another attempt.

Inspect the recovered data before acknowledging inspection. The acknowledgement is tied to the completed recovery job and the target revision:

```sh
hakopod database inspect TARGET_ID --job-id JOB_ID --revision REVISION --name target-name --inspected
```

Inspection does not connect an application. Use the reviewed connection replacement flow afterwards. The source remains available throughout recovery, and changes after the captured recovery point require a fresh capture before final cutover.

PostgreSQL upgrades use this same sequence: capture PostgreSQL 17, create a separate PostgreSQL 18 target, restore the logical archive, inspect it, then explicitly replace the saved application connection and redeploy. Same-major recovery and 17-to-18 upgrades are supported; downgrades and unknown source versions are rejected.

## Import Docker archives

Export an eligible PostgreSQL 17/18 custom archive with `pg_dump -Fc`, or capture a standalone Redis 8 RDB file. Hakopod imports the supplied file without contacting or modifying the Docker application. The operator supplies the original capture time; this is recorded as an attestation, not inferred from the file modification time.

The dashboard's dedicated import page supports files up to 64 MiB. The CLI streams up to 2 GiB, subject to the server's configured backup size limit:

```sh
hakopod database import-plan --file orders.dump --destination-id DESTINATION_ID --name docker-orders --engine postgresql --source-version 17 --captured-at 2026-09-27T10:00:00Z
hakopod database import IMPORT_ID --file orders.dump --name docker-orders
```

Review preparation calculates the raw file's SHA-256 and size. Upload rejects a different file, unsupported archive header or mismatched source major version. Redis format 12 alone is insufficient: the original `redis-ver` metadata must identify Redis 8. Verified imports become ordinary recovery artifacts. Continue with a separate target, inspection and explicit connection replacement.

Import reviews expire after 30 minutes. The server permits two concurrent uploads and bounds each request to 15 minutes. Upload leases, destination revision checks, current authorization and cleanup of interrupted staging objects prevent a retry from racing an earlier attempt. A completed retry returns the original artifact.

## Controllers and acceptance

The implementation expects CloudNativePG 1.30.1 in `cnpg-system` and the Opstree Redis operator built from upstream commit `c5017206e75f7743d79e82db47ec8c39d7410816` in `redis-operator`. That commit removes passwords from command arguments; the released 0.26.0 image can log them when a command fails and is not accepted. `Dockerfile.redis-controller` verifies the upstream archive checksum, runs its credential-handling regressions and builds the unchanged upstream source with pinned builder and runtime images.

Build the controller for the installation's architecture, publish it to the operator's registry, and retain the resulting image digest. For a registry serving both supported architectures:

```sh
docker buildx build --platform linux/amd64,linux/arm64 -f Dockerfile.redis-controller --tag "$REGISTRY/redis-controller:c501720" --metadata-file controller-build.json --push .
```

Use the resulting `repository:tag@sha256:digest` as `HAKOPOD_REDIS_CONTROLLER_IMAGE`. The development installer verifies downloaded manifest/chart checksums and configures a bounded 20-minute controller command timeout. Five minutes was insufficient for a real shard reduction on the bounded development worker. Admission checks the pinned image reference, source annotation, timeout and completed controller rollout before allowing database work:

```sh
HAKOPOD_TEST_KUBECONFIG=/path/to/development-kubeconfig HAKOPOD_REDIS_CONTROLLER_IMAGE="$REDIS_CONTROLLER_IMAGE" scripts/install-development-database-controllers.sh
```

This script only targets `k3d-hakopod-dev`; it must not be used against an operator cluster. Controller installation for other environments requires an explicit operator rollout with the same verified build, `hakopod.io/redis-controller-source` pod annotation and `EXEC_COMMAND_TIMEOUT=20m`. A supplied source annotation is an installation-operator attestation; customers cannot set it. No controller image is published by this source change.

Validation records distinguish unit/store checks, rendered dashboard review, real development-cluster acceptance and released availability. No production deployment or release is implied by passing development tests.

## Cloud workspaces

Managed databases share the workspace's compute and persistent storage allocation with applications. Admission serializes both kinds of reservation. Memory includes each database member, one extra member-sized working allocation for replacement or recovery, 50 MiB of sandbox overhead per member, another 128 MiB for recovery overhead and 256 MiB of shared application/readiness headroom. The review shows requested resources; quota errors include the required operational headroom. Failed or unfinished application changes keep their previous allocation reserved.

Shrinking Redis does not release storage for retained shard volumes. Confirmed database deletion changes the reclaim policy only for volumes bound to its exact owned claims, then removes the database namespace. Its reservation is released only after those volumes have disappeared; provisioning or storage failures keep deletion pending. Hosted compute cannot be released while databases remain. The single hosted worker supports recovery of failed database processes, but does not provide availability through worker or VM loss.

When workspace approvals are required, create, resize, delete, restore, inspection acknowledgement and connection replacement follow that review policy. Archive import metadata is approved before upload. Open the executed approval and choose **Continue archive upload**, select the exact reviewed file and confirm its checksum. The file itself is streamed without buffering it into an approval. A changed file or metadata requires another review.

Cloud supports raw archive uploads to hosted workspaces and directly reachable HTTPS BYO node APIs. Uploads through the BYO relay are unavailable; use the CLI against that node's direct API. Browser uploads remain limited to 64 MiB, and direct CLI uploads to 2 GiB.
