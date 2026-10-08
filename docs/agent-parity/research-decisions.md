# Research behind agent control

Research date: October 8, 2026. This record connects public evidence and a competitor walkthrough to the implemented changes.
The evidence supports product hypotheses. It does not establish demand prevalence, willingness to pay or a usability advantage.

## Evidence and decisions

| Observed problem | Evidence | Decision in this change |
| --- | --- | --- |
| Deployment status can leave users unsure what happened. | [Coolify issue 7113](https://github.com/coollabsio/coolify/issues/7113) is a historical, now-closed report of unclear intermittent deployment failures. The Openship walkthrough also found that deployment history alone did not explain earlier runtime failures. | Preserve build completion, deployment outcome and current runtime as separate facts. Continue build polling until automatic deployment reaches a terminal state. |
| Agent connection can require manual steps across different interfaces. | [Dokploy issue 3470](https://github.com/Dokploy/dokploy/issues/3470) requests automated installation and initial MCP access. Openship provides client-specific commands beside connection and scope controls. | Document exact connection commands and authority boundaries. Add project, installation and host connection workflows with explicit prerequisites. |
| Useful agent access needs selectable authority. | [This operator account](https://news.ycombinator.com/item?id=47390174) describes useful read-only investigation and unreliable repair suggestions. [Another account](https://news.ycombinator.com/item?id=49904806) describes tightly controlled sysadmin access. | Require explicit grants for commands, SQL, terminals, credentials and administration. Recheck current authority during long-lived execution. |
| SQL and terminal access can complete tasks that a dashboard does not cover. | [This SQL workflow](https://news.ycombinator.com/item?id=47895657) uses MCP and a SQL client to inspect application data. Competitor documentation also exposes terminal and database tools. | Add bounded commands, terminal polling and managed SQL through the canonical API. Require engine-specific runtime evidence before enabling query execution. |
| A redacted secret can disappear during a full configuration replacement. | [Dokploy issue 4525](https://github.com/Dokploy/dokploy/issues/4525) describes this API/MCP update risk. This is not evidence of a Hakopod defect. | Retain canonical configuration and secret-reference workflows. Give credential retrieval its own grant and disclosure. Do not treat generic write permission as permission to read secrets. |
| A backup feature is incomplete without a usable restore path. | [Coolify issue 7529](https://github.com/coollabsio/coolify/issues/7529) requested restore/import, and [issue 7987](https://github.com/coollabsio/coolify/issues/7987) reports restore failures. These issues are closed and describe historical pain. | Include existing backup and recovery operations in the interface inventory. Preserve their canonical review, scope and lifecycle qualification requirements. Agent exposure is not new recovery proof. |
| Existing tools already satisfy some prospective users. | [A Dokploy user](https://news.ycombinator.com/item?id=48021654) reports satisfactory operation. [A Compose user](https://news.ycombinator.com/item?id=45481624) describes perceived switching cost. | Treat broad MCP coverage and Kubernetes as capabilities to evaluate. Do not claim that either feature alone makes Hakopod easier or worth switching to. |

OpenAPI addresses a separate implementation problem: repeated contract maintenance can cause interfaces to drift.
The shared contract now supplies operation discovery, schemas, permissions metadata, generated client types and proxy routes.
The [generation process](contract-generation.md) explains its checks and limits.
It does not generate authorization decisions or prove that a workflow succeeds.

## Openship walkthrough

The reference was [oblien/openship](https://github.com/oblien/openship), release `v0.8.2`, source `0b446ead07ccfb98a1c5111d1dfe04cae79f1ce5`.
The investigation used an isolated research guest and a synthetic application.
It included installation, deployment API experiments and an authenticated browser walkthrough.

Inspected UI paths included project creation, Git import, the app catalog, import guidance, deployment history, topology, services, logs, monitoring, health, backups, MCP connections and token scope.
Selected mobile screens were also inspected. This was a technical walkthrough, not a participant study or a timed product comparison.

The useful patterns were direct client connection instructions, explanations of missing prerequisites and a visible permission summary before creating access.
The investigation did not establish that Openship uses K3s. It also did not establish a general performance or usability advantage.
Catalog entries were not treated as proof of managed database lifecycle support.

## Oracle execution guarantees

Oracle's [autonomous transaction documentation](https://docs.oracle.com/en/database/oracle/oracle-database/26/lnpls/autonomous-transactions.html) states that transaction properties apply only within the transaction that sets them.
Its Example 7-48 invokes an autonomous function from `SELECT`. The function's committed insert survives rollback of the calling transaction.
[SET TRANSACTION](https://docs.oracle.com/en/database/oracle/oracle-database/26/sqlrf/SET-TRANSACTION.html) configures the current transaction.
These properties do not establish read-only protection against autonomous functions.

The Oracle query mode therefore requires explicit write permission and nontransactional execution.
It refuses read-only and transactional requests before runtime access and advertises no transaction guarantee.
Native Free acceptance confirmed autonomous persistence after parent rollback and after a function commits and raises an error.
The production query adapter reported the latter outcome as unknown.
Driver, limit, observed cancellation and cleanup checks also passed, as recorded in the verification record.
Query support is limited to the pinned Free 23.26 standalone configuration. Enterprise remains unsupported.

## Vitess transaction scope

The managed gateway retains Vitess SINGLE mode. The pinned [session implementation](https://github.com/vitessio/vitess/blob/0f1ed062dec171e0adfab796110549752901e299/go/vt/vtgate/executorcontext/safe_session.go#L545-L577) rejects more than one writable shard.
Vitess starts an [implicit transaction](https://github.com/vitessio/vitess/blob/0f1ed062dec171e0adfab796110549752901e299/go/vt/vtgate/plan_execute.go#L351-L391) for ordinary autocommit statements.
Its [DML implementation](https://github.com/vitessio/vitess/blob/0f1ed062dec171e0adfab796110549752901e299/go/vt/vtgate/engine/dml.go#L109-L113) permits the single-shard optimization unless an override applies.
Hakopod rejects the override that enables multiple shards. A nontransactional request does not grant cross-shard DML.

The contract therefore reports `transaction_scope=single_shard` and `cross_shard_dml=false`.
Reads can span shards, but this does not promise a globally synchronized snapshot.
The [DDL path](https://github.com/vitessio/vitess/blob/0f1ed062dec171e0adfab796110549752901e299/go/vt/vtgate/engine/send.go#L198-L204) commits the parent transaction. Schema changes can partially apply.
The SQL page states these limits before query entry. Native acceptance checks single-shard persistence and rejected cross-shard writes against complete dataset snapshots.

## What still needs customer evidence

The current positioning hypothesis is less operational effort with retained control.
The [2025 Stack Overflow AI survey](https://survey.stackoverflow.co/2025/ai) supplies broad context about trust and accuracy concerns.
It does not measure demand for Hakopod or the current market.

The next study should recruit small teams and independent developers who deploy applications on Linux VMs and use coding agents.
Include satisfied users of competing tools and users who restrict agents to reading data.

1. Observe a first healthy deployment, including database binding and domain setup.
2. Observe diagnosis of a failed deployment using real logs and runtime events.
3. Observe a restore into a separate target and verify application data.
4. Check whether users understand requested permissions, scope, expiry and revocation.
5. Measure repeated successful use and reasons to switch before expanding the marketing claim.

Record completion, blockers, retries, outside documentation, elapsed time and operator decisions.
Compare complete tasks rather than tool counts. No participant interviews, recruitment messages or willingness-to-pay tests occurred in this work.

The [verification record](../agent-control-verification.md) identifies implemented, tested and unverified behavior.
The [UI review](../agent-control-ui-review.md) records inspected routes, viewports, themes and interaction limits.
