# SyneHQ database explorer integration

Status: source catalogue implemented. Explorer deployment and automatic connection import remain incomplete.

The target is one explorer per authorized scope, under `/synehq/`, for self-hosted Hakopod and Hakopod Cloud.
The database list will show **Explore your databases** when the scoped service is ready.
This change does not expose that action yet.

## Source catalogue

`GET /api/v1/database-explorer/connections?project=demo&environment=development`

The caller must supply one project and one environment. The API rejects missing, duplicate, and unknown query parameters.
The caller needs `deployments:read` and `databases:query` in that scope.
Machine credentials must name the exact project and environment.
Application credentials cannot use this endpoint.
CLI and machine credentials need explicit query grants. An administrator wildcard does not grant query access.

The response contains at most 64 records. Each record includes:

- The database ID, revision, name, and engine.
- An `eligible` or `unavailable` source state.
- A reason for that state.
- A `can_write` hint based on the caller's current grant.

An eligible source has a ready observation for its current revision, verified TLS, and the required endpoint.
The observation must be no older than 30 seconds. A restored database must pass the existing inspection gate.
The endpoint returns no host, port, password, connection URL, or certificate. Responses use `Cache-Control: no-store`.

Eligibility does not prove that an explorer instance exists or that Kelvo can connect.
The execution path must check current permissions again. The `can_write` field does not approve a write.
The catalogue reads current managed records. It does not create a second registry or copy credentials.
Deleted records leave the catalogue. New revisions lose eligibility until their observations match.

## Engine mapping

| Hakopod engine | OOS engine   | Required private endpoint |
| -------------- | ------------ | ------------------------- |
| `postgresql`   | `postgres`   | `read_write`              |
| `mysql`        | `mysql`      | `read_write`              |
| `mongodb`      | `mongodb`    | `cluster`                 |
| `clickhouse`   | `clickhouse` | `https`                   |
| `oracle`       | `oracle`     | `read_write`              |

These mappings identify candidate sources. They do not certify full Kelvo connectivity for each managed topology.
Redis, Vitess, and DuckDB are outside this integration. Local SQLite is an OOS feature, not a managed Hakopod database.

Hakopod's current external-database registry accepts retained PlanetScale connections only.
It is not a general registry for databases that run on the same machine.
Adding arbitrary databases requires a separate scoped registration flow with verified TLS and encrypted credentials.

## Cloud boundary

The Cloud gateway requires both the member's current query grant and the connected node's query grant.
It checks the response schema and exact project and environment before forwarding the catalogue.
It rejects unknown fields, including accidental credential fields.
It limits `can_write` to the member grant, node grant, and eligible source state.

The Cloud runtime must select the explorer from authenticated scope, never from a caller-supplied address.
Each browser tab needs stable scope. A mutable workspace cookie cannot select a query target safely.
Separate Cloud scopes need separate data volumes, encryption keys, grants, and query history.
The session adapter must preserve the real actor and check membership on every request.

## Runtime probe: 10 October 2026

The test used a fresh isolated Azure VM and the named `k3d-hakopod-dev` context.
The cluster ran K3s `v1.35.8+k3s1` with containerd `2.2.7-k3s1` on an AMD64 host.
The test image was:

```text
ghcr.io/synehq/synehq-oos@sha256:45d605f24210e95c3df61a0b5b04b8fc46805f6741b54d6974b0a65490e54ed2
```

This image came from OOS revision `94113ed42e0b8ecf38ba4359fc0511b249a6a8a7`.
The pod used UID 65532, a read-only root filesystem, no added capabilities, and no service-account token.
Limits were one CPU and 768 MiB of memory.

The first probe rejected the root-owned `emptyDir` data mount.
A non-root init container then created a private data directory with mode 0700.
The next probe reached Kelvo and failed with `Application containment is unavailable`.
The published launcher requires a dedicated cgroup subtree and a custom seccomp profile.
A normal restricted pod does not provide that delegation.

This is a failed runtime qualification. Do not enable automatic deployment from this result.
Do not remove containment checks or mount writable host-wide cgroups to bypass the failure.
The probe did not test ARM64 Kubernetes behavior, database connectivity, shared login, or tenant isolation.

## Remaining implementation

1. Add a private OOS connection interface with source identity, revision, idempotent updates, suspension, and removal.
2. Encrypt imported hosts and passwords with the OOS keyring. Keep provisioning secrets out of browser requests and logs.
3. Add the Hakopod session adapter. Bind approvals to actor, session, database, connection revision, and exact operation.
4. Implement scoped runtime delegation for K3s. Preserve cgroup limits, Landlock, seccomp, and the non-root worker.
5. Publish and pin a `/synehq` image. Current normal GHCR tags use the root path.
6. Connect provisioning, credential rotation, deletion, restart recovery, and bounded retries to the private interface.
7. Add the database-list action and a scoped picker for registered databases.
8. Test both products, both architectures, permission revocation, and cross-tenant denial before release.

Explorer failure must not fail a managed database operation.
Show service failure separately once the database-list action exists.
