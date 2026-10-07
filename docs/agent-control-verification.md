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

Contract SHA-256: `08ed1f4c4401d0fa2a8a60dc80251dfa6b3631c750d291263db88fa4ae4ce0f7`.
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
| Bounded terminals | Sessions have bounded input, output retention, duration and idle time. Polling uses exact decimal cursors and reports missed retained output. |
| Accurate uncertainty | Disconnected pod commands do not claim process termination. SQL reports unknown unless the final result or rollback is confirmed. No automatic command or query retry is added. |
| Current Cloud authority | Workspace identity, current role, delegated credential and node credential restrictions intersect. Delegated callers cannot approve their own requests. |
| Sensitive evidence | Audits omit command bodies, SQL, parameters, database credentials and result rows. No fixture evidence establishes production behavior. |

## Automated checks

All compilation and artifact-producing tests in this continuation ran on bounded VM snapshots.
Source edits, source generation, formatting and inspection ran locally. Snapshots copied files and did not hard-link mutable source.

| Snapshot or unit | Result and scope |
| --- | --- |
| `hakopod-agent-readonly-final` | Platform production build, typecheck, 72 server/library tests, 238 UI tests and 55 SDK tests passed. Includes capability-driven read-only UI and unavailable-scope refusal. |
| `hakopod-agent-final-descriptions` | Final OpenAPI/proxy generation and exact SDK generation check passed after agent-language corrections. |
| `hakopod-agent-interface-final-v2` | Earlier full Go run passed except an obsolete unsupported-MySQL assertion. Corrected cluster tests passed separately. Later rollback changes require a fresh full run. |
| `agent-cloud-policy-v2` | Shared credential-policy helper, focused server/gateway/state/identity and full Cloud Go suite passed before rebase. |
| `agent-cloud-rebased-v1` | After rebase onto Cloud main 570504, full Cloud Go, composed production build, typecheck, 2 grant dependency tests, server tests and 238 UI tests passed. Engine source was copied from the platform working tree. |

Final platform Go verification and the committed Cloud engine pin remain pending in this working record.
Cloud snapshot manifest SHA-256: `733f67092275b98550ab66ab3d9000c38c42cfc29939bc349f45e5997372e3c7`.

## Native development acceptance

Tests use only `k3d-hakopod-dev` and owned disposable resources.

| Workflow | Evidence |
| --- | --- |
| Pod execution | Passed separate stdout/stderr, exit 7 and authorization revocation in 5.91 seconds. |
| Application terminal polling | Passed owned pinned-pod input/output, exit 7, key revocation and cleanup in 10.54 seconds. |
| PostgreSQL | Earlier acceptance passed TLS, exact numeric/null values, DDL/DML/readback, limits, server read-only rejection and cancellation in 91.68 seconds. Rollback-reporting patch requires rerun. |
| MySQL | Native v6 passed exact binds, DDL/DML/readback, limits, cancellation, API controls and server read-only enforcement. Rollback-reporting patch requires rerun. |
| ClickHouse | Native v4 passed JSONCompact numeric/null preservation, read-only insert refusal, result limits, cancellation and normal namespace cleanup. Nontransactional execution only. |
| MyDuck | Diagnostic runs found that MySQL prepared INSERT binds fail and reported read-only modes permit writes. PostgreSQL wire binds worked. Final nontransactional execution acceptance remains pending. |
| Oracle and Vitess | Executors exist but remain disabled until their engine-specific native checks pass. |

The query transport verifies the managed application identity with TLS. A fixed byte relay runs in the exact owned, pinned database container.
SQL and credentials do not appear in relay command arguments. Kubernetes does not supply database authorization.

MyDuck must not advertise read-only support. Its reviewed UI requires an explicit write selection and explains nontransactional persistence.
A capability is enabled only after the corresponding native checks pass. Support is read from the target's query-capability endpoint.

## Independent review

The [UI review](agent-control-ui-review.md) passed for the documented route families, both themes, 1440px and 390px layouts, keyboard interactions and measured bounds.
All artificial UI fixtures are labelled DEVELOPMENT ONLY. Full Cloud shell, physical touch, assistive technology and 320px layouts remain unverified.

An independent contract review identified Cloud credential inheritance and stale delegated permission reporting. Both fixes passed re-review on Cloud commit 69980179.
A later SQL review identified unconfirmed rollback claims. The fix and new regression/native checks are recorded above as pending qualification.

## Release limits

Self-hosted terminal acceptance does not qualify Cloud interactive terminals.
No feature merge, new release, production deployment, public endpoint or production SQL execution is claimed here.
Revocation cannot remove the interval between the final authority check and remote execution.
Nontransactional writes and implicit-commit DDL can persist after failure. Inspect unknown outcomes before retrying.

Historical checks remain in earlier Git revisions. A previous successful snapshot does not qualify later behavior changes.
