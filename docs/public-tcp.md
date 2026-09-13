# Public TCP services

Public TCP forwards a connection unchanged through the existing HAProxy ingress.
An SMTP application owns its STARTTLS handshake and certificate. Hakopod's
`public = true` and `tls` fields still configure HTTP ingress; extra `ports`
remain private unless referenced by an explicit `public_tcp` listener.

```toml
schema_version = 1
name = "mail"

[services.smtp]
image = "registry.example.com/smtp@sha256:YOUR_VERIFIED_IMAGE_DIGEST"
port = 2525
public = false
size = "small"
replicas = 1
termination_grace_seconds = 60

[[services.smtp.public_tcp]]
port = 587
target_port = 2525
source_cidrs = ["0.0.0.0/0"]
```

`target_port` must match exactly one declared TCP container port (`port` or an
entry in `ports`). A primary `port` is required for workload readiness. If a
separate HTTP port provides the readiness check, that endpoint must also report
whether the SMTP listener is ready. Hakopod resolves the target to the corresponding private Kubernetes
Service port. Public HTTP and TCP cannot target the same backend port because
HAProxy requires different backend modes. Use distinct container ports. For restricted submission, replace `0.0.0.0/0` with your allowed
IPv4 CIDRs. Omission is rejected. Each service may declare four listeners, and an
application may declare sixteen; each listener accepts at most sixteen CIDRs.
Source filtering uses the source observed by HAProxy. Upstream NAT or a proxy
can change that address. PROXY protocol and IPv6 exposure are not supported by
this initial profile.

## Operator setup

Enable only the required external ports with `HAKOPOD_PUBLIC_TCP_PORTS` (for
example, `587`). The installation must also provision matching TCP container and
Service ports on its owned HAProxy controller. A host installation uses matching
host ports. A NodePort or LoadBalancer service must use `externalTrafficPolicy:
Local` to preserve source addresses. NodePort exposes its assigned node port,
not necessarily the listener's port; the external load balancer or NAT must map
587 to it explicitly. Environment configuration alone never opens a port.

Allow the same port in the cloud security group, host firewall and any external
load balancer. Configure the DNS record to the public ingress address. Hakopod
does not change cloud firewalls automatically. Platforms without the HAProxy v3
TCP CRD, the owned ready deployment, source-preserving exposure, or the required
provisioned port fail preflight rather than silently creating a private listener.
Management ports and HTTP/HTTPS are reserved.

The supported controller is HAProxy Technologies Kubernetes Ingress 3.2.15,
installed as `hakopod-ingress` in `haproxy-controller`. TCP-services ConfigMaps and
custom namespace filters are incompatible with this managed profile. Existing
v1 and v3 TCP resources are checked for port conflicts. Resource inventories are
bounded; excessive counts require operator review rather than a partial scan.

## Changes, failures and rollback

A port is reserved atomically to one application before its first deployment.
Reservations are durable ConfigMaps in the ingress namespace and survive failed
deployments, listener removal and application rollback. They prevent another
application from taking a port while a rollback is pending. To deliberately reuse
a port for another application, an operator must first retire the old revisions,
verify no matching TCP frontend remains, and remove that port's
`hakopod-tcp-port-PORT` ConfigMap. This is an explicit transfer, not an automatic
side effect of deleting a service.

Unchanged listeners stay available during readiness-gated rolling updates.
Changing a port's target or source CIDRs closes that listener before workloads
change; the new listener opens only after every desired workload is ready. These
changes can interrupt SMTP sessions. Runtime acknowledgement checks each owned
HAProxy pod's active process, generated bind, source restriction, backend and
active frontend. A failed or unacknowledged reload fails the operation. The
controller needs permission to execute a bounded read-only inspection inside
its owned ingress pods; no application receives that capability.

Connections use HAProxy's shared process and connection budget, with a per
frontend cap of 256 and a five-minute client inactivity timeout. Existing
installation backend timeouts and reload drain settings still apply. Long
sessions can be interrupted by those limits or application termination. Review
HAProxy timeouts and the application's termination grace period for your mail
server. No per-service proxy sidecar or extra ingress controller is started.

Observation reports `configured` only after the exact desired TCP resource has a
durable reload acknowledgement. Accepted resources awaiting a worker reload are
`pending`. An empty TCP resource records listener removal until it is
acknowledged, so a retry cannot reopen an old route accidentally. This status
is not a claim of internet reachability or SMTP correctness. Before cutting over
production, test from outside the cloud network: SMTP greeting, EHLO, STARTTLS,
certificate hostname and chain, authenticated delivery, disallowed sources,
rollback, and long-session behavior. Keep the existing SMTP deployment until
certificate mounts and AWS permissions have also passed their acceptance checks.
