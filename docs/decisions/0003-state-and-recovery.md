# ADR 0003: Durable operations and bounded management resources

Status: implemented for the combined API/reconciler process; failures and restart recovery require real acceptance results.

PostgreSQL owns desired application specifications, immutable revisions, accepted work, identities/key verification, and audit metadata. Kubernetes owns runtime state. Acceptance commits the operation and revision before returning 202. Idempotency keys bind to a request hash. Expected revisions prevent stale edits; a database lock serializes revision allocation and a per-application advisory lock prevents concurrent execution. A restarted worker can claim incomplete work and reapply owned resources. This is at-least-once execution with idempotent effects.

One Go process is the default. No Redis, external queue, retained metrics stack or centralized log store is required. Kubernetes calls, worker concurrency, request concurrency, streams, list lengths, query results and timeouts are bounded. Direct Kubernetes reads avoid a cluster-wide informer cache in this small installation. Future watch/cache adoption needs measured scale and explicit memory limits.

HAProxy sends application traffic straight to ready workload endpoints. It does not route through the management API or PostgreSQL. During a management outage, already-running workloads and routing remain available while the cluster is healthy. New deployments, authorization changes and management state updates stop. A PostgreSQL outage pauses durable management progress. A Kubernetes control-plane outage prevents scheduling and reconciliation, while existing data-plane traffic can continue subject to node/network availability. A node or ingress loss is a separate availability failure.

Deployment groups are not atomic. Services roll out with readiness gates, bounded dependencies and deadlines. A failed group records its failure; recovery re-applies the prior healthy artifact where available, and explicit rollback is a new revision using previously resolved digests. Neither path rolls back external databases or external secret values. Disconnecting a CLI does not cancel accepted work. Cancellation stops at explicit boundaries and can leave already-applied resources requiring a rollback.

Reconciliation and cleanup are restricted to resources labeled as owned by the application. Deployment selectors remain immutable. HPA owns replica counts when enabled; the application reconciler must preserve that field on updates. Secret-triggered restarts require one explicit owner before adding such an integration.

The API checks scope and permissions in Go on every resource access. Scoped deployment keys cannot administer infrastructure or keys, or directly read secret values. Their owner remains a trust boundary: deploying code can expose secrets explicitly injected into that authorized service. Revocation blocks new requests and is rechecked before further privileged reconciliation steps; it does not remove healthy existing workloads.

Sources:

- https://www.postgresql.org/docs/17/explicit-locking.html#ADVISORY-LOCKS
- https://kubernetes.io/docs/concepts/workloads/controllers/deployment/
- https://kubernetes.io/docs/tasks/run-application/horizontal-pod-autoscale/
- https://render.com/docs/deploys
