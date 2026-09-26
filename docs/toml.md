# TOML schema v1

The CLI and dashboard submit one canonical Go application specification. Unknown
fields, unsupported versions, invalid names, duplicate or undeclared networks,
and dependency cycles fail validation. TOML is capped at 256 KiB. `init` writes
schema_version=1; project/environment come from CLI context or explicit CI flags,
and are never embedded Kubernetes namespace or credential settings.

Required application fields are `name` and a `services` map. Each service needs
an `image`. The minimum public service also needs `port` and `public=true`.
One-service applications use exactly the same format as groups.

Application `[env]` defaults are passed to all services when top-level
`inject_env = true`; service values take precedence. Top-level `[secrets]` binds
shared secret references independently. Import-time `env_file = ".env"` (or a
list of filenames) works at application or service level and expands into
values/references before deployment. See [environment files and shared
variables](environment-and-build-reuse.md) for CLI, Git and dashboard usage.

Set replicas independently inside each service table:

```toml
schema_version = 1
name = "shop"

[services.web]
image = "nginx:stable-alpine"
port = 80
public = true
replicas = 2

[services.api]
image = "ghcr.io/your-team/api:1.0"
port = 8080
replicas = 3
```

Replica counts are not application-wide. Omitted counts default to one. Services
with persistent volumes and deployment jobs require one replica. Autoscaling,
when configured on a service, uses its own minimum and maximum replica counts.
Use `suspended = true` or the dashboard's Stop action to stop a service; keep its
`replicas` value so Resume can restore it.

An optional application-level `domains` table maps custom hostnames to public
HTTP services:

```toml
[domains]
"shop.example.com" = "web"
```

Create the application first, then verify each hostname in Custom domains before
adding it to a deployment. Hostnames use lowercase DNS names without wildcards,
schemes, paths or ports. At most 20 mappings are active at once. The platform
reserves verified claims for the application so historical rollbacks cannot take
another app's hostname. Removing a mapping removes routing, not its reservation.
DNS routing and certificate coverage are separate checks.

| Service setting | Default and meaning |
|---|---|
| `image` | Required HTTPS OCI reference; tag resolved and recorded as digest; optional scoped registry credentials |
| `port` | Main private TCP port, 1–65535; used for readiness and optional public HTTP |
| `ports` | Up to 15 extra private TCP/UDP endpoints, with a name, port and optional target_port |
| `public` | false; true adds public HTTP ingress and needs a port |
| `public_tcp` | Up to 16 explicit TCP listeners per service, 64 per application: port, target_port and up to 16 source_cidrs; self-hosted only, using administrator-provisioned ingress ports |
| `certificate_mounts` | Up to 4 service-owned certificate references with hostname and read-only mount_path; independent of HTTP TLS |
| `aws_identity` | Name of an operator-approved AWS workload binding for this exact service |
| `size` | small; default resource profile below |
| `resources` | Optional per-service CPU/memory requests and limits; overrides individual size defaults |
| `replicas` | Per-service saved count; defaults to 1, allowed 1–20 (Cloud: at most 3) |
| `suspended` | false; true stops the service and pauses autoscaling while retaining its saved replicas |
| `healthcheck` | Optional HTTP readiness path; a port otherwise gets TCP readiness |
| `readiness` | Optional declared-listener check: tcp, smtp or smtp_starttls; combined with healthcheck when present |
| `update_strategy` | rolling by default; recreate stops the old revision before starting its replacement |
| `env` | Explicit nonsecret variables, max128, each value max4KiB |
| `command`, `args` | Container entrypoint/arguments; not shell strings unless the image runs a shell |
| `depends_on` | Workloads that must become ready (services) or complete successfully (jobs) first |
| `job` | One-time deployment work with timeout_seconds 10–900 and retries 0–3; one replica, no listeners |
| `files` | Up to 16 total mounts: named read-only files with mount_path and content or a scoped secret reference |
| `bindings` | Up to 16 private HTTP/database URL environment bindings derived from service ports and scoped credentials |
| `http` | Up to four additional named HTTP endpoints on declared TCP service ports, with optional assigned custom domains |
| `backend_http2` | false; true makes the ingress speak HTTP/2 (h2) to this service, for gRPC and other h2-only servers; requires `public` and a `port`; rejected with `serverless` or with named `http` endpoints |
| `networks` | Omitted joins default; explicit nonempty list replaces default membership |
| `network_access.from` | Optional allowlist of services in this application; an empty list denies local peer traffic |
| `network_access.from_applications` | Up to32 exact application/service peers within a granted virtual network segment |
| `autoscaling` | Optional CPU HPA; min_replicas defaults1, target_cpu defaults70, max_replicas required <=20 |
| `architecture` | Optional amd64/arm64 image selection and node scheduling restriction |
| `run_as_user` | Default UID/GID10001; optional positive non-root UID, also used for volume group ownership |
| `run_as_group`, `fs_group` | Optional positive process and volume group IDs; default to run_as_user |
| `read_only_root_filesystem` | false; true makes the image filesystem read-only, with writable data and temporary mounts declared separately |
| `working_dir` | Image default; optional clean absolute directory |
| `termination_grace_seconds` | 30; optional 1–300 seconds for graceful shutdown |
| `registry_credential` | Name of a managed credential in this application's project/environment; no credential value |
| `secrets` | Up to32 environment-variable bindings such as `DATABASE_URL = { ref = "database-url" }` |
| `volume` | One persistent data directory: `mount_path`, `size_gib` (1–200), optional `storage_class`; one replica, no HPA |
| `mounts` | References application-level named volumes; each sets volume, mount_path, optional read_only and sub_path |
| `temporary_mounts` | Bounded scratch directories; mount_path, size_mib and optional memory=true for tmpfs |
| `gpu` | `count` from1–8; requires advertised NVIDIA GPU capacity; no GPU HPA |
| `tls` | Exactly one managed `certificate` or cert-manager `issuer`; public services only |
| `restart_nonce` | Opaque restart marker written by the restart action; at most64 letters, digits, `_` or `-` |

See [application lifecycle](application-lifecycle.md) for jobs, file permissions, connection wiring, named HTTP endpoints and preflight behavior.

TCP startup checks are distinct from readiness. Hakopod never silently converts
an HTTP readiness check into liveness. Dependencies sequence rollout only;
application clients must retry normal runtime failures.

| Profile | CPU request / limit | Memory request / limit |
|---|---|---|
| small | 120m / 600m | 154Mi / 308Mi |
| medium | 300m / 1200m | 308Mi / 615Mi |
| large | 600m / 2400m | 615Mi / 1229Mi |
| compute | 1200m / 4800m | 2458Mi / 4916Mi |
| gpu | 2 / 8 CPU | 8Gi / 16Gi |

Profiles are fixed per-replica budgets, not percentages of node capacity.
The small, medium, large and compute budgets were increased by 20%: previous
values multiplied by 1.2, rounding memory up to a whole MiB. `1000m` is one CPU
core. Requests reserve scheduler capacity; CPU limits throttle usage and memory
limits cap RAM. The total steady-state request is the effective per-replica
request multiplied by the replica count (maximum replicas for autoscaling).
Rollouts need extra capacity while old and new pods overlap.

New defaults apply when a service is next reconciled or deployed by an upgraded
engine; existing pods are not resized just by installing a new binary. Explicit
resource values keep their configured values and are validated against the new
defaults for omitted fields. Review the plan and node capacity before deploying.

Profiles appear in `plan`. Use a `resources` table to override individual values:

```toml
[services.api]
image = "nginxinc/nginx-unprivileged:alpine"
size = "medium"
replicas = 2

[services.api.resources]
cpu_request = "250m"
cpu_limit = "1"
memory_request = "512Mi"
memory_limit = "1Gi"
```

All values are quoted strings, per replica (or job attempt). CPU accepts cores
with up to three decimal places or integer millicores, from `1m` to `64` cores.
Memory accepts whole bytes or `Ki`, `Mi`, `Gi`, `Ti`, `k`, `M`, `G`, `T` units,
from `1Mi` to `256Gi`. `512Mi` means 512 mebibytes; `512M` means 512 million
bytes. Zero, negative, unlimited and ambiguous fractional-byte values are rejected.

Omitted or empty fields inherit `size` (which defaults to small). Requests must
not exceed their effective limits, including inherited defaults. A request above
a profile limit needs an explicit higher limit too. Changing `size` changes only
the values you have not overridden. Removing `resources` restores the profile.
The dashboard review, capacity checks, Deployments, deployment jobs and scheduled
jobs all use the resulting values. CPU autoscaling uses the effective CPU request.

Namespace budgets account for explicit resources and rollout overlap;
this does not reserve physical capacity. Cloud keeps its existing maximum large
profile budgets (600m CPU request, 2400m CPU limit, 615Mi memory request, 1229Mi memory
limit per replica). Hosted Free keeps its fixed small budget and does not allow
custom resource fields. Node availability and operator quotas still apply.

Compose import converts `deploy.resources.reservations.{cpus,memory}` to requests
and `deploy.resources.limits.{cpus,memory}` to limits. Service-level `cpus`,
`mem_limit` and `mem_reservation` are also supported; duplicate declarations must
agree. Compose `512m` means 512Mi and is converted to a Kubernetes byte quantity.
Omitted values inherit Hakopod's profile; imports do not create unbounded limits.
Unsupported device/PID reservations are rejected rather than discarded.

Rolling updates allow one additional replica with zero requested
unavailable replicas; actual availability still depends on capacity, readiness
correctness and application shutdown behavior. Pods have a 30-second termination
grace period by default, run as UID/GID10001, drop capabilities and receive no service-account
token. Images must support that unprivileged runtime. Persistent services use
Recreate updates; their PVC survives normal service removal and restart. Existing
volume size/class changes are rejected until explicitly migrated or expanded.

These fields use the same configuration in the dashboard, CLI, and repository
sync. The dashboard's Edit configuration action opens the whole application
TOML, including shared networks and volumes. Review the plan before deployment;
changing a shared definition can affect several services. Export includes
advanced fields even when a compact form does not show them.

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

To allow only one service to call the API, add:

```toml
[services.api.network_access]
from = ["web"]
```

The caller must still share a network. Omit this table to allow all members of
shared networks. Use `from = []` to block peer traffic. This does not remove an
explicit public HTTP route or the DNS egress exception.

Additional ports stay private. The service's image must listen on each target:

```toml
[[services.api.ports]]
name = "metrics"
port = 9090
target_port = 9091
protocol = "TCP"

[[services.api.ports]]
name = "discovery"
port = 5353
protocol = "UDP"
```

The first endpoint is `api:9090`, routed to container port 9091. Target port
defaults to port, and protocol defaults to TCP. Private policies apply to the
target ports and cover both service addresses and direct pod connections.

To share a private segment across applications, add `virtual_network` and
`segment` to an internal network definition. A project administrator must first
grant this application access in the separate network TOML. Attach selected
services, then use `network_access.from_applications` to restrict remote peers.
Network grants and connections are isolated by project and environment. See
[virtual network configuration and examples](virtual-networks.md).

## Approved private destinations (alpha.13 and later)

Self-hosted services can reference administrator-approved private destinations:

```toml
[services.api]
private_egress = ["orders-db"]
```

This is an excerpt to add to an existing service table. Grants are scoped by
project, environment, application and service. They allow only the approved
private CIDRs and TCP ports; a secret or environment variable grants no access.
They also work for jobs and internal-only networks. Managed Cloud rejects this
field. See [private database access](private-database-access.md) for administrator
configuration, limits, revocation and availability.

## Named volumes and filesystem permissions

```toml
[volumes.uploads]
size_gib = 10

[services.api]
image = "your-public-registry/api:1"
port = 8080
run_as_user = 10001
run_as_group = 10001
fs_group = 10001
read_only_root_filesystem = true
working_dir = "/app"
mounts = [
  { volume = "uploads", mount_path = "/app/uploads" },
  { volume = "uploads", mount_path = "/app/archive", sub_path = "archive", read_only = true },
]
temporary_mounts = [{ mount_path = "/tmp", size_mib = 32, memory = true }]
```

This is an illustrative image name. A mount's sub_path must be an existing
relative directory inside that volume. Mount paths cannot overlap or replace
protected system directories. Read-only mounts prevent writes through that
path; another writable mount of the same data can still change it.

A named volume defaults to `access_mode = "ReadWriteOnce"`. To share it between
services, set `access_mode = "ReadWriteMany"` and select an explicit storage_class
whose provisioner supports that mode. Services sharing the volume must use the
same fs_group. Hakopod does not install a storage driver automatically. The local
development provisioner supports ReadWriteOnce; shared multi-node storage needs
its own capable CSI provisioner.

Each application can declare 16 named volumes, with 20 total persistent claims
and 200 GiB across named and legacy volumes. Each service can declare 16 named
and temporary mounts. Temporary mounts total at most 128 MiB; memory-backed
mounts count against the container memory limit and disappear when the pod is
replaced. Disk-backed temporary mounts count against ephemeral storage limits.

Persistent services use one replica without autoscaling and Recreate updates.
Removing a mount or service retains its persistent claim. Rollback changes the
configuration, not the stored data. Existing claim size, storage class and access
mode cannot change through an ordinary deployment.

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

The API supports selecting several services in one request. Both
`POST /api/v1/plan` and `POST /api/v1/deployments` accept
`"services": ["api", "worker"]`. Supply the application's `spec` or `toml`
alongside `project` and `environment`; deployment also requires
`expected_revision` and an `Idempotency-Key` header.

Use either the existing `service` string or the new `services` array, never
both. Arrays must contain 1–20 unique, nonempty service names, all present in
the submitted configuration. Omit both selectors for an application-wide
deployment; an empty array is rejected to avoid accidentally deploying everything.
Selected services are merged into one application revision and their image tags
are resolved again. Unselected services keep their accepted configuration,
image digests and registry credential references. Shared application variables,
secret defaults, networks and volumes must be changed through an
application-wide plan. A selected group uses the normal rollout and recovery
rules; it is not an atomic transaction across running containers.

Example deployment body (replace the revision with the current application revision):

```json
{
  "project": "demo",
  "environment": "production",
  "expected_revision": 12,
  "services": ["api", "worker"],
  "toml": "name='backend'\n[services.api]\nimage='ghcr.io/example/backend:latest'\n[services.worker]\nimage='ghcr.io/example/backend:latest'"
}
```

Rollback uses a previous successful immutable spec and creates a new revision.
It does not undo external data changes. Application groups are not atomic;
deployment detail shows service results and failed-group recovery events.

## Secrets and persistent templates

Save an application-scoped secret before referencing it. Secret values are
write-only, never returned in configuration history, and must be referenced
explicitly by each service. Updating a value requires a restart/deployment.
The template catalog creates ordinary specifications using the fields above;
see [the cockpit guide](cockpit.md) for database, application and vLLM requirements.

Host ports, host networking, host-directory mounts, static IPs and privileged
containers remain unsupported. Unknown fields fail
rather than weakening policy. Do not put credentials in env, image URLs, TOML
or source; recognized credential names and URL userinfo are rejected. This
cannot detect arbitrary secrets disguised as ordinary values, and application
logs themselves can still contain sensitive data.

## SMTP and other public TCP workloads

See the [SMTP migration guide](smtp-migration.md) for public TCP, backend certificate
mounts and AWS workload identity. These optional fields preserve existing schema
v1 behavior: extra ports remain private, and `public`/`tls` still control HTTP.

Public TCP requires server deployment mode `self-hosted`. The administrator
selects this through `HAKOPOD_DEPLOYMENT_MODE` or `[server] deployment_mode` in
operator configuration, not an application file. Managed-cloud mode rejects
public TCP listeners regardless of roles or licenses; private TCP ports remain
available. Self-hosted administrators may provision any available non-platform
port from 1 through 65535, up to 256 per installation. Application TOML can use
only those provisioned ports and never opens host ports or firewalls itself.

## Listener readiness

Services may add a `readiness` table to check a declared TCP listener, SMTP
greeting and commands, or verified STARTTLS. If `healthcheck` is also set, both
checks must pass. See [listener readiness](readiness.md) for configuration, helper
installation, resource limits and the distinction from email deliverability.

## Node placement and serverless HTTP

Set `services.<name>.node_name` to an exact Kubernetes node name to pin that
service's replicas or jobs. Scheduler rules and volume affinity still apply.
See [node placement](node-placement.md).

An optional `[services.<name>.serverless]` table runs a public HTTP service at
zero or one replica with request-triggered wake-up. It accepts `min_replicas`,
`idle_seconds`, `startup_timeout_seconds`, `request_timeout_seconds`, and
`max_concurrency`. The installation must have an activation gateway enabled.
See [serverless configuration, limits and examples](serverless.md).
