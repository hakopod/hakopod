# Agent control through the API, CLI and MCP

The CLI and MCP use the versioned control API. API authorization remains authoritative.
The operation catalog comes from `api/openapi.json`. Dashboard and SDK types come from the same contract.

The contract classifies project operations, installation administration, dedicated execution tools and human consent flows.
Discovery lists the connection boundary, permissions, review requirements and prerequisites for each operation.
It also returns the operation summary and description from OpenAPI when those fields exist.
The [coverage inventory](agent-parity/coverage-matrix.md) records source coverage. It does not establish production availability.
The [research decisions](agent-parity/research-decisions.md) connect the work to public user reports and the Openship walkthrough.

## Discover and call operations

List operations without credentials:

```sh
hakopod api operations --family databases --limit 25
hakopod api operations --operation getManagedDatabase
```

Use the exact operation ID returned by discovery. Supply the returned cursor to read another page.
Inspect the operation parameters and body schema before a call.
Selected operations include the referenced schema definitions.

```sh
hakopod api call getManagedDatabase --project demo --environment development \
  --path-json '{"id":"DATABASE_ID"}'
```

For a mutation, add `--allow-write`. Supply `--body-file FILE` when the operation requires JSON.
Use `--idempotency-key KEY` when the contract requires it.
The canonical endpoint still checks permissions, immutable revisions and resource ownership.

MCP provides `api_operations` and `api_call` with the same catalog and scope checks.
The dashboard proxy routes come from the same OpenAPI exposure metadata.
HTTP MCP dispatches operations through the canonical runtime API.
Installation administration uses a separate connection. Credentials need an additional explicit grant and connection option.
Streams, terminals and SQL use dedicated tools. Binary uploads and human consent retain their separate workflows.
Deployments use the existing reviewed plan flow. Deployment opt-in also enables cancellation and rollback tools.

See [contract generation](agent-parity/contract-generation.md) for the generation process and drift checks.

## Separate permissions and opt-ins

| Action | API permission | CLI or stdio MCP option | HTTP MCP option |
| --- | --- | --- | --- |
| Generic mutations | Endpoint-specific permission | `--allow-write` | `allow_write=true` |
| Deploy, cancel or roll back | `deployments:write` | `--allow-deploy` | `allow_deploy=true` |
| Run a pod command | `pods:exec` | `--allow-exec` | `allow_exec=true` |
| Query managed SQL data | `databases:query` | `--allow-sql` for MCP | `allow_sql=true` |
| Change managed SQL data | `databases:query` and `databases:write-query` | `--allow-sql-write` | `allow_sql_write=true` with `allow_sql=true` |
| Open application terminals | `deployments:write` and `pods:exec` | `--allow-exec --allow-terminal` | `allow_exec=true` and `allow_terminal=true` |
| Use credentials | `agent:credentials` and the resource permission | `--allow-credentials` | `allow_credentials=true` |
| Administer the installation | `admin` and `agent:admin` | `--installation --allow-admin` | `installation=true` and `allow_admin=true` |

Each option enables only its action category. An option does not grant an API permission.
CLI and MCP resource inspection also require `deployments:read`. Include it when creating an execution key for those interfaces.
Machine keys require explicit execution permissions and a project/environment scope.
CLI credentials also require explicit execution permissions. An administrator wildcard does not enable machine or CLI execution.
Database query keys cannot have an application restriction.
SQL write grants require the SQL query grant.
Users must request and consent to execution permissions during device login.
Credential access can place secrets in an agent's context. Grant it only to trusted agents and integrations.
The [connection guide](agent-parity/connection-workflows.md) describes host-only access, stored node grants and bounded terminal polling.

## Pod commands

```sh
hakopod exec APPLICATION --project demo --environment development \
  --service api --pod POD_NAME --container app --allow-exec \
  --command-json '["/bin/sh","-c","printenv NODE_ENV"]'
```

Use the runtime API to obtain a current pod name. A command targets one owned service pod and container.
The API verifies the pod UID before execution. Commands use an argument array. A shell runs only when the command explicitly selects one.

The timeout is at most 20 seconds. Each output stream is limited to 64 KiB.
Commands accept at most 32 arguments. Each argument is limited to 4,096 bytes.
The process permits four concurrent pod commands. Excess output is discarded and marked as truncated.
Responses encode stdout and stderr as base64. A reported exit status means the transport observed process exit.

An `unknown` outcome does not establish process termination. Check the command effects before a retry.
Permission revocation closes the transport. Kubernetes does not guarantee that closing an exec connection terminates the process.
Audit records contain the command hash, target identity and outcome. They do not contain command or output bodies.

## Managed SQL queries

Read `getDatabaseQueryCapabilities` for the target engine before execution:

```sh
hakopod api call getDatabaseQueryCapabilities --project demo --environment development \
  --path-json '{"id":"DATABASE_ID"}'
```

`supported` identifies enabled execution. `read_only_supported` identifies server-enforced read-only execution.
The response also specifies parameter syntax, execution modes and transaction guarantees.
An unsupported engine or mode is rejected before connection. The current [verification record](agent-control-verification.md) lists qualified engines.

For PostgreSQL, save a query in a file:

```sql
SELECT $1::numeric, current_setting('transaction_read_only');
```

```sh
hakopod database query DATABASE_ID --project demo --environment development \
  --sql-file query.sql --parameters-json '["9007199254740993"]'
```

The dedicated CLI query defaults to read-only. CLI and machine credentials require explicit SQL grants. An administrator wildcard does not grant SQL access. For a write, add `--allow-sql-write --revision REVISION` and use both SQL permissions.
Read the database revision before the write review. The API rejects a missing or changed write revision.
The dashboard database detail page links to the query page. The page reviews the target and statement before a write.

Queries use the managed application identity over verified TLS. The response does not expose database credentials.
PostgreSQL must accept the statement with `EXPLAIN`. Transaction control, `COPY` and multiple statements are unavailable.
Some data-definition statements support `EXPLAIN`, including `CREATE TABLE AS`. They require write access.

PostgreSQL read queries use a read-only transaction. Transactional writes commit only after a final authority check.
Use `--execution-mode nontransactional` only when the capability permits it and the intended statement requires it.
MySQL and Vitess retain a read-only transaction for read requests. Their nontransactional option applies to writes.
Some engines commit DDL implicitly. Nontransactional changes can persist after a failure, cancellation or result-limit error.
An engine without read-only enforcement requires explicit write access, including for a SELECT statement.
The dashboard never changes a read request into a write request automatically.

The process permits four concurrent queries. Each query has a 20-second deadline, at most 1,000 rows and at most 1 MiB of results.
The defaults are 100 rows and 256 KiB. Transactional writes attempt rollback when a result exceeds the bound.
Read results may be truncated. SQL null stays JSON null. Other cells use text to preserve numeric precision.

Query errors include an operation ID and outcome after execution begins.
An `unknown` outcome means the server cannot establish the final write result. Check the database before a retry.
Queries do not retry automatically. Audit records exclude SQL text, parameters, credentials and result rows.

## Verification and remaining work

See [the verification record](agent-control-verification.md) for checks performed on this change.
Native tests require the named `k3d-hakopod-dev` context and disposable fixtures.
UI fixtures contain artificial data and do not prove database connectivity.

The contract inventory is complete for this source snapshot. Native and production acceptance remain separate checks.
Cloud application terminal polling has separate native acceptance. Read the verification record for transport coverage and Cloud limitations.
Marketing claims must identify released capabilities and their tested limits.
