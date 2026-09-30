# Hakopod Edge

Hakopod Edge is the traffic-protection and routing configuration built on our
existing HAProxy ingress. Open **Settings → Hakopod Edge → Edit configuration** to
configure it. The Infrastructure entry remains available. This name does not
describe a separate service, a CDN or a global network.

Installation administrators can set ordered rules for exact hostnames and path
prefixes, with IPv4/IPv6 allow and deny lists, per-client rate limits and country
restrictions asserted by a trusted proxy. A Cloud workspace cannot change the
shared installation policy; the internal Cloud operator manages it.

Protection is disabled until explicitly enabled. The editor has a separate
review step. It preserves drafts after a failed request, and a concurrent change
requires comparing the latest configuration and reviewing again. Controller
settings and the edge policy can be saved in one durable, audited revision.

## Traffic rules

Rules are evaluated in their displayed order. The **first matching hostname and
path prefix supplies the complete rule**; later rules do not add restrictions.
Place a specific path before a catch-all `/` rule. A prefix such as `/api` also
matches `/apiculture`; use `/api/` when that distinction matters. Denied addresses
and countries take precedence over allow lists in the selected rule.

Hostnames must be exact DNS names without wildcards, schemes or ports. On hosts
with enabled rules, requests must use canonical paths. Unreserved percent
escapes, repeated slashes, dot segments, encoded separators, semicolon path
parameters, nested percent encoding and malformed escapes receive **400**. This
keeps the ingress route, traffic rule and application request aligned. Query
bytes are preserved.
Only canonical certificate-challenge token paths under
`/.well-known/acme-challenge/` are excluded. Traversal, extra path segments and
encoded separators do not receive that exemption.

HAProxy 3.2 requires `expose-experimental-directives` for its URI normalizers.
Hakopod adds that directive in its owned global block while these path guards
are enabled and removes its directive when protection is disabled. The editor
does not accept arbitrary experimental directives.

Requests rejected by an address or country rule receive **403**. Requests over a
configured rate receive **429**. Rates use HAProxy's one-second request-rate
counter and are independent for each rule and client. They are local to each
HTTP/HTTPS listener and ingress process, not an installation-wide quota or a
token-bucket burst allowance. Multiple ingress replicas increase the aggregate
rate a client may obtain. Counters may reset during reloads and restarts.

Each listener's rate table holds at most 20,000 keys with a ten-second expiry.
When it cannot track a new key, the covered request receives **503** rather than
silently bypassing the limit. No database or management API call runs on the
request path.

Policy limits are 32 rules, 64 entries per rule list, 512 total CIDRs including
trusted proxies, 32 trusted proxy CIDRs, 128 characters per path prefix and a
64 KiB serialized policy. IDs are unique lowercase letters/digits/underscores/
hyphens, at most 32 characters. Country codes are ISO 3166-1 alpha-2. A rate of
zero disables rate limiting for a rule; otherwise it must be 1–100,000 requests
per second. Rules remain saved and validated when the policy is disabled.

## Client identity and country rules

**Connection address** uses the original peer address seen by HAProxy and ignores
forwarded identity headers. A load balancer or NAT can make many visitors share
that address. Kubernetes NodePort/LoadBalancer Services must preserve source
addresses with `externalTrafficPolicy: Local`, unless ingress uses host
networking or the supported host-port installation. Direct upstream NAT still
needs consideration. The development setup uses `Local`; external load
balancers must target nodes that actually have ready ingress endpoints.

**Trusted proxy** mode requires an explicit list of proxy peer CIDRs and one
client-IP header: `CF-Connecting-IP` or `X-Real-IP`. Only those peers may assert
the visitor's address. Covered requests from any other peer, or with missing,
duplicated or malformed identity headers, receive 403. This also blocks direct
origin access to a protected route. Broad `/0` proxy trust ranges are rejected.
Keep the trusted list synchronized with your provider's published networks.

Country restrictions additionally require `CF-IPCountry` or
`CloudFront-Viewer-Country` from that same trusted proxy. The proxy must supply
the chosen client identity and country headers; selecting a name does not make
the provider send it. Missing, duplicated, invalid and unknown country values
receive 403 for a rule with country restrictions. This is proxy-asserted
geography; Hakopod does not install or download a GeoIP database.

Existing host/path rewrites, TLS passthrough routes, unconditional source-header
rewrites, PROXY protocol configuration, disabled snippets or conflicting operator
rules require separate operator review. Hakopod rejects incompatible configuration
rather than overwriting it.
The compiler reserves its own variables and rate tracking; arbitrary HAProxy
directives remain unavailable through the editor.

## API and application status

The dashboard uses the same `GET` and `PATCH /api/v1/settings/haproxy` contract
as other clients. Read the latest database revision and Kubernetes resource
version before saving. For example:

```json
{
  "expected_revision": 0,
  "expected_resource_version": "REVIEWED_RESOURCE_VERSION",
  "edge": {
    "enabled": true,
    "client_ip_source": "connection",
    "rules": [
      {
        "id": "api",
        "host": "api.example.com",
        "path_prefix": "/",
        "deny_cidrs": ["198.51.100.7/32"],
        "requests_per_second": 20
      }
    ]
  }
}
```

Supplying `edge` replaces the whole policy. Omitting it keeps the current
policy. `settings` retains its existing partial-patch semantics. A 202 response
means the change is durably queued, not active. The worker rechecks administrator
and Cloud-operator authority before applying it.

For changes that include or retain an edge policy, the worker checks the owned
ingress configuration and reads the exact policy revision from the running
HAProxy workers before marking the change applied. A saved ConfigMap or a
generated configuration file alone is
insufficient. Unsupported configuration, drift or failed reload acknowledgement
is reported. Keep the previous working revision available when troubleshooting
an external controller failure; an applied status records a successful check at
that time, not continuous monitoring of every future ingress change.

## Controller settings

Administrators can review and edit installation ingress settings in the dashboard.
Hakopod targets the owned HAProxy Technologies Kubernetes Ingress ConfigMap. The
catalog follows the controller's [3.2.15 annotations reference](https://github.com/haproxytech/kubernetes-ingress/blob/v3.2.15/documentation/annotations.md).
The development installation uses chart 1.54.0, controller 3.2.15 and HAProxy 3.2.23.

Use a JSON object with string values. Send only the fields you want to change.
An empty string removes a field, allowing the controller's default or another
applicable override to take effect. Unknown ConfigMap entries are preserved.

```json
{
  "load-balance": "leastconn",
  "check-interval": "10s",
  "timeout-check": "5s",
  "pod-maxconn": "128",
  "timeout-queue": "5s"
}
```

| Setting | Accepted values | Effect |
| --- | --- | --- |
| `max-content-length` | Self-hosted only; integer bytes, 1–10737418240 (10 GiB) | Returns HTTP 413 when the declared `Content-Length` exceeds this limit. Streaming/chunked bodies are not capped. |
| `maxconn` | Integer, 16–65536 | Caps connections accepted by each HAProxy process. Higher limits use more memory. |
| `nbthread` | Integer, 1–8 | Sets HAProxy worker threads. |
| `timeout-connect`, `timeout-client`, `timeout-server` | 1ms–24h | Limits backend connection setup and client/backend inactivity. |
| `timeout-http-request`, `timeout-http-keep-alive` | 1ms–24h | Limits request arrival and idle HTTP connections. |
| `timeout-tunnel` | 1ms–24h | Limits inactivity on upgraded connections, including WebSockets. |
| `timeout-check` | 100ms–5m | Limits backend health-check responses. |
| `check-interval` | 1s–10m | Sets the interval between enabled backend health checks. Lower values create more probe traffic. |
| `timeout-queue` | 1ms–1h | Limits the wait for backend connection capacity. |
| `timeout-client-fin`, `timeout-server-fin` | 1ms–1h | Bounds the lifetime of half-closed connections. |
| `hard-stop-after` | 1s–24h | Limits connection draining during a reload. Remaining connections close at the deadline. |
| `pod-maxconn` | Integer, 16–65536 | Caps connections per backend pod. The controller divides this value across ingress replicas; keep the value above the replica count. Requests beyond the cap queue. |
| `load-balance` | `roundrobin`, `static-rr`, `leastconn`, `first`, `source`, `random` | Selects the default backend algorithm. Algorithm arguments are unavailable. |
| `http-connection-mode` | `http-keep-alive`, `http-server-close`, `httpclose` | Reuses both connections, closes backend connections, or closes both sides. Closing connections increases connection setup work. |
| `dontlognull` | `true`, `false` | Omits connections with no data when true. Turning it off can increase log volume. |
| `logasap` | `true`, `false` | Logs after response headers when true; final response size and duration are then unavailable. |
| `abortonclose` | `true`, `false` | Cancels pending backend work after a client disconnects when true. |

Durations must use one integer followed by `ms`, `s`, `m` or `h`. Compound and
fractional durations are rejected. Booleans and integers must still be quoted
JSON strings. Each value is limited to 32 characters, with at most 21 fields in
a request. Existing installations receive no new defaults until an administrator
applies a change.

Ingress or service annotations can override backend settings, including health
checks, backend connection limits and load balancing. `check-interval` only takes
effect where health checks are enabled. The editor does not change those
annotations, enable disabled checks or expose arbitrary directives and credentials.

One narrow exception exists, and it is not general annotation support: a service
with `backend_http2 = true` gets `haproxy.org/server-proto: h2` on its own
generated ingress, so the ingress speaks HTTP/2 to that service's backend instead
of HTTP/1.1. This is for gRPC and other servers that accept only h2. A few other
specification fields also write fixed controller annotations, such as the
serverless timeout and TLS redirect and issuer annotations, but a specification
cannot set arbitrary annotations, and the HAProxy editor still cannot set them.
The annotation applies to the whole ingress, so it is rejected alongside named
`http` endpoints, whose backends speak HTTP/1.1.

Two consequences are worth planning for. Readiness probes run from Kubernetes
straight to the pod over HTTP/1.1, so a `healthcheck` path against a pure gRPC
server fails; omit `healthcheck`, and the declared port gets TCP readiness
instead. A `readiness` table with `protocol = "tcp"` is optional. TLS ingresses
also carry `haproxy.org/ssl-redirect: true`, and a gRPC client cannot follow the
resulting 301, so point real clients at the TLS frontend.

A reviewed change includes the database revision and Kubernetes resource version.
Hakopod records the intent and an attributed audit event before applying it. The
worker checks administrator authority again and rejects a stale ConfigMap. Failed
requests preserve the editor draft; unrelated settings remain untouched.

The controller may reload HAProxy after a change. Existing connections normally
drain, but `hard-stop-after` bounds that drain. Long drains keep old processes and
their memory alive. A durable `applied` status confirms the ConfigMap update.
If an edge policy is saved, even when disabled, it also confirms that the running
workers acknowledged that policy during the apply. This is not continuous health
monitoring; inspect ingress health when diagnosing later changes.

`TestLiveProxyConfiguration` applies these controls to the named development
cluster, waits for the generated directives, checks them with `haproxy -c` and
restores the original ConfigMap. It refuses any other cluster context and
preserves intervening operator edits instead of overwriting them during cleanup.

## Declared request body size

In self-hosted installations, set `"max-content-length": "10485760"` for a
10 MiB declared upload limit. An empty string removes the guard. The dashboard
lists this under Settings → Hakopod Edge → Edit configuration. Hakopod Cloud
rejects changes to this field, including previously queued self-hosted changes.

This is a Content-Length admission guard, **not a complete request-body limit**.
HAProxy 3.2's `req.body_size` reports only available buffered data for chunked
requests, so using it as a large streaming-body cap would allow bypasses. Enforce
streaming/chunked limits in the receiving application or a dedicated upload
service. No large per-connection buffers are allocated by this setting.

Hakopod generates a fixed HTTP request ACL in an owned block of
`backend-config-snippet`; arbitrary snippet input stays unavailable. Other
snippet content is retained. Editing or removing the owned block outside
Hakopod produces a conflict instead of falsely reporting the guard as configured.
A backend snippet triggers the controller's reload path; frontend-only snippet
edits in this controller version can update the file without activating it. On
reset, a harmless comment explicitly clears the generated rule.
Controller redirects/rejections that happen before the snippet still take priority.

`TestLiveContentLengthGuard` checks the declared-size boundary through real
HAProxy, documents that streaming bodies are not capped, and verifies reset
removes the effective ACL. It uses only the named development cluster.
