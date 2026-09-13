# TOML schema v1

The CLI and dashboard submit one canonical Go application specification. Unknown
fields, unsupported versions, invalid names, duplicate or undeclared networks,
and dependency cycles fail validation. TOML is capped at 256 KiB. `init` writes
schema_version=1; project/environment come from CLI context or explicit CI flags,
and are never embedded Kubernetes namespace or credential settings.

Required application fields are `name` and a `services` map. Each service needs
an `image`. The minimum public service also needs `port` and `public=true`.
One-service applications use exactly the same format as groups.

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
| `size` | small; centrally defined resources below |
| `replicas` | 1; allowed 1–20; no scale-to-zero |
| `healthcheck` | Optional HTTP readiness path; a port otherwise gets TCP readiness |
| `env` | Explicit nonsecret variables, max128, each value max4KiB |
| `command`, `args` | Container entrypoint/arguments; not shell strings unless the image runs a shell |
| `depends_on` | Names of services that must become ready first, bounded by rollout timeout |
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

TCP startup checks are distinct from readiness. Hakopod never silently converts
an HTTP readiness check into liveness. Dependencies sequence rollout only;
application clients must retry normal runtime failures.

| Profile | CPU request / limit | Memory request / limit |
|---|---|---|
| small | 100m / 500m | 128Mi / 256Mi |
| medium | 250m / 1 CPU | 256Mi / 512Mi |
| large | 500m / 2 CPU | 512Mi / 1Gi |
| compute | 1 / 4 CPU | 2Gi / 4Gi |
| gpu | 2 / 8 CPU | 8Gi / 16Gi |

Profiles appear in `plan`. Advanced arbitrary resource overrides are currently
rejected. Rolling updates allow one additional replica with zero requested
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
