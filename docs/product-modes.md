# Self-hosted and Hakopod Cloud

Hakopod Cloud focuses on deploying small applications from Git, serving them
over HTTP/HTTPS, attaching custom domains and connecting private services.
Customers choose predictable resource limits without managing ingress ports or
nodes. Public SMTP servers, SFTP and other custom public protocols belong on
self-hosted installations. A shared SMTP gateway is outside Cloud's scope.

The public repository implements the deployment engine and the installation
policy. Hakopod Cloud remains a planned service; selecting its mode does not
create a production-ready shared hosting platform.

## Product boundary

| Capability | Self-hosted Free or licensed | Hakopod Cloud |
| --- | --- | --- |
| Git deployments, HTTP/HTTPS and custom domains | Available | Core offering |
| Private service ports and scoped virtual networks | Available | Core offering |
| Public TCP, including SMTP and SFTP | Administrator-provisioned ports | Rejected by installation policy |
| Node access, host terminals and shared ingress configuration | Authorized installation operators | Platform operations only |
| Resource capacity | Operator-owned capacity and workload profiles | Plan limits; account-level enforcement is still required |
| Public signup | Disabled in public release binaries; setup and explicit invites remain available | Cloud build plus explicit operator opt-in |
| Team features | Signed Pro entitlements | Commercial packaging; no exception to networking policy |

Customer-owned Bring Your Own Cloud is a dedicated self-hosted installation on
AWS, GCP or Azure. Paid operations do not turn it into the shared Hakopod Cloud
service. The [private BYOC toolkit](cloud-deployments.md) currently provisions
HTTP/HTTPS only; additional TCP exposure still needs administrator provisioning.

## Enforced today

`HAKOPOD_DEPLOYMENT_MODE` or operator TOML `[server] deployment_mode` selects
`self-hosted` (the default) or `managed-cloud`. The choice belongs to the server
operator, never an application, team role or license. Managed cloud rejects
public TCP at planning, durable deployment acceptance, rollback and
reconciliation. Startup also rejects a port allowlist or remaining owned TCP
routes whose removal has not been acknowledged.

Self-hosted applications can use only the public ports an administrator has
already provisioned and enabled. No deployment changes host ports or cloud
firewalls automatically. See [public TCP](public-tcp.md) for limits, source
restrictions and switching an existing installation to managed-cloud mode.

Public self-hosted binaries cannot enable public signup through environment or
operator TOML settings, including a mode override. A Cloud build must separately
opt in to managed-cloud mode and signup. Initial owner setup and licensed
invitations remain available under their existing authorization checks. See
[accounts](accounts.md) for the build policy and enrollment paths.

Private TCP remains available in both modes. Applications can use databases,
queues and private APIs through declared ports and peers. Custom domains and
HTTP TLS continue to use the HTTP ingress path; a hostname does not select a
backend for arbitrary TCP traffic. Backend certificate mounts and approved AWS
identities retain their service scope and do not grant public exposure.

The engine applies fixed [CPU and memory profiles](toml.md), replica bounds and
per-application Kubernetes quotas. Those quotas are technical ceilings, not
account subscriptions or capacity reservations. Separate applications receive
separate quotas, and GPU workloads use higher ceilings. Selecting managed-cloud
mode currently does not restrict those profiles or enforce an account budget.

Use the [self-hosted operator example](../examples/hakopod-server.toml) or the
[managed-cloud operator example](../examples/hakopod-cloud-server.toml).
Environment settings take precedence over either file. Account and alarm email
uses an external SMTP provider independently of public application listeners.

## Before opening the shared service

Cloud needs account-level admission and quotas for applications, replicas,
CPU, memory, storage, builds and network usage. Deployments, scale changes,
autoscaling and rollback must all respect the purchased capacity, including
rollout headroom. Define which profiles and GPU workloads each plan permits
before exposing them to customers.

Keep cluster-wide administration in the platform operator plane. Existing roles
restrict host access, but Cloud signup, support access and operator credentials
still need a separate operational review. Validate isolation for unrelated
customers against the [threat model](threat-model.md); the current baseline is
one operator organization with trusted developers.

Public TCP denial controls inbound listeners. It does not block outbound SMTP
or replace email-abuse controls. Current external workload egress permits public
IPv4 destinations while excluding private and metadata ranges. Establish and
verify Cloud egress, rate and abuse policies before admitting untrusted accounts;
ordinary application email should use an external provider.

Public TLS issuance and renewal, recovery, upgrades, capacity exhaustion and
external reachability require production acceptance. Local TCP and policy tests
do not establish those service guarantees. Keep these launch requirements
separate from features already implemented in the engine.

The public `GET /api/v1/auth/status` response includes `deployment_mode` as
`self-hosted` or `managed-cloud`, including before first-owner setup. It reports
the validated startup mode and does not enable public signup or bypass signed
entitlements. Cloud connections can use it to reject self-hosted installations.

## Initial Cloud connection limits

Managed-cloud mode now enforces the initial BYO-node policy: exactly one
registered Kubernetes node, 1–10 services per application, small/medium/large
resource profiles, and at most three replicas per service including autoscaling.
GPU and AWS workload identity are unavailable alongside public TCP. Additional
node enrollment is disabled in this mode. Self-hosted installations retain their
existing profiles, replica limits and administrator-provisioned public ports.

Planning, every new durable revision and reconciliation check these limits.
Rollback checks both the desired and resolved historical specifications. A denied
revision cannot allocate a new application or queue entry. Returning an existing
idempotency result does not enqueue another operation; reconciliation still checks
the current policy before applying it. Switching deployment mode does not delete
existing workloads or guarantee that old workloads already meet the new limits.

`GET /api/v1/cloud/capabilities?project=demo&environment=development` exposes
versioned enforcement and a bounded node-count observation to a machine key with
read access to that project/environment. It returns no node names, addresses or
credentials. Cloud checks it when connecting and before forwarding an operation.
Old engine versions without this contract must fail the Cloud connection check.
A node joined outside Hakopod causes subsequent checks to fail; administrators
who control and modify the engine remain outside a remote enforcement guarantee.

Validation covers ordinary revisions, resolved rollback input, key scopes,
parallel/self-hosted policy behavior and a read-only capability/preflight check
against `k3d-hakopod-dev`. A denied real-cluster deployment created no namespace.
These are configured limits; no production capacity or billing claim is implied.
