# TOML schema v1

The CLI and dashboard submit one canonical Go application specification. Unknown
fields, unsupported versions, invalid names, duplicate or undeclared networks,
and dependency cycles fail validation. TOML is capped at 256 KiB. `init` writes
schema_version=1; project/environment come from CLI context or explicit CI flags,
and are never embedded Kubernetes namespace or credential settings.

Required application fields are `name` and a `services` map. Each service needs
an `image`. The minimum public service also needs `port` and `public=true`.
One-service applications use exactly the same format as groups.

| Service setting | Default and meaning |
|---|---|
| `image` | Required public anonymous HTTPS OCI reference; tag resolved and recorded as digest |
| `port` | Omitted for a worker; otherwise private container/service TCP port, 1–65535 |
| `public` | false; true adds public HTTP ingress and needs a port |
| `size` | small; centrally defined resources below |
| `replicas` | 1; allowed 1–20; no scale-to-zero |
| `healthcheck` | Optional HTTP readiness path; a port otherwise gets TCP readiness |
| `env` | Explicit nonsecret variables, max128, each value max4KiB |
| `command`, `args` | Container entrypoint/arguments; not shell strings unless the image runs a shell |
| `depends_on` | Names of services that must become ready first, bounded by rollout timeout |
| `networks` | Omitted joins default; explicit nonempty list replaces default membership |
| `autoscaling` | Optional CPU HPA; min_replicas defaults1, target_cpu defaults70, max_replicas required <=20 |

TCP startup checks are distinct from readiness. Hakopod never silently converts
an HTTP readiness check into liveness. Dependencies sequence rollout only;
application clients must retry normal runtime failures.

| Profile | CPU request / limit | Memory request / limit |
|---|---|---|
| small | 100m / 500m | 128Mi / 256Mi |
| medium | 250m / 1 CPU | 256Mi / 512Mi |
| large | 500m / 2 CPU | 512Mi / 1Gi |

Profiles appear in `plan`. Advanced arbitrary resource overrides are currently
rejected. Rolling updates allow one additional replica with zero requested
unavailable replicas; actual availability still depends on capacity, readiness
correctness and application shutdown behavior. Pods have a 30-second termination
grace period, run as UID/GID10001, drop capabilities and receive no service-account
token. Images must support that unprivileged runtime.

## Named networks

```toml
[networks.frontend]
[networks.backend]
internal = true

[services.web]
image = "your-public-registry/web:1"
port = 3000
public = true
networks = ["frontend", "backend"]

[services.api]
image = "your-public-registry/api:1"
port = 8080
networks = ["backend"]
```

These are illustrative image names. Services communicate on declared ports only
when they share membership; stable short names such as `api` resolve through
Kubernetes DNS. The web service above can use external egress because it also
joins frontend. API has only internal membership and loses general external
egress, while required DNS remains allowed. External egress does not allow
management/private CIDRs or metadata endpoints. Policies work on direct pod IPs
as well as Service IPs; no separate Docker bridges are created. This is the
documented IPv4 K3s model, not arbitrary Docker Compose compatibility.

## Staging, targeting and rollback

Saving local editor changes has no infrastructure effect. Planning returns the
normalized spec, warnings and configuration diff. Deployment requires that
plan's expected revision. A conflicting edit must be replanned. Group updates
and `--service api` share the same durable application queue.

Service-targeted plans preserve the latest accepted resolved images of untouched
services. If that base release has not resolved yet, wait before planning a
targeted update. This prevents a moved tag on an untouched service from silently
changing its artifact. The plan exposes any added digest pins. Other targeted
configuration is merged into the coherent accepted application revision.

Rollback uses a previous successful immutable spec and creates a new revision.
It does not undo external data changes. Application groups are not atomic;
deployment detail shows service results and failed-group recovery events.

## Explicitly unavailable in this milestone

Private-registry credentials, secret bindings, custom domains, host ports, host
networking, static IPs, privileged containers, persistent volumes and managed
databases are not supported. Unknown fields fail rather than weakening policy.
`[services.api.secrets] DATABASE_URL={ref="database-url"}` is a reserved future
shape and currently fails with a clear unavailable-binding error. Do not put
credentials in env, image URLs, TOML or source; recognized credential names and
URL userinfo are rejected. This cannot detect arbitrary secrets disguised as
ordinary values, and application logs themselves can still contain sensitive data.
