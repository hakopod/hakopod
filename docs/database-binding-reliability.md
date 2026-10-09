# Keep application connections up to date

A saved binding and a working connection are different things. A binding can
point to the right database while an existing container still has an old
password. Hakopod records the resolved settings, replaces affected pods when
those settings change, and lets you test a connection from an application pod.

## What happens when a binding changes

The API stores references in the application revision. It does not store a
resolved password in that revision. At deployment, the controller resolves each
reference and creates an immutable Kubernetes Secret for that service's
environment. The pod template names that exact snapshot.

The existing maintenance worker checks bound services under the application's
durable runtime claim. If the resolved database, username, password, or SSL mode
changes, it updates the environment snapshot and starts a rollout. Only affected
services roll. The same values do not start another rollout.

A later maintenance pass checks the actual ready pods against the current
template before recording `binding_ready`. That checks their image, environment
references, init containers, volumes and service labels. It does not claim that
the application's database driver has authenticated. Use **Test connection**
for the separate connection check, and application health for its own startup
or migration result.

Completed deployment jobs keep their original snapshot. Future scheduled jobs
receive the new settings; active jobs keep the settings they started with.
Retained ReplicaSets and pods protect their old snapshots until they are no
longer referenced. Each application can retain at most 128 environment snapshots.
If retained workloads fill that history, cleanup must happen before another
snapshot can be created.

This is reconciliation, not an immediate notification from an external secret
provider. A change becomes visible after Hakopod resolves it. A failed provider
read leaves the previous snapshot in place and does not count as verification.

## Test from the application

Open a service's **Database connections** section and choose **Test connection**.
The caller needs `deployments:write` for that application: the action starts a
fixed helper inside the container and attempts database authentication. It does
not accept a URL, password, shell command or SQL query.

The response shows the tested pod, observation time, saved application revision
and these checks:

| Check | What it establishes |
| --- | --- |
| Container environment | The helper found the declared connection variable in the container. |
| DNS | The database hostname resolves from that container. |
| Network | The container can open the database's TCP port. |
| TLS certificate | For a TLS binding, the server certificate chain and hostname verify against the selected CA. |
| Authentication | The server accepted the loaded username and password. |
| Read-only query | PostgreSQL or MySQL returned `SELECT 1`, or Redis returned `PONG`. |

The current helper supports PostgreSQL, MySQL and Redis wire protocols. This
includes Vitess and MyDuck endpoints that use a supported protocol. MongoDB,
ClickHouse and Oracle connection checks return **Unsupported** in this version;
they do not run a partial check and report success.

An ordinary internal binding can be configured without TLS. Its test uses that
transport and displays **The connection is unencrypted**. A TLS test never
retries over plaintext after certificate verification fails. For a managed
binding whose SSL option permits a weaker check, the diagnostic still requires
a valid hostname and certificate chain. The application driver's own behavior
depends on its selected settings and TLS profile.

A successful test also compares the container's value with the latest resolved
snapshot. It uses a fresh keyed comparison inside the helper; the API response
does not contain a password or credential fingerprint. A mismatch is shown as
**Current configuration not verified**, even if the old credentials still work.
The controller checks the pod identity, current template, saved revision and
authorization again before returning the result.

The helper reads the container environment supplied by Kubernetes. It cannot
inspect a value that an entrypoint rewrites inside another process, a driver's
connection pool, application tables, migration permissions or all replicas.
`SELECT 1` is a small connection check, not a complete grant audit. Use a
specific pod to check another replica.

```sh
hakopod test-connection orders --service api --variable DATABASE_URL
hakopod test-connection orders --service api --variable DATABASE_URL --pod api-abc123
```

The CLI returns a nonzero exit status unless the result is `passed`. The SDK
returns the structured result so callers can inspect each stage:

```ts
const result = await client.application(applicationId).service('api')
  .testConnection('DATABASE_URL', { pod: 'api-abc123' })
```

The API route is
`POST /api/v1/applications/{id}/services/{service}/bindings/{variable}/test`.
Its body contains `expected_revision` and an optional `pod` name. The server
allows four concurrent tests and gives each request 20 seconds. A cancelled or
interrupted transport retains its slot through the helper's possible remaining
lifetime. Test starts, outcomes and stage codes are audited. Raw driver errors,
standard error output, connection values and fingerprints are not returned or
written to the audit event.

## Install the helper

The release includes a prebuilt, digest-pinned helper for amd64 and arm64 in
`probe-image.txt`. Configure that image through the installation owner's
`HAKOPOD_READINESS_PROBE_IMAGE` setting, then redeploy bound services so their
pods receive the helper. See [helper installation](readiness.md#helper-installation).
The helper is shared with listener readiness. It adds an init container that
copies a static executable and public CA bundle, followed by no running sidecar.
The application mounts those files read-only and receives no Kubernetes token.

An older pod or installation without this helper displays **Test unavailable**
with a setup or redeployment instruction. It does not silently test from the
controller, which could have different network access and credentials.

For driver-specific trust configuration, see [application database TLS](application-database-tls.md).
Startup budgets and explicit migration jobs are described in
[application lifecycle](application-lifecycle.md).
