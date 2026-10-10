# SyneHQ database explorer integration

The integration adds shared browser login, automatic source synchronization, and an **Explore databases** action for configured scopes. Kubernetes provisioning remains unqualified. Do not advertise automatic explorer installation until the runtime gate below passes.

## Configure a scope

Set `HAKOPOD_EXPLORER_TARGETS_FILE` on the engine. This versioned TOML file contains at most 64 targets. Unknown fields are rejected:

```toml
schema_version = 1

[[targets]]
scope = "demo/development"
project = "demo"
environment = "development"
url = "http://127.0.0.1:3100/synehq"
key_file = "/run/secrets/explorer-demo.key"
```

The target URL is operator configuration. A browser cannot supply or change it. Use HTTPS for remote targets. Loopback HTTP is allowed. The private key file contains 64 lowercase hex characters and has mode 0600 or stricter. Put the same control key in the matching OOS instance. Use separate keys, metadata, volumes, and containment boundaries for each scope.

OOS needs `OOS_HAKOPOD_SCOPE`, `OOS_HAKOPOD_KEY_FILE`, and `OOS_HAKOPOD_AUTHORITY_URL`. The authority URL ends in `/internal/database-explorer/authorize`. Use a digest-pinned `/synehq` image that contains the managed-session adapter. Normal root images and older prefix images do not provide this protocol.

A connected Cloud node also needs `cloud_workspace` in its target and `HAKOPOD_EXPLORER_CLOUD_AUTHORITY_URL` set to the fixed Cloud origin plus `/internal/explorer-authority`. The node validates each delegated human session through that fixed URL. It intersects the member grants with its own current machine-key grants. The shared Cloud runtime uses its existing in-process scope and membership checks.

## User flow

1. Open Databases in an explicit project and environment.
2. Select **Explore databases**. The action is shown only when a target is configured and the user can query databases.
3. Hakopod synchronizes ready managed sources before it opens the explorer. It refreshes the snapshot during active requests, at most once per ten seconds.
4. The explorer uses the existing Hakopod browser session. It does not ask for a second password.
5. Users with management permission can add other database connections. Imported sources are changed in Hakopod.

Each browser tab carries project, environment, and Cloud workspace in its URL. A workspace selection in another tab cannot change its target. The URL does not grant access. The engine checks the current human session and scope on every request and again before returning a buffered result.

## Connection and execution checks

The catalogue endpoint remains `GET /api/v1/database-explorer/connections?project=demo&environment=development`. It rejects missing, duplicate, or unknown query parameters. It requires `deployments:read` and `databases:query`. It contains no endpoints or credentials. `available` means that the operator configured a target; it does not prove service health.

The browser transport uses `POST /api/v1/database-explorer/http`. Its upstream is fixed by scope. It cannot forward internal control routes, authentication routes, redirects, caller headers, or arbitrary URLs. Bodies, results, tickets, and source snapshots have fixed size limits.

Before source sync, the engine loads the current database record and observes its runtime. A source needs a ready current revision, verified unexpired TLS, a usable private endpoint, and any required restore inspection. The engine opens credentials only for the private OOS sync call. OOS encrypts hosts and passwords with its existing keyring.

| Hakopod engine | OOS engine   | Private endpoint |
| -------------- | ------------ | ---------------- |
| PostgreSQL     | `postgres`   | `read_write`     |
| MySQL          | `mysql`      | `read_write`     |
| MongoDB        | `mongodb`    | `cluster`        |
| ClickHouse     | `clickhouse` | `https`          |
| Oracle         | `oracle`     | `read_write`     |

Repeated syncs preserve connection IDs. Changed credentials or endpoints change the source fingerprint. OOS increments its local revision and invalidates approvals. Deleted or unavailable sources are removed from the active snapshot. Manually added connections are preserved.

Each managed session carries the real actor. Query approval stays bound to that session, connection revision, and operation. Before credential resolution, OOS calls the engine again. The engine checks session revocation, current scope, source readiness, source fingerprint, and write permission. Cloud checks current membership and node binding too. Authority errors deny execution. Browser approval for writes remains required.

Cloud workspaces that require Cloud approval have read-only explorer access. OOS browser approval cannot replace a required Cloud review. Connection management is also disabled for these workspaces.

## Integration verification: 10 October 2026

The isolated development VM ran the updated OOS JavaScript and static files over the published AMD64 container below. This was a development image, not a new release image. It retained the normal cgroup, seccomp, non-root, and read-only filesystem controls.

- A real Hakopod browser session opened OOS without another login.
- Connection testing, saving, schema inspection, table reads, and SQL queries used native Kelvo. The SQLite fixture returned `42`.
- A restricted actor retained read access. Connection management and AI settings were disabled with visible help.
- Store tests covered encrypted hosts and passwords, stable imports, source rotation, removal, manual connections, actor audit, approval isolation, and revoked authority.
- Cloud tests covered delegated actors, foreign workspaces, revoked membership, and the read-only policy for required Cloud approval.
- The independent UI review covered desktop and mobile screens, Hakopod light and dark themes, keyboard access, and completed query results.
- The composed Cloud dashboard preserved its URL workspace when the workspace cookie selected another workspace. This tested the UI and proxy with a development engine fixture. It did not test the full Cloud service or tenant deployment.

Imported-source cards received source review only. Browser tooling did not support touch events. OOS retains its existing light theme and mobile tab scrolling behavior.

The native database fixture was manually added SQLite. It does not prove automatic import or network access to a provisioned Kubernetes database. Source import has contract and store test coverage. The Kubernetes deployment gate remains open.

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

## Release gate

Run the full engine and OOS suites, affected Cloud checks, and dashboard builds on the isolated development VM or CI. Test encrypted imports, idempotent sync, source rotation and deletion, actor audit, session revocation, read-only grants, foreign workspace denial, and asset paths through the scoped proxy.

The containment failure above still blocks automatic Kubernetes provisioning. Do not weaken cgroups, Landlock, seccomp, or non-root execution to pass it. A configured external OOS runtime must already have private database networking and valid containment. A source or explorer failure must not fail a managed database operation.
