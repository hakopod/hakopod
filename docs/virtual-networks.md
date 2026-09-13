# Virtual networks

Virtual networks let services in different applications communicate privately
within one project and environment. Each network contains named segments. A
project administrator grants applications access to a segment, then a deployer
connects specific services through their application configuration.

Development, staging and production have separate network scopes, even when
they use the same names. Create environments from the header's environment menu.
Each environment has its own applications, secrets and network grants.

## Create a network

Open **Networks → Create network**. Use the form or import a network TOML file:

```toml
schema_version = 1
name = "commerce"
description = "Private storefront and data services"

[segments.frontend]
applications = ["catalog", "storefront"]

[segments.data]
applications = ["catalog", "database"]
```

Review the previous and proposed configurations before saving. A grant permits
an application to join; it does not connect all of its services automatically.
The network inspector shows configured service connections, private DNS names,
ports and application revisions. Deployment status shows whether the latest
configuration has finished applying. Copy the actual address from this page.

The **Connect service** page adds membership to an existing application's
configuration, then opens a deployment review. It retains existing networks,
internet access and peer restrictions. To isolate a service from the internet,
remove its non-internal networks in application TOML and review that change.

## Connect services through application TOML

For the `database` application, add a local network pointing at the shared segment
and attach only the service that needs it:

```toml
[networks.data]
internal = true
virtual_network = "commerce"
segment = "data"

[services.postgres]
image = "postgres:17"
networks = ["data"]

[[services.postgres.ports]]
name = "sql"
port = 5432
target_port = 5432
protocol = "TCP"

[services.postgres.network_access]
from = []
from_applications = ["catalog/api"]
```

This excerpt describes networking; use the PostgreSQL catalog template for a
complete configuration with storage, resource limits and a password reference.
Hakopod resolves the image to an immutable digest during deployment.

The `catalog` application's `api` service must also join `commerce/data`:

```toml
[networks.data]
internal = true
virtual_network = "commerce"
segment = "data"

# Add this membership to the existing services.api definition.
# Include other local network names here if the service still needs them.
[services.api]
networks = ["data"]
```

An application's local network name can differ from the shared segment name.
Local networks without `virtual_network` remain private to that application.
Services can join several local or shared networks when their configuration and
grants permit it. Mounts and filesystem permissions remain independent service
settings; see [workload configuration](toml.md#named-volumes-and-filesystem-permissions).

## Peer and port rules

With no `network_access` block, a service accepts its declared ports from members
of its networks. An explicit block restricts ingress:

- `from` lists services in the same application. They must share a local network.
- `from_applications` lists exact `application/service` peers in the same shared
  segment. Wildcards and cross-environment peers are rejected.
- Empty lists deny those peer categories. Omitting `from` inside an explicit
  block also denies local peers.

Shared network access permits neither arbitrary namespace traffic nor undeclared
destination ports. TCP and UDP mappings enforce the container's `target_port`;
clients use the advertised `port`. Normal public ingress and readiness behavior
remain governed by the service's other settings.

Both source egress and destination ingress must permit a connection. Adding a
shared segment does not override another local peer restriction. Kubernetes
policies are additive, so infrastructure administrators must avoid installing
broader policies that permit traffic outside these rules.

## Permissions and changes

Project administrators manage network grants. Application-scoped deployment
keys can connect their application only to approved segments. Hakopod checks
grants during planning, durable acceptance and cluster deployment. These checks
also apply to configurations received through GitHub, GitLab or the REST API.

Updates and deletions require the reviewed network identity and revision. A
deleted and recreated network receives a new identity, so an old review cannot
change it. Revisions and changes are recorded in the existing audit/history store.

Detach services and successfully deploy that change before removing their grant
or deleting the network. Queued, running and failed releases may have partially
applied resources, so their references remain protected until a later successful
release retires them. A subsequent failed release does not revive connections
retired by an earlier successful detach. Rolling back to an old connection still
requires its current grant.

## API and limits

All paths below are relative to `/api/v1`. Reads use `project` and `environment`
query parameters; mutation bodies include that scope.

| Operation | Endpoint |
| --- | --- |
| List networks | `GET /virtual-networks` |
| Inspect connections and export TOML | `GET /virtual-networks/{name}` |
| List approved application/service choices | `GET /virtual-networks/{name}/candidates` |
| Review network TOML or structured configuration | `POST /virtual-networks/plan` |
| Create from a review | `POST /virtual-networks` |
| Update with `expected_id` and `expected_revision` | `PUT /virtual-networks/{name}` |
| Delete with reviewed identity, revision and name confirmation | `DELETE /virtual-networks/{name}` |
| Create a project environment | `POST /projects/{project}/environments` |

Application connections use the ordinary `/plan` and `/deployments` endpoints.
Network definitions accept either `toml` or `spec`, with unknown TOML fields
rejected. The generated [OpenAPI contract](../api/openapi.json) defines request
and response fields.

A project supports 32 environments. Each environment supports 64 virtual
networks, each with up to 16 segments and 64 granted applications per segment.
Definitions are limited to 32 KiB. The inspector returns at most 256 connections
and marks truncated results; connection choices contain at most 200 applications.
Inspection returns no environment values or secret contents. No separate network
daemon or unbounded discovery cache is added.

## Verification and boundaries

`TestLiveVirtualNetworksAcrossApplications` passed on `k3d-hakopod-dev` on
13 September 2026. Four isolated fixture applications verified private DNS,
TCP/UDP port mappings, allowed cross-application traffic, denied unlisted peers,
denied other segments/environments, and removal of peer access. The fixtures
were removed afterward. Database/API tests cover grant authorization, atomic
acceptance, stale revisions, replacement identities and safe grant removal.

This implementation uses Kubernetes NetworkPolicy and requires a CNI that
enforces it. It does not allocate subnet CIDRs or add traffic encryption, VPC
peering, cross-cluster routing or a service mesh. Live acceptance used IPv4 K3s;
other CNIs and dual-stack clusters require their own acceptance. See the
[Kubernetes NetworkPolicy model](https://kubernetes.io/docs/concepts/services-networking/network-policies/).
