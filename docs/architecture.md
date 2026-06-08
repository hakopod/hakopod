# Architecture and operational boundaries

Hakopod's first slice has two management processes: one Go API/reconciler and
a TanStack Start dashboard. PostgreSQL owns specifications, immutable releases,
authorization records, audit metadata and accepted work. Kubernetes owns runtime
state. HAProxy routes directly to application endpoints, independently of both
management processes and PostgreSQL.

## Durable release lifecycle

`POST /plan` normalizes the canonical specification and returns a reviewable
diff without writing infrastructure. `POST /deployments` requires an expected
revision and an idempotency key. A transaction locks revision allocation and
commits the release, queue entry, application desired revision and audit event
before returning 202. Identical duplicate submissions by the same identity
return the original operation; different input under that key conflicts.

The default queue has two workers. A session advisory lock serializes each
application across processes, including service-targeted and group releases.
Queued work runs in accepted revision order. Later releases do not implicitly
cancel earlier work. Each privileged stage checks the held database session as well as current authorization. Artifact persistence and terminal commits use that same session. Cancellation of requests already in flight remains best effort; this is not distributed exactly-once fencing. A lock is released when the database connection closes;
a replacement process resumes the same running record. Images are resolved to
digests once and persisted before applying Kubernetes resources. Re-execution
is idempotent, not exactly once.

Service-targeted updates merge into one coherent application revision under
optimistic concurrency. The CLI submits the canonical planned group to avoid
merging a retry into a newer state. A failed release retains per-service state
and events. The worker attempts to restore the complete last successful group.
Explicit rollback accepts a new auditable release from stored digests. Neither
recovery nor rollback reverses external database changes or secret values.
Groups are not atomic; partial service changes remain possible during failure
and recovery. Spare capacity is needed for one surge replica per service.

Cancellation is explicit. It prevents subsequent privileged steps and leaves
already-applied resources in place. Disconnecting the CLI only stops waiting.
Key/owner authorization is checked at acceptance and again during execution;
revocation stops new privileged work without deleting existing workloads.
Database loss cancels active reconciliation and leaves durable work resumable.

## Ownership and runtime observation

An application/environment has a deterministically derived namespace and stable
Kubernetes Services. Policies are installed before workloads. Public services
receive HAProxy Ingress; private services have no external route. NetworkPolicy
enforcement, limitations and host-network exceptions are in ADR 0002.

The reconciler updates only resources bearing the application ownership label.
HPA owns replicas when enabled. Application reconciliation preserves unrelated
pod-template annotations, including a future authorized secret restart owner.
Source pushes, completed builds and infrastructure changes use bounded durable
PostgreSQL inboxes in the same Go process. There is no external controller cache. Targeted rollout watches
are backed by periodic reads; runtime snapshots are refreshed in bounded
50-application pages, with a recorded observation time.

## Memory and load budgets

The Go process has a 192 MiB soft Go memory target, two workers, ten maximum
PostgreSQL connections, 64 concurrent HTTP requests, 16 log/event streams,
16 KiB stream copy buffers, capped API results and one-minute key usage updates.
Application pages contain at most 25 rows and 512 KiB of encoded specification/observation data. Deployment histories carry metadata only; full revisions are fetched individually.
The memory target is a GC setting, not an RSS guarantee or a Kubernetes limit.
Streams have deadlines and credential revalidation. PostgreSQL does not retain
application logs. Its workload is metadata and audit records; deployment history
currently requires operator-managed retention/backup before long-term operation.

The dashboard splits routes and keeps query cache retention at 60 seconds.
It pauses polling on hidden tabs and caps displayed log history. No topology
canvas, charting engine, Redis, Prometheus or log aggregation stack is required.
Optional retained observability belongs in a later measured profile.

Service charts retain at most 24 actual samples while open, fetched every 15
seconds, and discard their query cache when closed. Password hashing is limited
to two concurrent bcrypt operations. Source and automatic-build inboxes each
accept at most 1,000 pending items. GitHub Actions performs source builds outside
the management host; bounded result artifacts carry the resulting image digest.

Initial application limits: 20 services, 16 declared networks, 20 replicas per
service, 20 pending releases per application, and 200 applications per environment.
These bounds protect one-process development installations; they are not claims
of verified production capacity. Every resource profile is shown by `plan`.

## Dependency failures

| Failure | Running application traffic | Management impact |
|---|---|---|
| Dashboard process | Continues | Browser UI unavailable; API/CLI still usable |
| Go API/reconciler | Continues | New actions/streams unavailable; durable work resumes after restart |
| PostgreSQL | Continues | Authentication/acceptance stop; reconciler pauses safely |
| Kubernetes API | Existing routing may continue | Scheduling, rollout and fresh observations unavailable |
| HAProxy or its node | Public traffic can fail | Additional ingress pods alone do not make an endpoint HA |
| Worker | Workloads on that worker fail | Rescheduling depends on control plane and spare capacity |

One control-plane node and local PostgreSQL are not highly available. This
milestone has no tested production backup/restore or Ubuntu host installer.
Read the milestone tracker before deploying real workloads.
