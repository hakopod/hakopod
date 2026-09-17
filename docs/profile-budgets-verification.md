# Profile budget increase

Small, medium, large and compute CPU/memory requests and limits are now 120% of
their previous values. CPU is represented in millicores; memory rounds up to a
whole MiB. GPU and explicit per-service resource values are unchanged.

The engine profile map remains the source for deployment, jobs, preflight and
plan resource facts. Namespace resource quotas now account for effective profile
budgets as well as explicit values and rollout overlap. Trusted Cloud quotas are
still applied last. Cloud's large-profile ceiling follows the engine, while
Hosted Free continues to forbid custom budgets and reserves its full memory limit.

Cloud obtains its small profile through the public engine accessor. Shared Free
quota headroom and the compute API's advertised values derive from that profile,
and the workspace card renders those API values instead of hardcoded numbers.

## Verification on 2026-09-18

- Full OSS Go suite passed with isolated PostgreSQL; focused profile, quota,
  resource-ceiling and capacity tests passed after the final quota adjustment.
- OSS dashboard production build and typecheck passed; Cloud composed dashboard
  build, typecheck and UI regression suite passed.
- Cloud full Go suite passed with isolated PostgreSQL, including the API allowance,
  matching resource ceilings, fixed Free restrictions and rollout headroom.
  The 37 Cloud Python tests also passed.
- Named development-cluster acceptance verified the increased small budget on
  actual running pods, unchanged explicit overrides, a successful deployment Job
  and a successful CronJob-created run.
- Two sandboxed Free workloads became ready with full 308Mi memory reservations.
  gVisor, HTTP ingress and cross-tenant/metadata/control-API isolation checks passed.

These are development-cluster and local-source results, not a production rollout.
Other profiles were checked through validation, capacity and budget tests; they
were not all separately load-tested on running pods. Visual review is recorded
in the Cloud repository's `docs/profile-budgets-ui-review.md`.
