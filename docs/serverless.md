# Serverless HTTP functions and containers

Enable **Serverless HTTP** on a service to keep its public URL while the container
sleeps between requests. Hakopod runs zero or one replica. An incoming request
waits for readiness, then the activation gateway forwards it to the container.
The gateway does not retry application requests.

For a small function, start a **JavaScript function** or **Python function** in the
new application form or **Add service** page. Edit its HTTP handler, configure
variables and secrets, and review the deployment. The starter uses the language's
standard HTTP server and mounts its source as an immutable configuration file.
For packages/frameworks such as FastAPI, Express or Django, use **Build from Git**
to build an image and enable Serverless HTTP when configuring the resulting
service. Any HTTP container listening on `0.0.0.0` can use this lifecycle.

```toml
schema_version = 1
name = "http-functions"

[services.api]
image = "ghcr.io/example/http-function:latest"
port = 8080
public = true
size = "small"
replicas = 1
# node_name = "worker-eu-1" # Optional placement

[services.api.serverless]
min_replicas = 0
idle_seconds = 300
startup_timeout_seconds = 60
request_timeout_seconds = 60
max_concurrency = 16
```

Use `min_replicas = 1` to keep a latency-sensitive service warm. With zero, idle
time starts after the last active request finishes. In-memory state and temporary
files disappear when the pod stops; keep durable data in a separate database or
object store. Do not put background workers or scheduled tasks in an HTTP function.
A manual **Stop** remains stopped and does not wake on traffic.

| Setting | Default | Allowed values |
| --- | --- | --- |
| Minimum replicas | 0 | 0 or 1 |
| Idle period | 300 seconds | 30–86,400 seconds |
| Cold-start wait | 60 seconds | 5–300 seconds |
| Request time limit | 60 seconds | 1–300 seconds, after startup |
| Concurrent requests | 16 | 1–64, per service |

An installation supports at most 512 configured serverless services. The gateway
accepts at most 128 concurrent requests across the installation and
starts at most two services at once. A service at its concurrency limit returns
429; exhausted installation capacity or an unavailable/stopped release returns
503. A startup/request deadline returns a failure (or closes an already-streaming
response). Requests have a 4 MiB body limit. Unknown hosts return 404. Applications
still need their own authentication, rate limits and idempotency for side effects.
No exactly-once execution guarantee is made after a client disconnect or network
failure.

Use the public URL for invocation. Direct private Service DNS bypasses activation
and activity tracking, and cannot wake a sleeping service. Persistent mounts,
extra ports, public TCP, backend certificate mounts, GPU, HPA and deployment jobs
cannot be combined with serverless. Databases and workers in the same application
remain ordinary services. This release does not add event triggers, a Lambda
handler ABI, or horizontal request-based autoscaling.

## Installation

Fresh native installations configure a separate activation listener on the
server's `node_ip:8082`. Public HTTPS continues to enter through HAProxy on 443;
TLS and custom domains use the existing configuration. Port 8082 must remain
private to the cluster nodes/pods. It serves only known public applications and
has no management API. Allow its traffic through your internal firewall and keep
it closed at the public firewall/security group.

For an existing installation, add this to the API environment (normally
`/etc/hakopod/api.env`) and restart `hakopod-api`:

```sh
HAKOPOD_SERVERLESS_ADDRESS=10.0.0.10:8082
```

Use the management server's Kubernetes internal IPv4 address, reachable from the
cluster. If using operator TOML, the equivalent is:

```toml
[server]
serverless_address = "10.0.0.10:8082"
```

`HAKOPOD_SERVERLESS_LISTEN` / `server.serverless_listen` can override the bind
address for development or a wildcard bind. The advertised address must remain
assigned to this host and reach that listener. Network policy permits only this
node's address and, on native K3s, its two reserved Flannel host addresses on the
service's HTTP port. It does not permit the node's whole pod subnet. The management process must have direct
network access to Kubernetes Service IPs, as it does on the native installer.
A dashboard-only local Mac process outside K3s does not satisfy this requirement.

An unconfigured installation rejects serverless plans and disables the UI option.
Changing/removing the gateway address with live serverless services requires
reviewed redeployment of those services to update their activation endpoints.
Do not remove the gateway until those services have switched back to ordinary
mode. Upgrades preserve existing operator settings; they do not silently add a
listener to older installations.

One gateway process owns an installation-wide PostgreSQL lease. A second process
is refused; losing the lease stops the process. Scale-to-zero services depend on
this management host being available. Cloud embeddings must explicitly provision
a reachable gateway and pass the trusted address to the shared runtime before
advertising availability; enabling this in OSS does not provision Cloud gateways.

## Debugging

The service's Runtime card shows its placement and serverless policy. Sleeping
containers appear as **Sleeping** in observations. Kubernetes records
`HTTPIdleSleep` and `HTTPIdleWake` events; container logs and deployment history
remain the normal debugging surfaces.

The service Requests view shows the public ingress route and the current workload
endpoints. Public traffic passes through the activation gateway; ingress request
duration includes cold-start waiting. A sleeping service has no ready workload
endpoint until wake-up completes. Query strings, authorization headers and bodies
remain excluded from Requests logging.

If startup fails, inspect pod events, image-pull credentials, the listening port,
healthcheck and selected node. Initial deployments still have to pass their
readiness gate before becoming eligible for automatic sleep. A failed or changing
release is not activated until its deployment/recovery reaches a healthy revision.

## Verification

Verified on 20 September 2026 in the named, two-node `hakopod-dev` K3s development
cluster with `TestLiveServerlessIngressNodePlacementAndRecovery`:

- An immutable application release placed a Python HTTP service on the worker
  and a JavaScript HTTP service on the management node using their exact names.
- The idle Python service reached zero pods while the JavaScript service stayed
  at one replica and served HTTP through HAProxy.
- Three simultaneous POST requests through HAProxy woke the Python service;
  container logs confirmed one handler invocation for each original request.
- The Requests collector recorded those requests and the routing view included
  the activation route and the workload endpoints.
- A subsequent manual Stop release left the service at zero after public traffic.

The final acceptance run used automatically generated network policies with no
manual policy edits. Regression tests cover the management node's exact Flannel
host addresses, backend ownership, bounded route discovery, a revision changing
during cold start, concurrency, body limits and deadlines. UI verification is
recorded separately in [the visual review](node-serverless-ui-review.md).

This was HTTP acceptance in development, not a production deployment, a load
benchmark, a multi-gateway availability test or a new certificate-renewal test.
