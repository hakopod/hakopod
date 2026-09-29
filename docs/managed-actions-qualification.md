# Managed Actions qualification

This record accompanies [PR #134](https://github.com/hakopod/hakopod/pull/134).
It separates product behavior, deterministic control-plane tests and real
workloads. Tests use disposable development resources; they do not certify a
customer installation or an arbitrary fleet size.

## Controls and operating bounds

- Four reconciliation workers operate within the API process. PostgreSQL owns
  due times, one-minute scheduling leases and fairness order. Application locks
  still protect provider and Kubernetes mutations. A reconciliation has a
  45-second deadline; a stale completion cannot replace a newer lease or a
  configuration change's wakeup.
- Runner management and workflow readers share a budget for each credential:
  one GitHub request per second, burst ten and two authenticated requests in
  flight. Provider retry deadlines are persisted in pool scheduling. Credentials
  are hashed for budget and cache keys; response bodies and tokens are not stored
  in retry errors. The supported configuration is one active Managed Actions
  API/controller process per installation. Budgets and cooldowns are
  process-local: multiple active processes would multiply local bursts and
  concurrency while still sharing GitHub's external quota. Scheduling leases
  support restart recovery; they do not establish active-active quota safety.
- Pools with empty slots are revisited after one second when they can progress;
  settled pools use a ten-second interval. Provider cooldowns take precedence.
  These are next-eligibility intervals; a worker backlog or credential cooldown
  can delay dispatch further. They do not establish a queue-to-start bound.
- Scoped runner inventories coalesce 100-item pages across pools. The cache
  permits 128 targets and 10,000 total records, including unfinished scans.
  Published snapshots expire 30 seconds after scan completion; a scan has
  45 seconds to finish, so individual pages can be older than the snapshot's
  publication time. Cache hits retain the original page observation time. A missing entry requires
  a fresh identity lookup. Oversized or slow inventories fall back to direct
  lookups with the same request budget.
- Retirement decisions use fresh provider state. A failed cleanup must check
  again before removing a runner that could have accepted a job. Explicit
  service deletion remains cancellation, as documented in the product.
- Service image observations come from running container status on owned pods.
  Empty, partial or failed observations cannot display the saved revision as a
  current image. A rollout can report multiple actual images while old jobs drain.
- Log grouping parses the retained window before paging and search. Rendering
  is limited to 1,000 rows per page with at most 32 ancestor levels. Downloads
  preserve the loaded raw text. Permission revocation removes output and stops
  retries and polling.
- Node discovery, pool readiness and pod creation honor the environment's
  trusted allocation and any selected node. Allocated nodes use exact object
  lookups, independent of global inventory pages. Admission and registration
  check runtime readiness, architecture and scheduling taints; pod creation
  rechecks them and pins the exact node without bypassing the scheduler.

## Control-plane qualification

The scheduler test uses real isolated PostgreSQL for 100 pools and 1,000
persisted slots, application locks and leases. GitHub responses are deterministic
fixtures and provider time is accelerated. With one shared credential and an
injected 60-second `429`, the batched implementation updated every slot using
36 requests: 12 inventory pages, 23 direct fallbacks and the rate-limited request.
The run advanced 110 seconds of simulated provider time; the oldest final
observation was 33 seconds old. Its 797 ms wall time is test execution time,
not production reconciliation latency.

The inventory-specific regression reads all 1,000 runners from ten provider
pages and verifies that cache hits do not refresh their timestamps. Other
regressions cover coalesced readers, resumable pagination, cache bounds and
direct fallback for an organization larger than the retained inventory limit.

The slow-pool scheduler test holds one pool while the other 99 finish and checks
that at most four workers run. Lease tests cover process interruption, old
completion rejection, a persisted one-hour cooldown and released application
locks after cancellation. These are control-plane failure tests; they do not
run 1,000 Kubernetes pods or GitHub jobs.

## Real GitHub workflow

[Run 36537231751](https://github.com/hakopod/hakopod/actions/runs/36537231751)
passed on 2026-09-29 using the product pod builder in `k3d-hakopod-dev`:

- A dedicated ephemeral runner checked out this repository and compiled and
  executed a native C program.
- The observer identified the assigned run, job and attempt, saw masked output
  while the pod was running, and retrieved the completed GitHub job logs.
- Both the observer host and real GitHub job concluded successfully. The
  observer test took 81.31 seconds, including runner startup and a deliberate
  30-second output probe; this is not a workflow latency benchmark.
- GitHub removed the one-job registration. The dedicated JIT repository secret
  and the local protected registration files were removed after verification.
  The CI observer used a read-only GitHub job token.

## Cache reuse across fresh runners

[Run 36541317231](https://github.com/hakopod/hakopod/actions/runs/36541317231)
passed at `8a84906` on 2026-09-29. One managed runner saved an 8 MiB payload
through the pinned official `actions/cache/save` action. A different managed
runner verified the cached files were initially absent, restored the exact cache key,
and checked both the first runner's marker and the payload SHA-256. Both jobs
concluded successfully and the disposable cluster was removed.

The current pinned runner used GNU tar 1.35 and gzip. Saving took 1.209 seconds
and restoring took 0.906 seconds from each action's first log to its success
message; each complete job took seven seconds. These are observations of this
fixture, not general cache latency guarantees. The temporary registrations,
JIT secret, exact test cache and local credential files were removed.

This verifies GitHub dependency/output caching across fresh jobs. It does not
verify BuildKit's separate `type=gha` layer exporter. The new image source adds
explicit tar/zstd prerequisites and an archive smoke test; the live result above
uses the existing gzip-capable engine pin, not an unpublished zstd image.

## Dashboard

The independent [UI review](actions-log-groups-ui-review.md) covers 22 rendered
route/theme/viewport cases plus interactive states: full and step logs, actual
service and topology image consumers, role revocation, polling, paging, search,
downloads and long lines. Dark and Paper themes were reviewed at desktop and
mobile widths, including 320px. These are explicitly marked artificial UI
fixtures; live provider behavior is covered separately above.

The independent [guided setup review](managed-actions-setup-ui-review.md)
covers creation, existing pools, multiple-pool selection and all four setup
steps in both themes at desktop and 390/320px mobile widths. Its 60 passing
case records include keyboard and touch controls, actual element bounds,
unavailable nodes, architecture mismatch, failed credential and deployment
requests, permission revocation and revision conflicts. The reviewed plan,
revision and idempotency key remain bound to deployment retries. Representative
final screenshots were inspected after all reported findings were resolved.

## Runtime and performance interpretation

The runtime harness and supported cross-build configuration are documented in
[Managed Actions](managed-actions.md#verification). A single runner's two
simultaneous BuildKit requests are not two concurrent GitHub jobs or fleet
throughput. A warm repeat within one builder is not a cache hit in a fresh
ephemeral runner. Startup timings must distinguish image/sandbox preparation,
workspace copying, Docker readiness and job assignment.

The existing per-slot resource split matters when comparing compilers: after
the 100m CPU and 512 MiB sandbox allowance, the runner receives one quarter and
Docker receives three quarters. A two-core slot therefore limits shell steps
to 475m and nested Docker work to 1425m. The total slot limit is not the CPU
available to either process on its own. Native and emulated compiler results
must state where the compiler runs.

Use native architecture pools or a compiler's native cross-compilation support
for expensive builds when possible. BuildKit user-space QEMU enables foreign
Dockerfile `RUN` instructions; it does not provide `binfmt_misc` or arbitrary
foreign-architecture job containers. Scoped remote registry caches can avoid
rebuilding dependencies between jobs without retaining another job's workspace.

## Deployment acceptance

Before an enterprise rollout, qualify the intended concurrency, job duration,
architecture mix, disk needs and GitHub credential arrangement on the target
node class. Include sustained job churn, node loss, recovery, quotas and actual
queue-to-start latency. The bounded development workload and deterministic
provider simulations do not establish those installation-specific results.
No operator installation or production runner pool was changed by this PR's
qualification work.
