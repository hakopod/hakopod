# Agent control through the API, CLI and MCP

The CLI and MCP use the versioned control API. API authorization remains authoritative.
The operation catalog comes from `api/openapi.json`. Dashboard and SDK types come from the same contract.

This change adds scoped operation discovery, pod commands and managed PostgreSQL queries.
It does not provide complete dashboard parity. Discovery lists each excluded operation and its reason.

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
For broad CLI operation access, configure the canonical API origin. The public dashboard API proxy forwards a smaller operation set.
HTTP MCP dispatches operations through the runtime API and does not use that per-operation proxy list.
Generic calls exclude credential access, host administration, streaming responses, binary uploads and dedicated execution paths.
Deployments use the existing reviewed plan flow. Deployment opt-in also enables cancellation and rollback tools.

## Separate permissions and opt-ins

| Action | API permission | CLI or stdio MCP option | HTTP MCP option |
| --- | --- | --- | --- |
| Generic mutations | Endpoint-specific permission | `--allow-write` | `allow_write=true` |
| Deploy, cancel or roll back | `deployments:write` | `--allow-deploy` | `allow_deploy=true` |
| Run a pod command | `pods:exec` | `--allow-exec` | `allow_exec=true` |
| Read PostgreSQL data | `databases:query` | `--allow-sql` for MCP | `allow_sql=true` |
| Change PostgreSQL data | Both SQL permissions | `--allow-sql-write` | `allow_sql_write=true` with `allow_sql=true` |

Each option enables only its action category. An option does not grant an API permission.
CLI and MCP resource inspection also require `deployments:read`. Include it when creating an execution key for those interfaces.
Machine keys require explicit execution permissions and a project/environment scope.
An administrator wildcard does not enable machine execution.
Database query keys cannot have an application restriction.
SQL write grants require the SQL query grant.
Device login preserves its existing default permissions. Users must request and consent to additional execution permissions.

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

## PostgreSQL queries

Save a query in a file:

```sql
SELECT $1::numeric, current_setting('transaction_read_only');
```

```sh
hakopod database query DATABASE_ID --project demo --environment development \
  --sql-file query.sql --parameters-json '["9007199254740993"]'
```

The dedicated CLI query defaults to read-only. For a write, add `--allow-sql-write --revision REVISION` and use both SQL permissions.
Read the database revision before the write review. The API rejects a missing or changed write revision.
The dashboard database detail page links to the query page. The page reviews the target and statement before a write.

The endpoint currently supports PostgreSQL. Other managed engines return an explicit unsupported-engine error.
Queries use the managed application role over verified TLS. The response never exposes database credentials.
PostgreSQL must accept the statement with `EXPLAIN`. Transaction control, `COPY` and multiple statements are unavailable.
Some data-definition statements support `EXPLAIN`, including `CREATE TABLE AS`. They require write access.

Read queries use a PostgreSQL read-only transaction. Write queries commit only after a final authority check.
The process permits four concurrent queries. Each query has a 20-second deadline, at most 1,000 rows and at most 1 MiB of results.
The defaults are 100 rows and 256 KiB. A write whose result exceeds the selected bound is rolled back.
Read results may be truncated. SQL null stays JSON null. Other cells use text to preserve database precision.

Query errors include an operation ID and outcome after execution begins.
An `unknown` outcome means commit confirmation was lost. Check the database before a retry.
Queries do not retry automatically. Audit records exclude SQL text, parameters, credentials and result rows.

## Verification and remaining work

See [the verification record](agent-control-verification.md) for checks performed on this change.
Native tests require the named `k3d-hakopod-dev` context and disposable fixtures.
UI fixtures contain artificial data and do not prove database connectivity.

Complete parity remains a tracked product goal. Global administration and other excluded families need explicit scope design and acceptance coverage.
Support for MySQL, MyDuck, Oracle, Vitess and other engines requires engine-specific execution and cancellation tests.
Marketing claims about agent control must identify the released capabilities and these limits.
