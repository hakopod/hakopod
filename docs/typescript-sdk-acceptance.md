# TypeScript SDK acceptance

Verified on 2026-09-28. The SDK is a source preview at
`packages/sdk`; no npm publication or production deployment is included.
Cloud needs its corresponding automation-key change and this engine commit.

## Completed checks

| Area | Evidence |
| --- | --- |
| SDK | 25 tests, TypeScript positive/negative contract checks, generated OpenAPI consistency and npm dry-run packaging passed on Node 24. |
| Public engine | `go test -p 1 ./...` passed with isolated PostgreSQL. Includes key issuance, revocation, rotation, current-role authority, runtime-scope isolation and operation visibility after database deletion. |
| Public dashboard | Tests, typecheck and production build passed. Automation proxy checks cover methods, workspace headers, DELETE bodies and canonical API paths. |
| Cloud | Full Go suite with isolated PostgreSQL and the composed dashboard build/typecheck passed. The dependent Cloud PR records its additional checks. |
| UI | Independent [API-key UI review](sdk-api-keys-ui-review.md): 24 flow/state cases and 8 interaction cases, both editions/themes at desktop and mobile sizes, with screenshot inspection. Synthetic UI fixtures are identified as such. |
| Real Kubernetes | `TestSDKLiveLifecycle` passed in 146.27 seconds against the actual Go API, isolated PostgreSQL and `k3d-hakopod-dev`, using the built SDK. |

The live test created a private virtual network and a PostgreSQL 18 database,
deployed an unprivileged web service plus a SQL-checking service, and used a
server-resolved binding to create, write and read a table. It scaled only the web
service, rejected a stale revision, read actual runtime/log responses, deployed
an empty application, deleted the application/database/network and verified the
database deletion operation remained readable afterward. The disposable cluster
was removed after acceptance. Production workloads were not used for the test.

The successful deployment IDs were `24dfaf8cba0fab15e58a36c37cf99914`
and `cd23741431439773316d899debf7d400`; cleanup deployment was
`4e3c6ae45254b6e760dad8c756497190`. These identify disposable test operations,
not production resources.

## Reproducing the live test

Build the SDK with `npm ci && npm test` in `packages/sdk`. Provide an isolated
PostgreSQL test database and a development kubeconfig whose current context is
exactly `k3d-hakopod-dev`, with CloudNativePG 1.30.1 and local persistent storage.
Then run from the repository root:

```sh
HAKOPOD_SDK_LIVE_TEST=1 go test -p 1 -v ./internal/api \
  -run '^TestSDKLiveLifecycle$' -count=1 -timeout=15m
```

Set `HAKOPOD_TEST_DATABASE_URL` and `HAKOPOD_TEST_KUBECONFIG` in the environment.
Node must be available; `HAKOPOD_SDK_NODE` can name a wrapper that receives the
script path and forwards the fixture's API URL/key environment securely. No
production key is required: the test issues its own temporary scoped key.

## Limits of this pass

Redis operations, S3 backup/restore, PostgreSQL upgrades and connection cutover
reuse existing server APIs. SDK contract tests cover their review/payload
boundaries; they were not rerun against live Redis or S3 during this SDK pass.
Cloud authorization tests cover workspace/installation binding, membership,
MFA, permissions and approval boundaries, but this pass did not deploy the SDK
changes to production Cloud or exercise a customer workspace.

An initial live attempt correctly failed when the standard Nginx image tried to
write root-owned directories. The quick-start now uses the unprivileged image;
the final live run used its pinned catalog digest and completed cleanup.
