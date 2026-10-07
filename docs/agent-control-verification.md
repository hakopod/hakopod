# Agent control verification

Verification date: 2026-10-08, Asia/Kolkata. Base: `e45bcf285eabec001e9161878c6696ce7814e54f`.

This change adds a tested execution slice. It does not establish complete dashboard parity or production availability.
The shared catalog contains 325 contract operations. Generic invocation permits 121 and reports reasons for the other 204.
Some excluded operations already have dedicated tools or CLI commands. The counts describe generic invocation only.

Contract SHA-256: `3b4ffdc081901e1940bff572abcc2b346fa58a4549e9ee36c5fa75a8b49e4169`.

## Requirements and evidence

| Requirement | Implementation and evidence |
| --- | --- |
| Use one API contract | CLI and MCP share `internal/operations`. Dashboard and SDK types are generated from OpenAPI. Discovery returns the contract hash and referenced schema definitions. |
| Preserve configured scope | Shared invocation checks resources and injects scope. Tests cover nested references, foreign resources and application names. Global DNS administration is excluded. |
| Require execution grants | API handlers require explicit scoped machine grants. Store and device tests cover grant dependencies, consent, identity limits and revocation. |
| Keep action opt-ins separate | Deployment, generic write, pod execution, SQL read and SQL write options are independent. Tests reject build auto-deployment through generic writes. |
| Review database writes | CLI and MCP require a reviewed revision. The dashboard submits that revision. HTTP integration tests reject missing and stale revisions before cluster execution. |
| Bound execution | Four process-wide slots per execution category. Pod commands have a 20-second limit and 64 KiB per output stream. SQL has a 20-second deadline and row/byte limits. |
| Preserve exact data | Pod output uses base64. SQL null remains JSON null. SQL cells use text. API, MCP and CLI tests preserve numeric parameters above 2^53. |
| Report uncertain outcomes | Pod disconnects do not claim process termination. SQL commit transport loss reports unknown. The UI requires another write review after an unknown outcome. |
| Keep secrets out of evidence | Execution audits omit command bodies, SQL, parameters, credentials and results. PostgreSQL uses the managed application role with verified TLS. |
| Review the rendered UI | Independent [UI review](agent-query-ui-review.md) passed after fixes in both themes, desktop and mobile. Artificial UI data is explicitly labelled. |

## Completed checks

Final compilation and substantial tests ran on the VM. Early local checks stopped after disk exhaustion. Final local work used source edits, Python generation, formatting and inspection.

- Full `internal/operations`, `internal/agent` and `cmd/hakopod` tests passed on final source.
- Focused API tests passed: `Test(DatabaseQuery|HTTPMCP|PodExec)`.
- Focused store tests passed: `Test(AgentCapability|QueryRole|Device|ExternalDevice)`.
- Focused cluster tests passed: `Test(PostgresQuery|DatabaseQuery|PodExec)`.
- The final focused checks used a disposable PostgreSQL 17.11 instance. They included real transactions and HTTP handler authorization.
- A real `hakopod api operations --limit 1` call succeeded without a configured home or credentials. It returned the contract hash and counts above.
- Dashboard API generation, production build, typecheck, 69 server/library tests and 235 UI checks passed.
- SDK API generation, build and all 55 tests passed.
- `git diff --check` passed.

The focused command sequence is recorded in VM service `hakopod-agent-parity-focused-final`.
The dashboard and SDK sequence is recorded in `hakopod-agent-parity-web-complete`.

## Native development cluster

Tests used the explicit `k3d-hakopod-dev` context. They created their own resources.

`TestLivePodExec` passed in 5.91 seconds. It verified separate stdout/stderr, exit status 7 and refusal after authorization revocation.

The first SQL run failed because its port-forwarded connection could not reach the database listener.
The query implementation now uses a fixed byte relay inside the exact owned, pinned database container.
TLS authentication and SQL execution remain in Go. Command arguments do not contain SQL or credentials.

`TestManagedDatabaseQueryLive` then passed in 91.68 seconds. It verified:

- Managed application-role access over verified TLS.
- Exact large numeric values and SQL null.
- Bounded read truncation.
- Rejection of transaction control, multiple statements and COPY.
- Plannable DDL and a committed INSERT with readback.
- Read-only mutation rejection with unchanged data.
- Cancellation of a sleeping query with a one-second caller deadline.

The successful SQL run is recorded in `hakopod-agent-parity-native-sql-v2`.
The pod run is recorded in `hakopod-agent-parity-native`.
All native fixture namespaces, PVCs and associated PV claims were confirmed absent after cleanup.

## Release gate and limits

The initial full Go run found the numeric precision regression, which was fixed and passed in the final focused run.
That full run also reached the aggregate ten-minute API package timeout while progressing through existing authentication tests.
A full rerun with two package workers and a twenty-minute package timeout is pending at this record's initial creation.
The final focused checks and native SQL run include the later transport correction.

No merge, release or production deployment is claimed.
The public dashboard API proxy forwards a narrower operation set than the canonical API. HTTP MCP uses canonical dispatch.
SQL execution currently supports PostgreSQL. Other engines return an unsupported-engine response.
Native commit-response loss, physical touch, 320px layouts and complete dashboard parity were not qualified by this change.
Revocation checks cannot remove the network race between the final check and remote execution or commit.
