# Sidecar containers

A service can run extra containers in the same pod, next to the service container.
They share the pod's network, so a sidecar can change how the service reaches the
network. The first use is a Tailscale sidecar that sends a Kelvo node's outbound
traffic through an exit node.

Status: implemented and covered by unit tests (validation, rendering, capacity and
quota, grants, strict node profile files, plan diffs). It has not been run on a
real cluster, so end-to-end behaviour, including Tailscale routing, is unverified.

## Declare a sidecar

```toml
[[services.node.sidecars]]
name = "tailscale"
image = "tailscale/tailscale@sha256:<digest>"
cpu = "100m"
memory = "128Mi"

[services.node.sidecars.env]
TS_USERSPACE = "false"

[services.node.sidecars.secrets.TS_AUTHKEY]
ref = "tailscale-authkey"
```

| Field | Meaning |
| --- | --- |
| `name` | Lowercase letters, digits and hyphens, at most 63. Unique in the service, not the service name, not `app`, `install-readiness-probe`, `install-guard`, `docker`, `prepare` or anything starting `hakopod-` |
| `image` | Required, and must be pinned by `@sha256:` digest |
| `command`, `args` | Optional, same limits as the service's |
| `env` | Plain variables, same rules as service `env`. Secret-looking values are refused |
| `secrets` | Variable to secret reference, same shape and checks as service `secrets` |
| `cpu`, `memory` | Required. Each is both the request and the limit, with the same format and bounds as the service's own resources |
| `mounts` | Optional, up to 8. Share a mount the service already has |

At most 4 sidecars per service. They are not available for Managed Actions,
serverless, session or job services.

A sidecar mount names exactly one source and picks its own `mount_path`:

```toml
mounts = [{ temporary = "/var/lib/ts", mount_path = "/var/lib/tailscale" }]  # a temporary_mounts path of the service
mounts = [{ volume = "data", mount_path = "/data", read_only = true }]       # a named volume the service mounts
```

A shared mount is visible to the service container too. Do not share a directory
that holds a sidecar credential unless the service may read it.

## How it renders

Sidecars are Kubernetes native sidecars: init containers with
`restartPolicy: Always`. They start before the service container, keep running
beside it and stop after it. Each gets the same restricted settings as the service
container: non-root, all capabilities dropped, no privilege escalation and the
runtime default seccomp profile.

Sidecar CPU and memory count exactly like the service container in plans, capacity
checks, namespace quota and managed reservations. Secret values are read once per
deployment into an immutable Secret owned by the application, so changing a secret
value rolls the pod.

## Privileges

A sidecar never gets more than the baseline from the TOML. Capabilities, a TUN
device or root come only from the operator's node profile binding, which names the
sidecar. See [node profiles](node-profiles.md#sidecar-privileges). The service must
reference that binding with `node_profile`, so it is pinned to one node and runs one
replica. Without a grant the sidecar stays restricted, and a Tailscale sidecar will
not be able to create its tunnel.

Previews and service transfers refuse services that use a node profile, and so
refuse any sidecar that has a grant. Plain sidecars follow the service.

## Logs and terminal

The terminal and command APIs accept a sidecar name as the container, and only for
a sidecar the service declares. Service logs still show the service container only;
the dashboard has no sidecar view. Read a sidecar's log with `kubectl logs -c <name>`
on the cluster.

## Example: route a Kelvo node through a Tailscale exit node

Operator, in the node profiles file:

```toml
[[bindings]]
name = "kelvo-node"
project = "platform"
environment = "production"
application = "kelvo"
service = "node"
node = "kelvo-1"
seccomp = "unconfined"
privileged = true
run_as_root = true

[bindings.sidecars.tailscale]
capabilities = ["NET_ADMIN", "NET_RAW"]
tun = true
run_as_root = true
```

Application:

```toml
name = "kelvo"

[secrets]
# optional application defaults

[services.node]
image = "ghcr.io/example/kelvo@sha256:..."
node_profile = "kelvo-node"
node_name = "kelvo-1"
replicas = 1
temporary_mounts = [{ mount_path = "/var/lib/ts", size_mib = 16 }]

[[services.node.sidecars]]
name = "tailscale"
image = "tailscale/tailscale@sha256:<digest>"   # replace with the digest you have reviewed
cpu = "100m"
memory = "128Mi"
mounts = [{ temporary = "/var/lib/ts", mount_path = "/var/lib/tailscale" }]

[services.node.sidecars.env]
TS_USERSPACE = "false"
TS_STATE_DIR = "/var/lib/tailscale"
TS_EXTRA_ARGS = "--exit-node=<exit node tailnet IP or name> --exit-node-allow-lan-access"

[services.node.sidecars.secrets.TS_AUTHKEY]
ref = "tailscale-authkey"
```

Notes:

- Save `tailscale-authkey` as an application secret first. Prefer a tagged,
  non-reusable or ephemeral key.
- `TS_USERSPACE = "false"` makes Tailscale use the kernel TUN device, which is why
  the grant needs `tun`, `NET_ADMIN` and `NET_RAW`. The pod shares one network
  namespace, so the service's outbound traffic follows the sidecar's routes.
- The state directory is an `emptyDir`, so the node key is lost on restart. Use an
  ephemeral auth key. The service container can see this directory too.
- `--exit-node-allow-lan-access` keeps pod and cluster-internal traffic local. Check
  that the exit node is approved in your tailnet.
- The digest is a placeholder. Choose and verify the official image digest yourself.
