# HAProxy settings

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
JSON strings. Each value is limited to 32 characters, with at most 20 fields in
a request. Existing installations receive no new defaults until an administrator
applies a change.

Ingress or service annotations can override backend settings, including health
checks, backend connection limits and load balancing. `check-interval` only takes
effect where health checks are enabled. The editor does not change those
annotations, enable disabled checks or expose arbitrary directives and credentials.

A reviewed change includes the database revision and Kubernetes resource version.
Hakopod records the intent and an attributed audit event before applying it. The
worker checks administrator authority again and rejects a stale ConfigMap. Failed
requests preserve the editor draft; unrelated settings remain untouched.

The controller may reload HAProxy after a change. Existing connections normally
drain, but `hard-stop-after` bounds that drain. Long drains keep old processes and
their memory alive. A durable `applied` status confirms the ConfigMap update; it
does not replace checking ingress health or the controller's reload status.

`TestLiveProxyConfiguration` applies these controls to the named development
cluster, waits for the generated directives, checks them with `haproxy -c` and
restores the original ConfigMap. It refuses any other cluster context and
preserves intervening operator edits instead of overwriting them during cleanup.
