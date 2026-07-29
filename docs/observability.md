# Logs and terminals

Hakopod reads workload observations directly from Kubernetes. It does not install
Elasticsearch, Loki, Prometheus, or another always-on service for these features.
The dashboard retains only a short, bounded series while a resource view is open.
Missing metrics stay unavailable; they are never displayed as zero usage.

## Search recent logs

The log explorer searches one application's selected service, across up to eight
of its newest pods, or one explicitly selected pod/container. Its histogram counts
matching lines in the sampled window. A truncation notice means some retained
logs or matches were omitted. Kubernetes log rotation, deleted pods, and node
loss limit what is available; this is not a historical audit archive.

```sql
severity >= ERROR AND message ILIKE '%timeout%'
json.status >= 500 AND json.request.method IN ('GET', 'POST')
pod = 'api-7dbcf678b9-nwxqp' AND NOT message CONTAINS 'healthcheck'
timestamp >= '2026-09-12T09:00:00Z' AND json.trace_id IS NOT NULL
```

`WHERE` is optional. The supported operators are `=`, `!=`, `<>`, `<`, `<=`, `>`,
`>=`, `LIKE`, `ILIKE`, `CONTAINS`, `IN`, `IS NULL`, and `IS NOT NULL`, combined
with `AND`, `OR`, `NOT`, and parentheses. `LIKE` uses `%` for any sequence and
`_` for one character; `ILIKE` ignores case. Quotes escape by doubling the quote.
This is a filter language, not a database connection: SQL statements, functions,
joins, regular-expression operators, and writes are not accepted.

Fields include `timestamp`, `message`, `severity`, `pod`, `container`, and
`service`. JSON object fields are accessible through `json.field` or
`json.nested.field`. A structured string `severity`, `level`, or `log.level`
sets the severity; plain text keeps `DEFAULT`, regardless of words it contains.
The original log message remains visible. Missing JSON fields fail comparisons;
use `IS NULL` to select them explicitly. Numeric unquoted literals compare
numerically; quoted values compare as text.

The REST endpoint is `POST /api/v1/applications/{id}/logs/query`:

```json
{"service":"api","query":"severity >= ERROR","since_seconds":3600,"tail":2000,"limit":500}
```

It requires `logs:read` for that application. Queries are limited to 4,096 bytes,
128 tokens, and 12 nesting levels. Reads are sequential, with a 25-second deadline,
a maximum 24-hour requested window, 2,000 tail lines and 1 MiB per pod, 64 KiB per
line, 1,000 returned entries, and a 2 MiB retained-entry budget that conservatively
counts messages and parsed metadata. JSON metadata is limited to eight nesting
levels and 128 tokens; larger objects keep their original searchable message and
produce a metadata-limit warning. At most 16 log,
event, or terminal streams can run concurrently. Access is revalidated during the
read and before returning buffered entries.

```sh
hakopod logs shop --service api --query "severity >= ERROR" --since 1h
hakopod logs shop --service api --pod POD_NAME --previous --tail 500
```

## Open a container terminal

Select a running pod from the service inspector. The default command is `/bin/sh`.
Database presets execute clients already present in the container, such as
`psql` or `valkey-cli`; Hakopod never installs tools into a running workload or
injects database credentials into a terminal command. Minimal images may have no
shell. In that case, choose an executable that exists in the image.

```sh
hakopod terminal shop --service api --pod POD_NAME
hakopod terminal shop --service db --pod POD_NAME --command-json '["psql", "-U", "postgres"]'
```

A terminal requires a human dashboard or CLI session with `deployments:write`.
CI machine keys cannot open it. The Go API verifies the application's namespace,
pod ownership, service label, running container, and pod UID before exec. A session
is bound to the exact login credential that created it. Replacing a pod requires
a new session.

The browser and CLI use the same REST transport: create a session, connect one SSE
output reader, and send base64 input or resize messages with POST. This avoids a
separate WebSocket gateway. Kubernetes exec uses its supported SPDY transport.
Output is streamed through bounded queues; no command/output history is stored
by the management process. Audit events record the actor, pod, container and
session open/close, never command arguments, keystrokes, or terminal output.

Limits are four concurrent sessions per API process, ten minutes per session,
two minutes without input, and thirty seconds to connect the initial output
reader. Input messages contain at most 4 KiB. Input and output queues hold eight
chunks each. Credential revocation or loss of a project role closes the stream
within five seconds. Closing the view, disconnecting, or shutting down the API
cancels the exec connection. Processes deliberately detached inside a container
may continue; a terminal disconnect is not a workload restart or process killer.

## GitLab source sync

GitLab.com and GitHub bindings share the source plan/deploy API. Set `provider`
to `gitlab`, a project path such as `group/subgroup/repository`, branch, and
relative TOML path. A platform administrator must approve a first binding or a
provider/repository change. Deployers can edit the branch/path within that approval.
Plans resolve a commit and fetch TOML at that exact SHA; deploy requires both the
reviewed application revision and source revision.

Configure a GitLab token under integrations; use a token scoped to the required
repositories. Enable push events with the generated webhook secret and the exact
HTTPS endpoint `/api/v1/webhooks/gitlab`. `X-Gitlab-Token` authenticates deliveries;
`X-Gitlab-Event-UUID` deduplicates them. Provider identity is part of inbox selection.
The API uses one durable worker with retries, a 1,000-job pending limit, and
bounded completed-job retention. Disabling automatic deployment, changing the
binding, expiring its grant, or revoking its owner prevents new deployment effects.

OAuth login and repository access are separate integrations. Live provider
acceptance needs operator-owned OAuth applications, repository permissions and
reachable HTTPS webhooks; local tests use provider-protocol fixtures.
