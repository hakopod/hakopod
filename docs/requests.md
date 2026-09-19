# Requests

Open **Requests** in the dashboard to inspect HTTP traffic across applications
you can read. A service's **Requests** tab adds its current ingress routes,
Kubernetes Service and backend endpoints. Select a host in that routing view to
filter the log, or inspect a request to see its timings and selected backend.

## What is captured

The platform's managed HAProxy ingress writes one structured access record when
an HTTP request completes. The existing management process collects these logs
through Kubernetes and stores them in PostgreSQL; no separate logging database,
sidecar or traffic proxy is added.

Records include method, host, path, HTTP status/protocol, TLS version, response
bytes, total duration, request receive time, queue time, backend connection and
response-header times, retries, termination flags, active backend connections,
selected backend/server, ingress pod and a unique record ID. Negative HAProxy
timings are shown as **Not measured**. The timestamp is the ingress log timestamp,
not a distributed tracing span. Timings can overlap and should not be summed.

Application assignment uses the exact generated HAProxy backend name, never an
untrusted client Host header. Unknown and default backends are installation-admin
only. A request rejected before selecting an application backend may therefore
appear only to the installation administrator.

The route diagram reads current Kubernetes ingress rules and EndpointSlices. It
does not claim every endpoint handled a particular request. Historical request
records retain the backend that actually handled that request even if current
routes or endpoints have changed.

## Coverage and privacy

This captures completed HTTP requests through the configured managed ingress,
including error responses. It does **not** capture internal service-to-service
traffic, raw TCP sessions, direct pod/host-port traffic, dashboard API traffic
that bypasses this ingress, or application function calls. Long-lived HTTP or
WebSocket connections appear after completion; they are not individual message
traces. Encrypted passthrough traffic has no HTTP request metadata here.

Query strings, request/response headers, cookies, authorization values and bodies
are not recorded. Direct client/peer IPs are masked to IPv4 /24 or IPv6 /48 **in HAProxy before
logging** and masked again when parsed. Behind another proxy, this is the proxy network; untrusted forwarded IP headers are not used. Paths can still contain personal or
sensitive data; avoid putting credentials in URL paths. The UI's Copy action
copies only the retained, privacy-limited record. No payload replay is offered.

Reading requires `logs:read`, with both credential and current identity/project
restrictions applied before filtering and pagination. A global page means all
**accessible** applications, not all tenants. Application removal cascades its
retained request entries. Unmatched ingress records require unrestricted
installation administration.

## Retention and collection limits

- PostgreSQL retains at most 24 hours and the newest 100,000 records across the
  installation. High traffic can shorten that window. This is a debugging tool,
  not a compliance archive or a billing-grade request counter.
- Collection runs every five seconds, reads at most eight ingress pods, 20,000
  retained log lines / 4 MiB per pod, and writes at most 5,000 records per cycle.
  Work uses a 30-second deadline and one collector lock per database.
- Source cursors and records commit together. Reads overlap by two seconds and
  stable IDs suppress duplicates. New sources initially read the last minute;
  there is no historical backfill.
- Kubernetes log rotation, pod replacement, controller downtime and bursts above
  these limits can cause gaps. Detected unavailable/malformed/truncated reads are
  reported; gaps are not a count of lost HTTP requests. Not every rotation loss
  can be detected. A stale collector is called out after a minute.
- The browser updates the newest page every five seconds while visible. Older
  pages pause automatic refresh. Counts and error totals are labeled per page;
  they are not extrapolated traffic rates. Search is a literal host/path match.

## Operator setup

The collector configures logging only on the owned, pinned platform HAProxy
ConfigMap. Existing deployments start collecting after the new runtime starts;
applications do not need redeployment. It preserves unrelated configuration and
refuses to replace custom `log-format`, `syslog-server` or early logging settings.
On a custom controller the Requests page reports collection as unavailable.

For an operator-controlled integration, the exact format and syslog settings are
in `internal/requestlog/request.go`. The expected ConfigMap keys are `log-format`,
`syslog-server` and `logasap=false`. Do not substitute an ordinary access format:
formats containing raw request URLs or headers can expose credentials. Changing
ingress logging triggers the controller's normal HAProxy reload.

If collection is unavailable, check the configured platform ingress ownership,
controller readiness and API permissions for pod listing and `pods/log` reads.
Request collection errors do not stop application serving; storage availability
and collection freshness are separate from request success.

## API

`GET /api/v1/requests` supports `project`, `environment`, `application_id`,
`service`, `method`, `status`, `search`, `since_seconds`, `limit` and `cursor`.
Status 2–5 selects a class; 100–599 selects an exact status. Time windows are
1–86,400 seconds and page limits 1–100. Use `next_cursor` for older records.

`GET /api/v1/applications/{id}/services/{service}/requests/routing` returns the
current ingress and endpoint observation for a service. Both routes use the same
authentication as the rest of `/api/v1` and are available through the dashboard
proxy.

## Verification

The feature passed PostgreSQL authorization, retention, deduplication, cursor and
backend-binding tests; parser/privacy checks; API and browser-proxy route checks;
the full Go suite and Go vet; dashboard tests and TypeScript checking. A real
named development Kubernetes cluster verified HAProxy configuration, HTTP 404
capture, generated backend selection, current routes/endpoints and the absence
of query/header secrets in the ingress source logs. Live testing used HTTP,
not TLS termination, long-lived WebSockets, burst load or multiple ingress replicas.
The independent UI review records its rendered coverage and remaining limits in
[the review record](requests-ui-review.md). Production was not modified.
