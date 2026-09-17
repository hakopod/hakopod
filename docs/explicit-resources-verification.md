# Explicit service resources

Services now accept optional `resources.cpu_request`, `cpu_limit`,
`memory_request` and `memory_limit` strings. Omitted fields inherit `size`.
The shared engine validates quantities, positive bounded values and request/limit
ordering, and includes resource changes in immutable revision plans.

Deployments, deployment Jobs, CronJobs, preflight estimates and namespace CPU/memory
budgets consume the effective values. Quota budgeting covers rollout overlap and
retained prior services; trusted hosted-pool quotas are still applied last.
Cloud's gateway and engine retain the large-profile ceiling. The Cloud state
policy rejects custom resources on hosted Free before workload admission.

Compose reservations and limits, plus service-level CPU/memory aliases, convert
into the same fields. Compose byte units are converted explicitly, contradictory
aliases are rejected, and unsupported resource options are not discarded.
The shared dashboard edits, exports and reviews these values, including resetting
them to size defaults. Existing service-move and template API contracts were added
back to generation sources so API regeneration preserves the published contract.

## Verification on 2026-09-17

- Full OSS Go suite passed with isolated PostgreSQL, plus `go vet ./...`.
- Focused regressions passed for canonical TOML/JSON round trips, quantity bounds,
  inherited defaults, mutable-pointer isolation, revision diffs, Compose units and
  conflicting declarations, actual container resources, capacity calculations,
  rollout quota budgeting and managed-cloud ceilings.
- `TestLiveExplicitResources` passed on the named `k3d-hakopod-dev` cluster:
  Compose import produced two ready service replicas, a completed deployment Job,
  and a completed CronJob-created run. All actual pods had 125m/300m CPU and
  96Mi/192Mi memory requests/limits. Its owned namespace was deleted afterward.
  This acceptance test is also wired into native AMD64/ARM64 CI.
- OSS dashboard production build, typecheck and existing UI regression suite passed.
- Cloud full Go suite passed with a separate local PostgreSQL database, including
  gateway TOML/JSON validation and authoritative hosted-Free admission.
- Cloud composed dashboard production build, typecheck, UI regressions and 37
  Python tests passed.
- Independent visual coverage is recorded in [the UI review](explicit-resources-ui-review.md).

Local Kubernetes acceptance covered the named development cluster only. Cross-architecture
CI results, production deployment and a new release tag are not claimed by this local result.
