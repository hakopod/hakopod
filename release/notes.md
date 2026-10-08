Hakopod 0.1.0-alpha.58 adds operation discovery and scoped API calls to the CLI
and MCP, using the same versioned OpenAPI contract as the dashboard. Agents can
inspect permissions, request schemas and review requirements before a call.
Project, installation and host-only connections retain separate authority.
Writes, deployments, credentials, pod commands, SQL and terminals each require
their own explicit permissions and opt-ins. Personal login, consent and binary
uploads retain separate workflows.

Pod commands target one owned service pod and container. Commands have a
20-second deadline and 64 KiB limits on each output stream. An unknown outcome
does not prove the process stopped; check its effects before retrying. Application
and host terminal tools provide bounded polling with resumable output cursors.

Managed SQL queries are available through the API, CLI, MCP and dashboard for
qualified configurations. Queries have a 20-second deadline, at most 1,000 rows
and at most 1 MiB of results; defaults are 100 rows and 256 KiB. Writes require a
reviewed database revision. The dashboard adds Monaco SQL highlighting, keyword
completion and advisory grammar hints for PostgreSQL, MySQL and Vitess. Other
SQL dialects disclose unavailable grammar lint. Server validation remains
authoritative.

Users can delete empty projects and environments after confirming the target.
After owner setup, they can delete the empty demo project and create a project
with their chosen name and ID. Deletion revokes restricted keys, reserves retired
IDs and retains completed history. Personal account scopes remain protected.

Oracle query execution is limited to the pinned Oracle Database Free 23.26
standalone configuration with required TLS. It requires explicit write
permission and execution without a transaction, including for SELECT statements.
Oracle functions can commit outside the caller's transaction, so read-only safety
and rollback are not guaranteed. MyDuck uses its PostgreSQL endpoint and also
requires nontransactional write permission. Vitess data changes must target one
shard; reads may span shards, and schema changes can partially apply. Capability
discovery reports each database's modes and transaction scope.

Oracle Free native development acceptance passed, including NULL decoding,
bounded results, cancellation after observed execution, persistence checks and cleanup.
The verification record separates these runs from source coverage, dashboard
fixtures and other engine evidence. It does not establish production Cloud
availability or native acceptance for every operation in the discovery catalog.
See the [agent control guide](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.58/docs/agent-control.md)
and [verification record](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.58/docs/agent-control-verification.md)
for permissions, qualified configurations and remaining limitations.

Managed MyDuck and Oracle Free retain their single-instance, private-endpoint
provisioning model. Neither provides automatic failover. Oracle Enterprise,
Data Guard, public Oracle endpoints and arbitrary Oracle query images remain
unavailable. Oracle Free is proprietary software available at no charge.

Install this prerelease with:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.58/installer.sh -o installer.sh &&
sudo sh installer.sh --version 0.1.0-alpha.58
```

Upgrade an existing alpha.56 or alpha.57 installation with:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.58/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.58
```

Older installations need a supported intermediate release. The upgrade backs up
PostgreSQL and configuration, then restarts the management API and dashboard.
Retain those backups; replacing binaries does not reverse database migrations.

Hakopod Cloud has a separate package and rollout. This OSS release does not
establish production Cloud availability.
