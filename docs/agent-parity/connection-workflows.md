# Agent connection workflows

The OpenAPI `x-hakopod-agent` metadata classifies each operation. Operation discovery returns the exact contract schema, canonical permissions, connection boundary, review requirements and exclusion. Unknown operations fail closed. Availability describes source support; canonical API authorization still applies.

Project connections use one project and environment. Generic mutations require `--allow-write`. Reviewed platform mutations also require `--allow-deploy`; saved review, expected revision and retry-key checks remain canonical. Backup operations require a credential bound to the entire exact project/environment. An unscoped administrator connection cannot invoke them through this adapter.

Installation connections use `--installation --allow-admin`, an empty project/environment and explicit `admin,agent:admin` grants. Maintenance additionally requires the current installation owner. Credentials require `--allow-credentials` and `agent:credentials`; the extra grant does not replace the underlying resource permission. Create/rotate key and database credential responses contain secrets and must be stored securely by the caller.

Application terminal tools require both `--allow-exec --allow-terminal` and explicit `deployments:write,pods:exec` grants. Host terminal tools use `--host-only --allow-host-terminal` with a sole `nodes:terminal` credential and stored node grants, or `--installation --allow-admin --allow-host-terminal` with explicit installation authority. `login --host-only` asks the human to approve only their granted host access and stores no project scope. The host-only MCP connection offers only host terminal tools and rejects other access flags. Host access remains limited to the current owner with explicit installation authority or a stored node grant. CLI installation login includes `nodes:terminal` only when `--allow-host-terminal` was requested and approved in the device consent flow.

CLI credentials also require explicit grants for pod commands, application terminals and SQL queries. An administrator wildcard does not grant these capabilities. A default CLI login has no execution or SQL grant. Request the required permissions through device consent before execution.

`host_terminal_nodes` lists current stored grants and permitted nodes observed by the canonical API. Use terminal `open`, then `poll` with the returned session ID, then `input` with base64 input. Input is limited to 4096 decoded bytes. Poll output is a 64 KiB replay window with exact decimal cursors; `truncated` means the caller missed retained output. Sessions are limited to four, expire after ten minutes, and close after two minutes without input. Completed output is retained for thirty seconds. Close the terminal when finished. Existing browser streaming output remains supported, and a terminal can use only one output protocol.

The CLI uses polling for application terminals on both self-hosted and Cloud connections. The first successful poll confirms that output is attached. The CLI then permits input and resize requests. A truncated response stops the CLI session. Input requests are not retried.

`audit_export` returns one CSV page as text with a next cursor, capped at 1 MiB and 1000 canonical events. `hakopod api export exportUserAuditHistory --installation --allow-admin --query-json '{"identity_id":"..."}'` uses the same bounded decoder. CSV exports require the audit-history entitlement and current explicit installation authority. Generic JSON calls refuse CSV export.

Alarm read/acknowledge operations require an exact project/environment credential; the canonical incident check prevents crossing scope. Showcase reads are fixed to demo/development. Reviewed showcase removal uses installation administration with explicit write opt-in.

Deployment event sampling returns at most 100 records and 256 KiB over ten seconds, with an exact resumable cursor. Log queries use the bounded canonical log API. Neither interface invents runtime state.

The coverage inventory records literal dashboard call sites and contract metadata. Dynamic calls and alternate transports require manual review. The inventory is source evidence and must not be presented as production verification. Binary uploads and personal authentication/consent flows remain separate workflows.
