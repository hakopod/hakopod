# Hakopod SDK

Deploy apps, manage databases and connect private services from TypeScript.
Use the same SDK with self-hosted Hakopod or Hakopod Cloud. Go remains responsible
for authorization, image resolution, scheduling, replication and recovery.

Requires Node.js 22.12 or later. ESM, with no runtime dependencies. This package
is intended for trusted scripts, CI and server code. Never put an API key in a
browser bundle, application repository or client-side environment variable.

This is a source preview; `0.1.0-alpha.1` has not been published to npm yet.
Build and pack `packages/sdk` from this branch, then install the resulting
tarball in your project. The npm package name will be `@hakopod/sdk`.

```sh
cd packages/sdk
npm ci
npm pack
# In your application directory:
npm install /path/to/hakopod-sdk-0.1.0-alpha.1.tgz
```

Use an engine built with this SDK change. Cloud additionally needs the matching
workspace automation-key change. Earlier Cloud CLI credentials cannot be used
as SDK automation keys.

Set `HAKOPOD_API_URL` to your dashboard origin (or its `/api/v1` URL), and store
`HAKOPOD_API_KEY` in your secret manager. HTTPS is required except on loopback.
Create a scoped key in **Settings → API keys**. Cloud workspace owners can issue
keys for their ready workspace; no installation administrator permission is
granted. Cloud infers the key's workspace. An optional `HAKOPOD_WORKSPACE` must
match it. Old Cloud CLI sessions do not grant SDK workload access.

The pending Cloud key update adds **Never expires** for CI workflows. Select it
when creating or rotating a key, then save the returned value as
`HAKOPOD_API_KEY` in the CI secret store. No SDK or CLI flag is needed to use it.
The key keeps its workspace and permission limits, and revocation still takes
effect. Rotation gives the previous key at most 15 more minutes of access, even
if it originally had no expiry date. This option requires the matching Cloud
and engine release; it is not available in existing deployments yet.

```ts
import { Hakopod, service } from "@hakopod/sdk";

const hako = await Hakopod.connect(); // API URL + key; resolves the key's scope
const review = await hako.app("hello").plan({
  services: {
    web: service({
      image: "nginxinc/nginx-unprivileged:stable-alpine",
      port: 8080,
      public: true,
    }),
  },
});

console.log(review.plan.changes, review.plan.warnings);
const run = await review.apply();
console.log("Deployment:", run.id);
await run.wait({ onProgress: ({ status }) => console.log(status) });
```

The plan resolves image tags to digests and records the expected revision.
Review a new plan after a revision conflict. Plans with missing secrets cannot
apply. `app(name).deploy(spec, { review })` is an explicit plan/apply shortcut;
without a callback it submits immediately. A full application specification
replaces the desired application, including omitted services and configuration.

If you use an installation key, select a scope explicitly:

```ts
const hako = new Hakopod({
  apiUrl: "https://hakopod.example.com",
  apiKey: process.env.HAKOPOD_API_KEY,
  project: "shop",
  environment: "production",
});
const staging = hako.in({ project: "shop", environment: "staging" });
```

`HAKOPOD_PROJECT` and `HAKOPOD_ENVIRONMENT` also work, but must be supplied
together. No method silently switches to another project or environment.
Cloud keys cannot switch workspaces or connected installations.

## Apps and services

```ts
// Change one service, preserving its siblings and their revision.
const edit = await hako.app("hello").service("web").plan({ replicas: 3 });
console.log(edit.plan.changes);
await (await edit.apply()).wait();

const current = await hako.app("hello").get();
await hako
  .app("hello")
  .service("web")
  .restart({ expectedRevision: current.revision });
console.log(await hako.app("hello").service("web").logs({ tail: 100 }));
```

Service handles also expose `get`, `runtime`, `scale`, `stop` and `resume`.
Runtime actions and rollbacks require an expected revision. `scale(0)` is not a
stop operation: use `stop`. Application deletion requires `confirmName`,
`expectedRevision` and application management permission. First deploy a reviewed
empty service list and wait for cleanup; deletion does not silently stop services
or destroy retained data. Deleted application names cannot be reused.

For a new application, save secrets with the typed `/secrets/{name}` endpoint
using its explicit project/environment/application scope. For an existing app,
use `app(name).setSecret(name, value)`. Bind secrets using the canonical `secrets`
fields in the spec. Avoid literal secrets in `env`: plans and configuration are
inspectable. Database bindings below keep passwords out of the script.

## Databases

The engine and installation support matrix is in [Managed databases](../../docs/managed-databases.md).
Types describe the API contract; an engine field alone does not establish that
the installed release or workspace can run it.

Create a database once, then reference it by name or durable ID in later runs:

Use `await hako.databasePlacementNodes()` to inspect the approved nodes in the
selected project and environment. The result includes availability and reported
zone, region and provider. Choose `placement.node_names` from those observations;
a provider label alone does not prove that the nodes can survive a provider outage.

```ts
const provisioning = await hako.db("main").create({
  engine: "postgresql",
  mode: "cluster",
  cpu: "250m",
  memory: "512Mi",
  storageGiB: 5,
});
await provisioning.wait();
const database = hako.database(provisioning.databaseId);
const binding = await database.binding();

const edit = await hako
  .app("hello")
  .service("web")
  .plan({
    bindings: { DATABASE_URL: binding },
  });
await (await edit.apply()).wait();
```

PostgreSQL defaults to version 18; Redis defaults to version 8. The development
MySQL implementation defaults to 8.4; see its [current validation status](../../docs/managed-mysql.md).
Standalone means one member. PostgreSQL and Redis clusters default to one
replica; MySQL defaults to two voting replicas. Redis also defaults to three
shards. CPU, memory and storage apply **per database member**; MySQL adds
sidecars and Routers to the reservation. The server checks actual
installation capability and Cloud quotas; a small Cloud workspace might not fit
this cluster. Database replication is independent of application replicas.

Redis Cluster needs a cluster-aware client. `database.binding({ clusterAware:
true })` acknowledges that requirement; it does not configure your Redis client.
PostgreSQL and MySQL bindings support `read_write` and `read_only`; Redis Cluster
uses `cluster`. Configured PostgreSQL pools add `pooled_read_write` and optionally
`pooled_read_only`. Creation accepts `placement` and `pooling` with their API
schema fields and requires TLS. MySQL clients must load the bound database's
public CA from `/var/run/secrets/hakopod-database/<database-id>.crt`, verify the
endpoint hostname, and reconnect after failover. The binding does not configure
driver-specific TLS options. Endpoints are private and cannot generally be reached by a laptop.
`credentials()` is an explicit password reveal requiring write permission.

```ts
const resize = await database.resizePlan({ replicas: 2 });
console.log(resize.plan.plan.warnings, resize.plan.plan.blocked_reasons);
await (await resize.apply()).wait();
```

Blocked reviews cannot apply. Redis shard changes need a verified recent backup
and actual healthy slot ownership. The server rejects stale reviews and storage
shrinks. Delete with `expectedRevision` and `confirmName`; the returned operation
remains readable after the database disappears. Database names can be reused
after deletion, so keep IDs for recovery and long-lived references.

Oracle Enterprise Data Guard has a separate graceful switchover flow. Its
runtime gate remains closed pending licensed-image acceptance; these methods
do not make it available on an installation. When enabled, every member must
be healthy and applications must reconnect as the primary role changes.

```ts
const review = await database.switchoverPlan(standbyMemberName);
console.log(review.plan, review.warnings);
const switchover = await database.switchover({
  reviewId: review.id,
  expectedRevision: review.plan.revision,
  confirmName: "orders",
}, { idempotencyKey: savedRetryKey });
await switchover.wait();
```

Keep the review, revision and retry key before submitting. The server rejects
expired or changed topology. After a worker timeout, `retrySwitchover({
operationId, expectedRevision, confirmName })` resumes the existing approved
operation and target. It does not force failover or choose another standby;
a failed native broker operation still requires recovery review.

## Backups, recovery and upgrades

Configure and test an eligible S3 destination in the dashboard or with the typed
backup destination endpoints. Save any one-time recovery key separately. Then:

```ts
const backup = await database.backup(destinationId);
const archive = await backup.wait();
await database.schedule({
  name: "main-daily",
  destinationId,
  frequency: "daily",
  retentionCount: 7,
});
```

Create a separate, empty database first. Use `target.restorePlan(artifactId)`,
review its warnings, apply it and wait for the backup job. After actually
checking the recovered data, call `target.inspectRecovery({ jobId, confirmName,
expectedRevision, inspected: true })`. This is your attestation, not a data test
performed by the SDK.

Finally review `target.connectionPlan({ applicationId, service: 'web', variable:
'DATABASE_URL' })` and apply it to replace the saved connection and redeploy.
Recovery does not automatically change application connections. A PostgreSQL
17-to-18 upgrade follows this same separate logical-copy workflow. Source data
stays available; writes after capture require a fresh capture before cutover.
Redis archives preserve values and expiry and are consistent per shard, not a
single transaction across shards.

Import existing eligible Docker archives through the dashboard, then use the
resulting artifact ID with this recovery flow. Binary archive uploads are not
part of the SDK's typed JSON request method.

## Networks and the full API

`hako.network(name).plan({ segments })` returns a review. Applying it preserves
the reviewed identity and revision. Network deletion requires `expectedId`,
`expectedRevision` and `confirmName`. Connect services with the spec's `networks`
and `virtual_network` fields. `app()`, `service()`, `db()` and `network()` also
export pure configuration helpers: they perform no I/O.

```ts
const result = await hako.request("GET", "/applications/{id}", {
  params: { id: applicationId },
});
```

Method, path, parameters, JSON body and response types are generated from the
canonical OpenAPI contract. This escape hatch includes builds, Git workflows,
domains and backup configuration. Availability and permissions depend on the
installation. The dashboard's public automation proxy exposes an explicit
workload subset; installation administration uses the direct management API.
There is no local directory upload, in-memory app compiler or hidden build
service. Use the existing Git build flow or deploy a container image.

## Failures, safety and Terraform

- `APIError` preserves status and server code. Cloud `approval_required` includes
  `error.approval` with the workspace and review ID. Complete the review in Cloud;
  the SDK does not approve its own changes or report them as deployed.
- Only GET requests retry automatically. Writes never do. A `TransportError`
  with `outcome === 'unknown'` means the server may have accepted the write.
  Recover deployments with `recoverDeployment(idempotencyKey)`; for other writes,
  retain the exact payload and its key before retrying. Not all endpoints support
  idempotency; review the API contract before retrying a raw request.
- Keep operation IDs. Resume using `deployment(id)`,
  `databaseOperation(databaseId, operationId)` or `backup(id)`. Cancelling a wait
  does not cancel server work. `deployment(id).cancel()` requests cancellation;
  it does not imply an undo. Progress is actual server state, without estimates.
- Requests, responses, retries, concurrency, queues, pagination and waits are
  bounded. Name lookup stops at server limits; use durable IDs or explicitly
  paginate large application lists. Log tailing is bounded, not a live stream.
- Use Terraform for resources it owns and the SDK for resources owned by your
  scripts. Do not let both manage the same app, database or network: each can
  overwrite the other's desired state. Transfer ownership deliberately.
- Cloud membership, MFA, approvals, capacity and installation policy still
  apply. Expired/revoked keys cannot make new requests; deployed workloads stay
  running. Replacing a Cloud node invalidates keys bound to its old installation.

## Development

From `packages/sdk`: `npm ci`, `npm test`, `npm run check:generated`,
`npm pack --dry-run`. Regenerate with `npm run generate` after updating the public
OpenAPI contract. Tests use explicitly synthetic fixtures unless marked as live.

Legacy external database records remain readable through `externalDatabase`.
The SDK permits rotating their credentials, disconnecting their application
bindings, refreshing an existing binding to the rotated credential revision,
and deleting the record after those deployments finish. Creating, changing
endpoint configuration or adding bindings is unavailable.
