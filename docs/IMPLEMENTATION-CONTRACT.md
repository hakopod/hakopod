# Milestone 1 integration contract

The API prefix is `/api/v1`. Bearer keys are required except `/healthz` and `/readyz`. Success bodies are JSON; errors are `{ "error": { "code": "...", "message": "..." } }`.

Canonical spec is `internal/spec.Application`, JSON snake_case. `schema_version`, `name`, `services` map; service keys: image, port, public, size, replicas, healthcheck, env, command, args, depends_on, networks, secrets, autoscaling. Networks map supports `internal`. Zero port is a background worker; absent networks means default. Scope is separate from spec.

## API used by the dashboard and CLI

- `GET /me`: identity `{id,name,admin,permissions,project,environment}`.
- `GET /projects`: `{items:[{id,name,environments:[{name}]}]}`.
- `POST /projects`: `{name,environment}` (admin).
- `GET /applications?project=demo&environment=development&cursor=...`: `{items:[Application],next_cursor}`; max25 rows and512KiB of encoded spec/observation data.
- `GET /applications/{id}`: Application (with scope, desired spec, observed snapshot, metadata-only deployment summaries).
- `POST /plan`: `{project,environment,spec?,toml?,service?}` → `{application_id,expected_revision,spec,changes:[{service,field,before,after,sensitive}],warnings:[]}`. No infrastructure effect.
- `POST /deployments`: `{project,environment,spec?,toml?,service?,services?,expected_revision}` + required `Idempotency-Key` → HTTP 202 `{id,application_id,revision,status}`; stale expected_revision is 409. New applications start expected_revision=0.
- `GET /deployments/{id}`: Deployment including spec, resolved_spec, events, error, result.
- `GET /idempotency/{key}`: accepted deployment for the current identity, used by CLI retries; request input must still match.
- `POST /applications/{id}/rollback`: `{revision,expected_revision}` + Idempotency-Key → accepted deployment; rollback uses immutable resolved image digests.
- `POST /deployments/{id}/cancel`: boundary-aware cancellation.
- `GET /applications/{id}/logs?service=web&tail=100&follow=true`: bounded text stream.
- `GET /nodes`: `{items:[Node]}` using actual Kubernetes state; 503 explains absent cluster.
- `GET /keys`, `POST /keys`, `DELETE /keys/{id}`: admin scoped key lifecycle. Create `{name,project,environment,application?,permissions:[],expires_at}` returns key once. No credential values in list.
- `GET /audit`: admin recent audit metadata.

Application: `{id,name,project,environment,revision,status,spec,observed,created_at,updated_at}`. Deployment: `{id,application_id,revision,status,spec,resolved_spec,error,created_at,started_at,finished_at,events:[{id,time,type,message,service}],result}`. Statuses queued/running/succeeded/failed/cancelled; observed aggregate may be partial. Node: `{name,ready,unschedulable,architecture,kubelet_version,allocatable_cpu,allocatable_memory,pods}`.

## Ownership

Root: Go API/store/worker/server/CLI; root OpenAPI spec; main README and integration.
Cluster agent: internal/spec, internal/cluster and associated tests. No root dependency changes without notifying root.
Dashboard agent: web/**; generate TypeScript types from root API schema once ready; current contract supports initial UI. Verify current TanStack Start docs.
Infrastructure agent: deploy/**, scripts/**, examples/**, docs/decisions/**, docs/local-development.md, CI and local cluster setup. Use isolated cluster, never current orbstack context.

## Cluster package interface

`New(kubeconfig string, options Options) (*Client,error)` where Options holds AppDomain, IngressClass, RolloutTimeout.
`Resolve(ctx, spec.Application) (spec.Application,error)` resolves public image digests; private registry not falsely supported.
`Deploy(ctx, Target, func(Event)) (Observation,error)` idempotently applies owned resources, waits with bounded polling/watch and returns observed results. Target: Project, Environment, ApplicationID, OperationID strings; Revision int64; Spec spec.Application; Previous *spec.Application. Namespace derived deterministically with exported Namespace(applicationID string) string.
`Observe(ctx, Target) (Observation,error)`; `Nodes(ctx) ([]Node,error)`; `Logs(ctx,namespace,service string,tail int64,follow bool) (io.ReadCloser,error)`.
Event: Type,Message,Service strings. Observation: Status string, Services []ServiceStatus (Name,Status,Ready,Desired,Image,URL,InternalAddress,Message); Node as API fields above. Exact structs may be refined with coordination. Deploy must apply baseline policies before workload creation, drop capabilities, disable service-account tokens, use readiness, preserve HPA fields. Failures explain actual cause. No automatic rollback inside cluster package: root worker owns auditable recovery.

`spec.Parse([]byte) (Application,error)`, `spec.Normalize(Application) (Application,error)`, `spec.Diff(*Application,Application) []Change`; Change JSON fields as plan.

## Acceptance before calling M1 verified

Build/test strict specs and authorization; real PostgreSQL persists operations before 202 with duplicate/stale revision handling. Real cluster deploys web + private API; service discovery and denied cross-app traffic verified. Demonstrate image update, readiness failure with recovery, explicit rollback, and management restart. UI real data and node list verified in browser. Record gaps instead of claiming unrun tests passed.
