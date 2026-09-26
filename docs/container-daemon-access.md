# Container daemon access

Some services build or run containers of their own: a workload runner, a
notebook broker, a dbt compiler pool. Hakopod does not mount a host container
socket for them, and this feature does not change that. Instead an installation
administrator declares a container daemon that is reachable over the network
with mutual TLS, scoped to one exact project, environment, application and
service. A service then names that binding, and Hakopod projects the client
trust material and opens exactly enough egress to reach that one address.

Read [the threat model section on container daemon access](threat-model.md)
before approving a binding. A container daemon is effective root on the host
that runs it, and the platform cannot police what a granted service does inside
it.

The code paths described here are implemented and covered by unit tests. No part
of this has been exercised against a real container daemon on a real cluster, so
treat the end-to-end behaviour as unverified.

## What a binding is

A binding is installation-owned authorization. It lives in an administrator file,
never in application TOML, and it contains exactly these fields:

| Field | Meaning |
| --- | --- |
| `name` | The binding name a service references; unique, lowercase letters, digits and hyphens, at most 63 characters |
| `project` | Exact project name; no wildcards |
| `environment` | Exact environment name |
| `application` | Exact application name |
| `service` | Exact service name |
| `endpoint` | `tcps://<IPv4>:<port>`, at most 128 characters |
| `ca_file` | Absolute path to the PEM certificate authority that signs the daemon's and the client's certificates |
| `client_cert_file` | Absolute path to the PEM client certificate this service presents |
| `client_key_file` | Absolute path to its PEM private key |

All four scope fields must match the deploying service exactly. A grant is
therefore one binding for one service; a second service that needs the same
daemon needs its own binding, and giving it one is the decision described under
[shared daemons](#shared-daemons-are-not-isolated) below.

## Approve a daemon

Create the file, for example `/etc/hakopod/container-daemons.toml`. The addresses
and paths below are examples and must be replaced:

```toml
schema_version = 1

[[bindings]]
name = "runner-daemon"
project = "platform"
environment = "production"
application = "runner"
service = "worker"
endpoint = "tcps://10.90.4.17:2376"
ca_file = "/etc/hakopod/daemon/ca.pem"
client_cert_file = "/etc/hakopod/daemon/runner-client.pem"
client_key_file = "/etc/hakopod/daemon/runner-client-key.pem"
```

Point the server at it in the operator TOML:

```toml
[container_daemons]
file = "/etc/hakopod/container-daemons.toml"
```

Or set `HAKOPOD_CONTAINER_DAEMONS_FILE` directly in the service environment.
Restart the server after any change to the file; it is read once at startup and
there is no watcher.

Permissions differ slightly between the two routes, because the operator TOML
applies its own stricter rule to every file it references:

- Referenced from the operator TOML: the file must be a regular file of at most
  64 KiB with no group or other permission bits at all. Use mode `0600` or
  `0400`, owned by the server user.
- Referenced through `HAKOPOD_CONTAINER_DAEMONS_FILE`: the file must be a
  regular file of at most 64 KiB that is not writable by group or others. Mode
  `0640` owned by `root:hakopod-api` is accepted here.

Startup rejects unknown fields, a missing or wrong `schema_version`, duplicate
binding names, wildcard or malformed scope names, any endpoint that breaks a rule
below, relative trust paths or paths containing `..`, and more than 64 bindings.
Error messages describe the failure and never include certificate or key
material.

The trust files themselves must also be regular files that are not writable by
group or others. The certificate authority and client certificate are bounded to
256 KiB and the key to 32 KiB. `ca_file` must be a PEM certificate authority
allowed to sign certificates. `client_cert_file` must be a leaf certificate that
permits client authentication, must match `client_key_file`, and must be current
and signed by `ca_file`. These checks run again on every deployment, so an
expired client certificate fails the next deployment rather than silently
producing a service that cannot connect.

## Endpoint rules

The endpoint validator is the part that keeps this feature from becoming a
privilege escalation. Every rule is enforced on every resolution, not once at
acceptance:

- **`tcps://` only.** A daemon reached in plaintext cannot be authenticated, so
  there is nothing to verify and nothing to trust.
- **No socket or filesystem path.** `unix://`, `npipe://`, `fd://`, `file://`
  and bare paths are refused. A socket is a host path, and mounting a host path
  is exactly what the platform refuses to do.
- **An IPv4 address literal, not a hostname, and not IPv6.** The egress rule
  Hakopod writes names one peer, and a Kubernetes NetworkPolicy peer cannot be a
  name. A hostname would also let the destination change without review.
- **An explicit port between 1 and 65535.**
- **No credentials, path, query or fragment**, and at most 128 characters.
- **Not loopback and not `localhost`.** A daemon on the node running the pod is
  not a granted destination.
- **Not link-local and not the instance metadata address** (`169.254.0.0/16`).
- **Not inside the supported installer's pod or Service networks**
  (`10.42.0.0/16`, `10.43.0.0/16`). An endpoint inside the cluster would aim this
  capability back at the platform.
- **Not the Kubernetes API server address.** That address is read from the
  server's own client configuration, never from the file, and it is compared by
  host only: a daemon answering on another port of the control-plane address is
  still the control plane.
- **Not an unspecified or multicast address.**

## Reference it in application TOML

In the service's existing table:

```toml
[services.worker]
# Keep the service's image, port and other existing settings.
container_daemon = "runner-daemon"
```

The service must be on a network with ordinary external egress. A service whose
only networks are `internal = true` is refused, because the egress rule the grant
depends on could never let it reach the daemon.

Because a grant only means something if the service cannot re-aim it, Hakopod
reserves the client's own configuration surface. These six variable names are
refused in `env`, `secrets` and `bindings` on a service that names a daemon:

`DOCKER_HOST`, `DOCKER_TLS_VERIFY`, `DOCKER_CERT_PATH`, `DOCKER_CONFIG`,
`DOCKER_API_VERSION`, `BUILDKIT_HOST`

No mount may overlap the reserved directory
`/var/run/secrets/hakopod/container-daemon`, in either direction: neither a
mount inside it, nor a mount of a parent directory that would contain it. That
covers `volume`, `mounts`, `certificate_mounts` and `temporary_mounts`.

## What the container gets

On a successful deployment the service's first container receives:

- A read-only mount at `/var/run/secrets/hakopod/container-daemon`, mode `0440`,
  containing `ca.pem`, `cert.pem` and `key.pem`. The names are the ones the
  Docker client expects. The source is an owned, immutable Kubernetes TLS Secret
  in the application's namespace, named after a hash of its own content, so
  rotating the material creates a new Secret instead of editing one a running
  pod would not notice. Hakopod refuses a deployment if that Secret's ownership,
  type, immutability, labels or body have drifted from the declared material.
- `DOCKER_HOST` set to the same address and port as `tcp://`,
  `DOCKER_TLS_VERIFY=1` and
  `DOCKER_CERT_PATH=/var/run/secrets/hakopod/container-daemon`. Any Docker
  client or SDK reads these and makes a verified mutual TLS connection.
- One NetworkPolicy egress rule: the endpoint address as a `/32` block and that
  one TCP port. Not a subnet, so neighbours of the daemon stay unreachable.

No host path is mounted, no privileged container is created, no capability is
added, and no cluster credential is given to the application. Trust material
never appears in a log or an error message.

## Where this works and where it is refused

Available on a self-hosted installation, and on managed cloud only when the
installation has a dedicated customer-owned BYO node. That is the same isolated
cluster shape public TCP already proves. See
[dedicated BYO TCP](dedicated-byo-tcp.md).

- A managed-cloud installation with no dedicated node refuses to start with
  bindings configured, rather than starting and failing once per deployment.
  When bindings come from the operator TOML or the environment, this is refused
  while the configuration is validated; the cluster client refuses the same
  combination when the server starts.
- A shared workload runtime is refused outright, with or without a dedicated
  node. Such a runtime owns its own network policy, so the egress rule this
  capability needs would never be written and the daemon would be unreachable.
  Refusing says so at the point of decision instead of leaving a grant nobody
  can debug.
- `container_daemon` is refused on a preview, because a preview is a different
  scope and cannot inherit a grant naming the original application and service.
- It is refused on a service transfer, because the grant names the application
  and service the service is leaving. Migrate the binding explicitly.
- It is refused on an actions pool, which manages its own sandbox.
- It is refused on a serverless service, which scales to zero and would leave
  containers with nothing accountable for reaping them.
- It is refused on a job and on a scheduled job, which run unattended and may be
  retried, so nothing would reconcile the containers they created. Use a
  long-running service.

Planning, deployment acceptance and reconciliation each resolve the grant again.
An absent or wrong-scope reference fails with an instruction to ask the
installation administrator. The reference appears in the revision diff like any
other reviewed field.

## Shared daemons are not isolated

Two services granted bindings that name the same endpoint reach the same daemon
with equal authority. Each can see, stop and delete the other's containers, and
Hakopod cannot prevent that: the daemon's own API is the boundary, and the
platform is not inside it. If two services must not interfere, give them separate
daemons on separate hosts. Sharing one is an administrator's decision and an
administrator's risk.

## Revoke access

Resolution happens again on every plan, acceptance and reconciliation, and
nothing is cached. So:

- Remove the binding from the file and restart the server. Every later
  deployment of that service is refused. Deployments that already happened are
  not affected, and **running pods keep their projected certificate and their
  egress rule until they are replaced**. Revocation therefore takes effect on
  the next deployment.
- To revoke immediately, revoke the client certificate at the daemon, or on the
  certificate authority the daemon trusts, and stop accepting it there. The
  daemon is the only place that can refuse a connection already authorized.
  Then remove the binding and redeploy or stop the service.
- Removing `container_daemon` from the service and deploying successfully removes
  the mount, the variables and the egress rule from replacement pods. Trust
  Secrets no longer referenced by the current revision, or by a pod that is
  still draining, are deleted after a successful rollout.

Rotating the material is the same path in reverse: replace the files, restart the
server, and deploy. The new content produces a new immutable Secret, and pods
pick it up when they are replaced.
