# Place a service on a node

Use **Run on node** when creating or configuring a service. Choose Automatic to
let Kubernetes select an eligible node, or choose the exact node name shown in
the picker. The same setting works in TOML, JSON deployments, CLI and MCP plans:

```toml
[services.api]
image = "ghcr.io/example/api:latest"
port = 8080
public = true
node_name = "worker-eu-1"
replicas = 2
```

`node_name` is the Kubernetes node name, not an IP address, VM ID or display
name. A pin applies to every replica of that service and also works for deployment
jobs and scheduled jobs. Other services can choose different nodes. Remove the
field to restore automatic placement.

The scheduler still enforces architecture, taints, resource requests, storage
constraints and the installation's allocation policy. A pinned service waits if
its node goes away; it does not fail over onto another node. Choose automatic
placement for services that should move between available nodes.

Plans reject unavailable/cordoned nodes, conflicting architecture and known CPU
or memory overcommit on the selected node. Capacity is an observation, not a
reservation: concurrent deployments and rolling-update surge still need headroom.
Cloud users can only select nodes allowed by their trusted runtime allocation.

Local volumes stay on their original node. A plan rejects a pin that conflicts
with a bound volume's node affinity, a pending volume's selected node, or two
conflicting pins sharing a single-node volume. Move the data through a reviewed
migration/restore before moving a stateful service. Removing a pin does not move
data automatically.

`GET /api/v1/placement/nodes?project=demo&environment=production&application=api`
returns only available placement choices and their availability/architecture.
It requires `deployments:write` in that scope. It does not expose infrastructure
addresses, host terminals or node metrics.
