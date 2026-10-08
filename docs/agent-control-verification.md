# Agent control verification

Verification date: 2026-10-08, Asia/Kolkata. Platform base: `e45bcf285eabec001e9161878c6696ce7814e54f`.

This record separates source coverage, automated checks, native development acceptance and publication.
The feature changes are not yet merged or released. The website agent guide was published separately with released and in-development capabilities labelled.

## Contract and interface coverage

The OpenAPI contract contains 328 operations:

| Interface classification | Operations |
| --- | ---: |
| Generic scoped invocation | 171 |
| Separate installation invocation | 77 |
| Dedicated workflows | 33 |
| Human or protocol prerequisites | 47 |
| Unclassified | 0 |

The generated inventory found 367 literal dashboard calls and no unmatched literal contract routes.
Dynamic calls and alternate transports require manual review. Counts do not prove native or production parity.
The Go source inventory accounts for 340 literal API route declarations: 326 match OpenAPI and 14 have explicit exclusions.
CI rejects a new literal route without a contract or documented exclusion. Multiplexed and dynamic routes require separate review.

Contract SHA-256: `77faf89f6a7c29d75dec8a6e25e053dd4bfbc0356726809466b45eab6189b93b`.
See [the inventory](agent-parity/coverage-matrix.md), [generation process](agent-parity/contract-generation.md) and [connection workflows](agent-parity/connection-workflows.md).

## Implemented boundaries

| Requirement | Implementation and evidence |
| --- | --- |
| One contract | OpenAPI supplies the shared Go operation catalog, TypeScript types and generated dashboard proxy routes. Discovery returns schemas and the contract hash. |
| Explicit authority | Go checks current credentials, role, scope and resource ownership. Project, installation and host-only connections have separate authority boundaries. |
| Separate opt-ins | Generic writes, deployments, pod commands, SQL reads/writes, terminals, administration and credentials require their corresponding grants and options. |
| Credential control | `x-hakopod-agent` metadata supplies credential requirements to shared and Cloud checks. Write access does not imply credential access. Secret responses use no-store caching. |
| Reviewed writes | Database writes require a reviewed resource revision. Platform operations retain canonical plan, review, revision and retry-key checks. |
| Exact values | Pod output uses base64. SQL null remains JSON null. Query cells use text. CLI/MCP JSON decoding preserves integers above 2^53. |
| Bounded work | Pod commands and SQL each have four process-wide slots and a 20-second deadline. Pod output is capped at 64 KiB per stream. SQL caps results at 1,000 rows and 1 MiB. |
| Bounded terminals | Sessions have bounded input, output retention, duration and idle time. Polling uses exact decimal cursors and reports missed retained output. Trusted product scopes survive creation requests. Active streams recheck current keys, product authority and required permissions. |
| Accurate uncertainty | Disconnected pod commands do not claim process termination. SQL reports unknown unless the final result or rollback is confirmed. No automatic command or query retry is added. |
| Current Cloud authority | Workspace identity, current role, delegated credential and node credential restrictions intersect. Delegated callers cannot approve their own requests. |
| Sensitive evidence | Audits omit command bodies, SQL, parameters, database credentials and result rows. No fixture evidence establishes production behavior. |

## Automated checks

All compilation and artifact-producing tests in this continuation ran on bounded VM snapshots or CI runners.
Source edits, source generation, formatting and inspection ran locally. Snapshots copied files and did not hard-link mutable source.

| Snapshot or unit | Result and scope |
| --- | --- |
| `hakopod-agent-readonly-final` | Platform production build, typecheck, 72 server/library tests, 238 UI tests and 55 SDK tests passed. Includes capability-driven read-only UI and unavailable-scope refusal. |
| `hakopod-agent-final-descriptions` | Final OpenAPI/proxy generation and exact SDK generation check passed after agent-language corrections. |
| `hakopod-agent-interface-final-v2` | Earlier full Go run passed except an obsolete unsupported-MySQL assertion. Corrected cluster tests passed separately. Later rollback changes require a fresh full run. |
| `hakopod-agent-bcf36b48-full-go` | Full `go test -count=1 -p=1 -timeout=20m ./...` passed on the committed archive and exact templates submodule. Later terminal changes require separate checks. |
| `hakopod-agent-terminal-runtime-v5` | Focused scope, stream guard, terminal authority, polling and buffer tests passed. |
| `hakopod-agent-terminal-final-v8` | Focused race tests and native terminal polling passed in 104.264 seconds. Includes explicit machine grants, runtime narrowing, product revocation and confirmed namespace cleanup. Later CLI consent and attachment changes require a separate result. |
| `hakopod-agent-terminal-final-v9` | Focused race tests passed in 122.325 seconds. Includes current CLI/machine grants, administrator wildcard rejection, active grant removal, attachment failures and SQL authorization. Native terminal input/output, exit, product revocation and namespace cleanup passed in 75.96 seconds. The first input request succeeded immediately after the first poll, without retry. |
| `agent-cli-terminal-poll-20261008` v3 | CLI polling protocol tests passed in 0.192 seconds. Covers cursor progression, output bounds, truncation, exit status, input readiness, rejected input and DELETE cleanup. |
| `agent-cli-terminal-outcome-v4-20261008` | CLI polling tests passed in 0.171 seconds. Invalid remote exit codes report an unknown process outcome. Valid exit codes remain unchanged. |
| `agent-sql-grants-final-20261008` | SQL API authorization and scope tests passed in 2.039 seconds. Writes require both current SQL grants. CLI and machine administrator wildcards cannot replace either grant. |
| `hakopod-sql-execution-mode` and columns outcome v2 | All five focused driver tests passed after explicit nontransactional execution and column-failure outcome fixes. Read-only requests retain server-enforced transactions. Nontransactional failures do not claim rollback. |
| `agent-route-inventory-20261008` | Source inventory and three drift-check regressions passed. Covers new routes, missing exclusion reasons, obsolete exclusions, documented exclusions and comments. |
| `agent-contract-schema-v3` | Full operations suite passed in 5.406 seconds. The compiled JSON Schema validator covers unions, exact one-of semantics, conditionals, references with sibling constraints, exact numeric bounds, Unicode length, collections, closed object extensions and open webhook payloads. External schemas are refused. Error redaction and independent input bounds passed. Dashboard and SDK types were regenerated with openapi-typescript 7.13.0. |
| `agent-contract-paths-v4` | Full operations suite passed in 6.136 seconds. Every route variable has one required path declaration. Generation rejects duplicate and invalid declarations. Dashboard and SDK types were regenerated with openapi-typescript 7.13.0. |
| `agent-contract-summaries-v5` | Full operations suite passed in 2.885 seconds. All 328 operations have explicit action summaries. Generation rejects missing or obsolete summary entries. Independent source review checked action semantics, asynchronous outcomes and credential wording. Dashboard, SDK and editor schemas were regenerated on the VM. |
| `agent-contract-summaries-v6` | Full operations suite passed in 1.997 seconds after clarifying queued notifications, proxy changes, database bindings and volume resize actions. Dashboard, SDK and editor schemas were regenerated on the VM. |
| `agent-catalog-semantic-v2` | Full operations package tests passed in 2.280 seconds. Discovery preserves the OpenAPI summary and description in operation pages and details. Absent descriptions remain absent. |
| `agent-dashboard-drift-20261008` | The dashboard regeneration command and exact generated-file comparison passed on a disposable VM snapshot. CI now rejects changes to dashboard API types or editor schemas that were not regenerated. |
| `agent-oracle-decoder-limits-v5` | Nine adversarial decoder tests passed. The query-only receive limit checks packet lengths and accumulated values before allocation or append. It also limits cumulative wire reception. Management connections retain the upstream default. Six SQL and receive-limit mapping tests passed separately. Native Oracle execution remains separate. |
| `agent-oracle-private-diagnostics-v3` | Driver tests passed in 0.003 seconds and network receive-limit tests passed in 0.005 seconds. A synthetic nonempty warning decoded and consumed exactly its frame without standard output or standard error. Independent review confirmed that removing unconditional diagnostics preserves protocol consumption and returned errors. Manifest SHA-256: `0cd9d50af447ec30d2d3d89fc944deca7e49b3cfe5b796b675dbe23cdbf66205`. |
| `agent-oracle-null-v1` | Driver tests passed in 0.007 seconds and network tests passed in 0.005 seconds. Three new tests exercise the actual NULL decoder, subsequent field alignment, stale value removal, receive limits and rejection of nonempty NULL fields. Independent review checked the narrow upstream backport and the additional rejection. Manifest SHA-256: `b2c008ec3778456713370b4ea1395287f1b968d9bee3e7b85db6b726e0851039`. Native Oracle acceptance remains separate. |
| `agent-oracle-query-final-compile-v2-20261008` | The native test compiled with its execution gate unset in 0.022 seconds. Cancellation now requires observed server execution and a pending result before cancellation. The result must report a conservative outcome within five seconds. Autonomous-function persistence checks remain intact. Test and fixture logs omit raw backend errors and result bodies. Native execution remains separate. |
| `agent-mysql-rollback-final-v6-20261008` | The SQL driver and rollback suite passed in 0.045 seconds on a complete frozen snapshot. Cluster vet passed. MySQL rollback checks the server warning count before releasing the transaction. Any warning, failed diagnostic or failed release reports `unknown`. A native persisted-write positive control remains separate. |
| `agent-mysql-persistence-v1-20261008` | The native control passed in 421.15 seconds. In one fresh disposable fixture, a MyISAM write persisted and the production rollback helper reported `unknown`. The InnoDB control reported `rolled_back` and persisted no row. Normal cleanup completed, with namespace, pods, PVCs and backing PV absence confirmed. The earlier gate-unset compilation and driver tests passed in 0.043 seconds. |
| `agent-myduck-query-v6` | Focused cluster checks passed in 0.722 seconds. API error mapping passed in 0.399 seconds. The adapter rejects multiple statements and ambiguous string/comment syntax before Kubernetes access. Bound values retain their original content. Native execution remains separate. |
| Vitess selector v12 | Focused checks passed in 0.286 seconds. Spec comparison permits only the pinned CRD defaults `equal.hexWidth=0` and `tabletPools[].name=""` when absent. Nondefault values, wrong types and unrelated fields remain rejected. Native gateway selection remains separate. |
| `hakopod-agent-sql-postrows` | Driver regressions passed for authority loss after write-authorized row queries. Confirmed rollback reports `rolled_back`; failed rollback reports `unknown`. |
| `agent-cloud-policy-v2` | Shared credential-policy helper, focused server/gateway/state/identity and full Cloud Go suite passed before rebase. |
| `agent-cloud-c7f346a-9d0f49a-20261008` | After rebase onto Cloud main `c7f346a3`, the full Cloud Go unit suite passed at `9d0f49a4` with engine `b8bfc42e`. No database integration URL was set for this run. The earlier `agent-cloud-rebased-v1` directory was accidentally reused and is no longer an immutable snapshot. Its replacement run was discarded. This new snapshot used exclusive directory creation. |
| `agent-cloud-terminal-native-v8` | Final Cloud gateway/server race tests passed in 5.281 and 7.751 seconds. Covers terminal response validation, caller binding, revocation, reservation cleanup and hosted browser routing. Combined hosted and connected-node native polling passed in 74.78 seconds. |

The committed platform snapshot `e9b00927` passed CI run `37705921191`, including full Go, separate native-acceptance build, the Oracle decoder tests, vet, command builds, script checks, dashboard and SDK. Its amd64/arm64 template and Managed Actions runtime acceptance also passed. PostgreSQL network, ingress and certificate acceptance passed. Checkpoint `2e3952cc` passed its complete CI run `37708257805`. Checkpoint `1329b451` passed [CI run 37715423425](https://github.com/hakopod/hakopod/actions/runs/37715423425), including Go, native-acceptance compilation, Oracle decoder tests, vet, command builds, scripts, generated files, dashboard, SDK and the new server image build. That image build includes the local Oracle module and authorization sources. It does not publish the image. Checkpoint `97d5cad8` passed [CI run 37718789722](https://github.com/hakopod/hakopod/actions/runs/37718789722), including the NULL decoder fix and all Go, dashboard, SDK and server-image jobs. Later changes require their own final results.
Cloud commit `3d89b96` contains the terminal mapping and gateway fixes. Separate commit `57941ca` pins platform `9c6bad12`. Cloud run `37703834911` passed service and dashboard smoke checks, API tests, recovery transport and toolkit checks. Hosted packaging failed because Vitess source did not match its reviewed qualification delta. This remains a qualification blocker.
Cloud snapshot manifest SHA-256: `733f67092275b98550ab66ab3d9000c38c42cfc29939bc349f45e5997372e3c7`.
Cloud terminal v8 manifest SHA-256: `7078674f4856addb1cbb430a93fbc1b1914fb67662af7f7c4d387fd7190624ab`.

## Native development acceptance

Tests use only `k3d-hakopod-dev` and owned disposable resources.

| Workflow | Evidence |
| --- | --- |
| Pod execution | Passed separate stdout/stderr, exit 7 and authorization revocation in 5.91 seconds. |
| Application terminal polling | Snapshot `agent-terminal-runtime-v4-20261008` passed owned pinned-pod input/output, exit 7, key revocation and trusted product-authority revocation. The product revocation check closed the output handler in 3.07 seconds while the key remained valid. The full test passed in 62.55 seconds and confirmed namespace deletion. |
| Cloud terminal polling | Snapshot `agent-cloud-terminal-native-v8` passed hosted and connected-node input/output, exit status, Cloud grant revocation and exact downstream cleanup while the enrolled engine key remained valid. The test uses fixture Cloud identity/state and relay routing with the production gateway, canonical HTTP and real Kubernetes execution. It does not qualify a deployed relay. Namespace absence was confirmed. |
| PostgreSQL | Final rollback snapshot `agent-control-pg-rollback-20261008` passed focused transaction/revocation checks and managed native acceptance in 112.25 seconds. Covers TLS, exact values, DDL/DML/readback, limits, read-only refusal, cancellation and confirmed-versus-unknown rollback. Namespace and volumes were removed. |
| SQL through HTTP and MCP | `agent-query-api-mcp-v2` passed in 90.70 seconds. Canonical HTTP and HTTP MCP verified explicit grants, stale-review rejection, exact numeric/null/parameter values, writes and readback, per-operation audit identity/outcomes/redaction, session closure and revoked-key refusal. The owned PostgreSQL namespace and volumes were removed. |
| MySQL | Native snapshot `agent-mysql-query-expanded-v2` passed in 294.97 seconds. It verified exact binds, DDL, transactional and nontransactional DML persistence, read-only enforcement, result limits and confirmed rollback after authority loss. A later engine probe passed in 426.800 seconds. The fixture created an actual MyISAM table, but its managed Group Replication configuration rejected the insert with code 3098. A same-connection InnoDB positive control passed, and the MyISAM row count remained zero. This establishes the behavior of that managed configuration only. Dedicated cancellation reached actual execution. Both runs confirmed namespace and volume cleanup. The later warning-aware rollback helper passed its isolated persistence control in 421.15 seconds. That fresh fixture stopped Group Replication to permit a MyISAM write. The helper reported `unknown` with one persisted MyISAM row and `rolled_back` with zero InnoDB rows. Normal cleanup and exact namespace, pod, PVC and backing PV absence passed. This control verifies driver outcomes, not HA lifecycle behavior. |
| ClickHouse | Native v4 passed JSONCompact numeric/null preservation, read-only insert refusal, result limits, cancellation and normal namespace cleanup. Nontransactional execution only. |
| MyDuck | Native v8 passed in 100.38 seconds. The PostgreSQL DescribeExec path verified binds, exact numeric and NULL readback, read-only refusal, multiple-statement rejection before dispatch, row/byte/protocol limits and authority loss. The test observed the PostgreSQL aggregate in the shared process list before cancellation and required a result within five seconds. Namespace and backing PV absence were confirmed. Queries require explicit write permission and nontransactional mode. Read-only enforcement is unsupported. |
| Oracle | A fresh Oracle Free fixture completed initialization, then failed its bound numeric/NULL query during protocol decoding. Isolated diagnostics showed that the NULL projection caused the failure. The driver now includes the upstream type-0 NULL fix with receive-limit coverage. Fresh native acceptance remains pending. Raw namespace deletion during failed-test cleanup removed the scoped operator too early and stranded finalizers. Reviewed recovery completed, as recorded below. Recovery is not normal deletion acceptance. Execution remains disabled. |
| Vitess | Standalone v5 passed in 271.70 seconds, including actual tablet execution before cancellation, binds, writes, readback, read-only refusal, rollback, limits and owned cleanup. Later two-shard runs failed before adapter execution. Diagnostics identified slow full observation, then two exact operator CRD defaults that differed from the desired spec. The bounded owned-gateway selector, frozen credentials and exact-default comparison passed focused VM tests. Each failed native run confirmed namespace and volume cleanup. Final native selector and sharded acceptance remain pending. Execution remains disabled. |

`TestDatabaseQueryAPIMCPLive` uses a disposable PostgreSQL fixture through the canonical HTTP API and HTTP MCP transport.
The first native attempt found a test-decoding error: MCP places the SQL result inside an explicitly untrusted `data` envelope.
The corrected test verifies that envelope and passed all assertions and cleanup.
Its test SHA-256 is `b058d663417a0379a78b42296b688cca302ebf8edc3ab20672aded060ca805e7`.
The v2 snapshot manifest SHA-256 is `13bc2bca7dbda8b11d0314afd3d740beb1175cd0393286d8fb5bf9011fad1af1`.

The query transport verifies the managed application identity with TLS. A fixed byte relay runs in the exact owned, pinned database container.
SQL and credentials do not appear in relay command arguments. Kubernetes does not supply database authorization.

MyDuck must not advertise read-only support. Its reviewed UI requires an explicit write selection and explains nontransactional persistence.
A capability is enabled only after the corresponding native checks pass. Support is read from the target's query-capability endpoint.
MyDuck v8 used `1329b451` plus the reviewed cancellation test from `47bcf09e`.
Its adapter SHA-256 was `ebe7bc93cedab28aa97db987a76d9514e2c5ff0da1d0d1a63dde7893b84cdcad`.
Its test SHA-256 was `06c59aff17461daa9f680263163e745c56a9b2cc3fba518373886d8655c3681f`.
Its native log SHA-256 was `0aa7738cc435d02fec1feadba0dcdf012e373285e249145572b9db16c0d29ec2`.

The failed Oracle fixture required two recovery stages. The existing platform storage helper passed in 0.537 seconds.
The pinned operator's deletion routine then passed in 0.177 seconds. Independent review checked both temporary harnesses before execution.
They checked captured resource UIDs, ownership, deletion state, revision and the exact spec before permitted mutations.
The operator harness allowed only removal of the known SIDB finalizer after confirming empty pod and PVC inventories.
Bounded checks then confirmed namespace, SIDB, PVC and backing PV absence. No production dependency or deletion logic changed for recovery.
The harness SHA-256 values were `c7a08a4d42eb2578b0b1228a5310f883e77f6c1847e3add48785af943f92b0bf` and `722c4b10ad95cbdd699415eaa8239509c902c1f94e50c2772c4ea5a4dfc442a6`.

Later terminal snapshots v6 and v7 passed functional assertions but failed the 45-second namespace cleanup wait. They are not passing acceptance results. Snapshots v8 and v9 used a bounded 90-second absence check and passed cleanup without changing cluster resources or finalizers.

## Independent review

The [UI review](agent-control-ui-review.md) passed for the documented route families, both themes, 1440px and 390px layouts, keyboard interactions and measured bounds.
All artificial UI fixtures are labelled DEVELOPMENT ONLY. Full Cloud shell, physical touch, assistive technology and 320px layouts remain unverified.

An independent contract review identified Cloud credential inheritance and stale delegated permission reporting. Both fixes passed re-review on Cloud commit 69980179.
A later SQL review identified unconfirmed rollback claims. PostgreSQL and MySQL corrections passed the regression and native checks recorded above. A later post-rows authority-loss correction passed independent review and driver regressions; it changes failure reporting without changing successful execution or the Kubernetes transport.
Terminal review also required current `pods:exec` checks for runtime-scoped browser and CLI sessions. Creation, follow-ups and active streams now apply that ceiling. Scoped fixture tests require actual project membership; global administrator status does not replace that membership.
Further review found that direct CLI sessions could inherit execution from an administrator wildcard. CLI pod commands, terminals and SQL now require explicit grants. Active terminal checks detect removal of that grant even when administrator permission remains.
The CLI and canonical polling handlers now coordinate attachment before accepting input. Early attachment failures return an error instead of a successful empty poll.
SQL write authorization now checks both current SQL permissions. Issuance validation does not replace execution-time authorization.
The SQL driver now honors explicit nontransactional writes and reports conservative outcomes after column-metadata failures.

## Release limits

Cloud delegated terminals support polling. Hosted browser streaming is preserved by routing tests; the Cloud dashboard currently hides terminal controls. Connected-node browser streaming and installation/host administration are unavailable in Cloud.
The native connected-node test uses controlled relay routing, not a deployed production relay.
No feature merge, new release, production deployment, public endpoint or production SQL execution is claimed here.
Revocation cannot remove the interval between the final authority check and remote execution.
Nontransactional writes and implicit-commit DDL can persist after failure. Inspect unknown outcomes before retrying.

Historical checks remain in earlier Git revisions. A previous successful snapshot does not qualify later behavior changes.
