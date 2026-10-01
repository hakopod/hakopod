# PostgreSQL public endpoint development acceptance

The read-only planner is
`scripts/plan-development-postgres-public-endpoint-acceptance.py`. It performs
only Kubernetes `get`, Docker `inspect`, a TCP availability check and `helm
template`. It never applies a resource, pulls an image, starts a fixture or
executes a command in a container. The native runner refuses to start without
the planner's fresh protected output.

Both commands are only for the named `k3d-hakopod-dev` cluster on the approved
Linux amd64 development VM. The planner follows every running Docker container's
host PID into cgroup v2, applies each ancestor CPU, memory and CPU-set limit, and
compares configured CPU sets with both effective ancestor sets and online host
CPUs. It inventories every running Docker container and every non-Docker process
cgroup. A protected root-reviewed budget must classify each one explicitly;
unknown, replaced or unbounded competing workloads stop the plan. Reservations
are aggregated once at every shared ancestor, so sibling containers cannot each
claim the same parent quota. The planner repeats the Docker and host-process
inventory before returning and rejects a workload change during inspection.
It also rereads the raw `cpu.max`, `memory.max` and
`cpuset.cpus.effective` values for every relevant ancestor and rejects any
change. The host cgroup-v2 mount root may omit `cpu.max` and `memory.max`; that
fallback uses Docker's inspected host totals only after PID 1 and the planner
are proven to share the host cgroup and mount namespaces. Missing controls in
any child cgroup remain an error.

The planner then compares each node's effective capacity with Kubernetes
allocatable capacity and the requests of pods already scheduled there. The
request calculation conservatively sums application and init-container requests.
This overestimates ordinary sequential init containers, but includes restartable
init sidecars without relying on their ordering. The plan reserves one CPU and 3
GiB for its bounded runner, at least 500 millicpu and 512 MiB for the control
PostgreSQL workload, and a fixed one CPU and 2 GiB root operating-system reserve.
Kubernetes advertised capacity or a declared Docker limit alone does not satisfy
the check.

The planner also requires a protected, unexpired lane-grant file naming both
`root` and `vitess_backend`, verifies the two node identities and ownership
labels, verifies immutable image and chart digests, and refuses reused attempt
paths. The native runner separately requires at least 16 GiB of free scratch
storage and a bounded systemd unit.

Immediately before its first mutation, the runner takes the shared
`database-public-endpoint-acceptance.lock` with a nonblocking exclusive lock.
PostgreSQL and MySQL use the same lock, so their capacity reservations cannot
overlap. While holding it, the runner repeats config, grant, source, Docker,
cgroup, Kubernetes, port and Helm-render inspection and requires an exact match
with the reviewed plan. It holds the lock through cleanup. All child-process
input and output, including the long-lived revocation probe, have byte and time
bounds and are killed as a process group when a bound is exceeded.

The runner creates a new PostgreSQL control database, bootstraps a temporary
administrator, replaces it with a project-scoped bearer key, and starts the
candidate `hakopod-server` binary on a random loopback port. It then creates one
two-member PostgreSQL database and two digest-pinned host-network probe pods on
the named development nodes. Every fixture has a random run ID. Reports contain
IDs, phases, hashes and fixed probe results; they do not contain credentials or
secret bodies.

The protected configuration file has schema version 1 and these fields:

```json
{
  "schema_version": 1,
  "postgres_admin_url_file": "/protected/development-postgres-url",
  "psql": "/usr/bin/psql",
  "kubeconfig": "/srv/hakopod-backup-scratch/database-cockpit-20260929/development-kubeconfig",
  "kubectl": "/srv/hakopod-backup-scratch/database-cockpit-20260929/bin/kubectl",
  "docker": "/usr/bin/docker",
  "host_budget_file": "/protected/postgres-public-endpoint-host-budget.json",
  "helm": "/usr/local/bin/helm",
  "haproxy_chart": "/isolated/source/deploy/charts/hakopod-platform/charts/kubernetes-ingress-1.54.0.tgz",
  "server_binary": "/isolated/source/bin/hakopod-server",
  "probe_image": "registry.example/probe@sha256:64-hex-digest",
  "probe_command": "/probe",
  "app_domain": "apps.development.example",
  "public_address": "192.0.2.10",
  "public_domain": "database.development.example",
  "public_port": 15432,
  "tls_issuer": "development-database-ca",
  "ingress_class": "haproxy",
  "proxy_namespace": "haproxy-controller",
  "proxy_configmap": "hakopod-ingress-kubernetes-ingress",
  "proxy_release": "hakopod-ingress",
  "allowed_node": "k3d-hakopod-database-worker-0",
  "denied_node": "k3d-hakopod-dev-server-0",
  "server_environment": {}
}
```

The URL file, kubeconfig, host budget, configuration, lane grant and generated plan must be
owned regular files with mode 0600 or tighter. The URL supplies only the
development PostgreSQL administrator used to create and drop the runner's
uniquely named control database. Put any additional nonsecret operator settings
in `server_environment`; the runner owns the database URL, encryption key,
listener, kubeconfig and endpoint inventory.

The host-budget file has schema version 1. It contains no credentials. Container
IDs are the complete 64-character IDs returned by `docker ps --no-trunc`. Each
running container needs one exact entry. The two named k3d nodes use the
`development-node` class, and exactly one Docker or host entry uses the
`control-postgres` class. Reservations must cover the observed effective cgroup
limits. Containers with no explicit Docker CPU or memory limit are rejected.

Every non-Docker process cgroup also needs one exact `host_workloads` entry with
the executable basenames observed in that cgroup. These cgroups must have real
CPU and memory limits no greater than the reviewed reservation. This includes
systemd services, customer services and containerd slices. Exactly one entry is
the `acceptance-runner`; its cgroup must contain the planner and must be limited
to one CPU and 3 GiB. Generic path prefixes are not supported. The fixed root
reserve covers only `/` and `/init.scope`, with their executable names listed;
it cannot authorize another workload class.

```json
{
  "schema_version": 1,
  "cluster": "k3d-hakopod-dev",
  "reviewed_by": ["root"],
  "root_reserve": {
    "cpu_millis": 1000,
    "memory_bytes": 2147483648,
    "cgroups": [
      {"cgroup_path": "/", "executables": ["[kernel]"]},
      {"cgroup_path": "/init.scope", "executables": ["systemd"]}
    ]
  },
  "docker_workloads": [
    {
      "name": "k3d-hakopod-dev-server-0",
      "container_id": "64-hex-container-id",
      "class": "development-node",
      "cpu_millis": 2000,
      "memory_bytes": 4294967296
    },
    {
      "name": "development-control-postgres",
      "container_id": "64-hex-container-id",
      "class": "control-postgres",
      "cpu_millis": 500,
      "memory_bytes": 536870912
    }
  ],
  "host_workloads": [
    {
      "name": "postgres-public-endpoint-plan",
      "class": "acceptance-runner",
      "cgroup_path": "/system.slice/postgres-public-endpoint-plan.service",
      "executables": ["python3.11"],
      "cpu_millis": 1000,
      "memory_bytes": 3221225472
    },
    {
      "name": "customer-containerd",
      "class": "customer-containerd",
      "cgroup_path": "/system.slice/customer-containerd.slice",
      "executables": ["containerd", "containerd-shim-runc-v2"],
      "cpu_millis": 1000,
      "memory_bytes": 2147483648
    }
  ]
}
```

The abbreviated example shows the key shape. A real file lists both development
nodes, every other running Docker container and every other non-Docker cgroup.
The root reviewer must update container IDs, cgroup paths, executable lists and
reservations when the VM workload inventory changes. The planner records the
protected file's SHA-256 digest in its output, and the native runner checks the
same digest before starting.

The lane grant is another protected JSON file:

```json
{
  "schema_version": 1,
  "cluster": "k3d-hakopod-dev",
  "purpose": "database-public-endpoint",
  "approvals": ["root", "vitess_backend"],
  "expires_at": "2026-10-01T12:00:00+00:00",
  "nonce": "00000000000000000000000000000000"
}
```

After the lane owners create those files, generate the read-only plan with a new
attempt number:

```sh
python3 scripts/plan-development-postgres-public-endpoint-acceptance.py \
  --root /srv/hakopod-backup-scratch/database-cockpit-20260929 \
  --source /srv/hakopod-backup-scratch/database-cockpit-20260929/public-endpoint-source-v1 \
  --config /protected/postgres-public-endpoint-acceptance.json \
  --lane-grant /protected/postgres-public-endpoint-lane.json \
  --attempt 1
```

The HAProxy dry render pins controller 3.2.15 to the named development server
node. HTTP, HTTPS and statistics host ports are zero. TCP 15432 is the only
rendered host port. The plan records the source, configuration, grant, binary,
chart and rendered-manifest hashes. It is valid for 15 minutes and never extends
past the lane grant.

Only after that plan passes, run the native helper from a bounded systemd unit
with `MemoryMax=3G` and `CPUQuota=100%`:

```sh
python3 scripts/run-development-postgres-public-endpoint-acceptance.py \
  --root /srv/hakopod-backup-scratch/database-cockpit-20260929 \
  --source /srv/hakopod-backup-scratch/database-cockpit-20260929/public-endpoint-source-v1 \
  --config /protected/postgres-public-endpoint-acceptance.json \
  --lane-grant /protected/postgres-public-endpoint-lane.json \
  --plan /srv/hakopod-backup-scratch/database-cockpit-20260929/postgres-public-endpoint-v1.plan.json \
  --attempt 1
```

The run verifies writer and reader routing, TLS 1.2 or 1.3, plaintext refusal,
wrong-hostname and wrong-CA refusal, distinct allowed and denied source
addresses, existing-session closure, interrupted publication, interrupted
revocation, and retries after an unavailable HAProxy master socket and a hidden
TCP CRD. Faults pass through an owned loopback Kubernetes API proxy used only by
the owned server process. The shared CRD and HAProxy deployment are not changed.

The plan describes the cleanup boundary before native work begins. The final
JSON and JSONL evidence are written with mode 0600. The runner removes only
namespaces and port claims whose ownership labels match its generated IDs, then
drops only its generated control database. It never deletes the shared HAProxy
Deployment, namespace or TCP CRD. A cleanup failure is recorded as incomplete
and makes the evidence fail.
