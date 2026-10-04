# Xem runtime acceptance

`TestLiveXemTemplate` exercises the actual Xem template and upstream images
against PostgreSQL, Redis and private MinIO storage. It requires the named
`k3d-hakopod-dev` development context and an explicitly reserved, disposable
worker. It never selects a production cluster or sends email.

## Recorded verification

On October 4, 2026, native AMD64 acceptance completed all five runtime
combinations on the reserved cloud VM using the public catalog image digests.
The evidence combines the first three passing cases from run R4 with the last
two from the explicitly scoped R5 retry:

| PostgreSQL | Redis | Storage | Passing run | Duration |
| --- | --- | --- | --- | --- |
| Bundled | Bundled | Bundled MinIO | R4 | 187.83 s |
| External fixture | Bundled | Bundled MinIO | R4 | 159.96 s |
| Bundled | External fixture, DB 2 | Bundled MinIO | R4 | 161.00 s |
| External fixture | External fixture, DB 2 | Bundled MinIO | R5 | 282.12 s |
| External fixture | External fixture, DB 2 | External S3 fixture | R5 | 167.81 s |

Each passing case verified administrator and frontend session login, host/auth
rejection, private upload and signed download, then token, account, object,
Redis and PVC persistence after restarting every service. The bundled-storage
cases verified that the backend created its initially missing private bucket.
The fourth case also passed trusted PostgreSQL/Redis TLS, PostgreSQL plaintext
refusal with `verify-full`, and independent untrusted-certificate and hostname
rejection. All owned namespaces, claims, volumes, workers and clusters were
reclaimed; R5 exited successfully.

R4's fourth case passed its TLS behavior probes but failed probe cleanup because
normal pod status updates invalidated a creation-time resource version. The
retry retained the immutable UID deletion precondition, required confirmation
that each probe was gone, and passed both the affected fourth case and the
previously pending fifth case. R4 as a whole remains a failed run; its three
individually passing cases are the evidence used above.

Both runs used source base `67516302d7e57bf175bdc950d9f02ac8ad418e21` and
catalog `44f3bd6c396039920893592b0f93e5927562121c`. Their source manifests identify
the actual dirty source bytes, including the acceptance changes:

- R4: `.local/xem-runs/xem-acceptance-1791110270-4131858`, manifest SHA256
  `6c3a9aa1eff9d40be309f00ead12be3f553ce2e1555dc2b98a9f9c57f6416033`.
- R5: `.local/xem-runs/xem-acceptance-1791111289-34482`, manifest SHA256
  `1e3e42b6057db004c93f446d640247e59159a96e89510ec02bdb77b591668d69`.
  Its `test-selection.txt` records `external-dependencies` and the exact subtest
  filter; it is not presented as a separate full-matrix run.

The VM also passed all seven Redis observation regressions and TERM/INT runner
checks that verified child reaping and interruption evidence. The exact
go-redis v9.8.0 probe recorded the unused-pool behavior described below in
`redis-idle-probe/result.txt`; focused checks are in `runner-check/` beneath the
reserved scratch directory.

An earlier local run stopped after 353.92 seconds under development-node disk
pressure before backend/MinIO assertions. Its owned resources and local registry
were removed. That interruption and earlier ARM64 development/browser checks
remain historical evidence, not final-image ARM64 or public ingress acceptance.
No image was built locally or on the VM for these native runtime checks.

When the catalog entry is deployable, the suite obtains its production services
and the selected connection modes from `spec.PlanTemplate`, then adds the explicit
fixtures. Before publication it prefers `hakopod.toml` when present and otherwise
uses the disabled `candidate.toml`; the candidate path is reported in test output.

The suite creates a separate namespace for each of the four PostgreSQL/Redis
bundled/external combinations with bundled MinIO, then runs an additional case
with all three dependencies external. Planner/API regressions cover all eight
configuration combinations; the runtime suite covers five. External services
are real dependencies in the same disposable namespace; this verifies connection
configuration without claiming arbitrary provider-network access.

Bundled MinIO uses generated application-scoped access and password secrets and
starts without a bucket. The backend must create `xem-files` using
its explicit opt-in, then uploads use the private MinIO address while signed
reads pass through Xem's public origin. The test uses
`https://xem.example.test:8443` to exercise preservation of the explicit port in
SigV4's Host header. The public object route must reject anonymous reads and
write methods, strip application credentials, and disable caching. No separate
initializer job or MinIO client image is required.

The external S3 case uses an explicitly provisioned private HTTP fixture.
Production external storage requires a browser-reachable HTTPS endpoint.

## Run on a reserved cloud VM

Use a Linux VM with at least 7 GiB of available memory and 35 GiB of free space
in an explicitly reserved scratch directory. Prepare a separate Docker daemon
whose Unix socket, PID file and data directory are inside that scratch directory.
Run the daemon and the entire harness in the same dedicated network namespace;
a second daemon in the host network namespace can modify customer Docker
network rules. Supply isolated Docker configuration, runtime state and network
address pools. Keep any required host egress rules narrowly scoped to the new
namespace and record their exact cleanup. Bound both containers and build tools
with an encompassing resource cgroup before starting the daemon.
Docker and its private containerd must also share a mount namespace so image
mounts remain visible to both. On systemd hosts, use `NetworkNamespacePath` and
a read-only resolver bind; do not remount `/sys` in a way that hides cgroups.
Run the harness separately from the daemon with `KillMode=mixed` and
`TimeoutStopSec=300`, allowing the shell to stop its current child and complete
ownership checks while Docker is still available.
The host's default Docker daemon and operator Kubernetes context must remain
untouched. Copy the exact source and catalog revision into scratch; all builds,
downloads and test artifacts stay on the VM. No image build, registry login or
CPU emulation is needed. The runner downloads checksum-pinned k3d and native
kubectl v1.35.8 into the scratch checkout; Docker, Go, curl and Python must already
be available on the VM.
The runner rechecks disk space after compilation and before starting Kubernetes.
Reserve additional space for compilation and image pulls; on a shared VM,
monitor the remaining headroom throughout execution.

The checked-in runner refuses macOS, an implicit Docker socket, a mismatched
Docker data directory, a checkout outside scratch, or an existing container or
k3d cluster. After reviewing VM capacity and reserving the isolated daemon, run
inside its network namespace:

```sh
HAKOPOD_XEM_VM_ACCEPTANCE=1 \
HAKOPOD_XEM_VM_ROOT=/srv/hakopod-backup-scratch/xem-acceptance \
HAKOPOD_XEM_DOCKER_ROOT=/srv/hakopod-backup-scratch/xem-acceptance/docker \
HAKOPOD_XEM_DOCKER_PID_FILE=/srv/hakopod-backup-scratch/xem-acceptance/dockerd.pid \
DOCKER_HOST=unix:///srv/hakopod-backup-scratch/xem-acceptance/docker.sock \
scripts/xem-vm-acceptance.sh
```

The runner verifies that its network namespace matches the daemon's and differs
from the host's. It isolates both k3d configuration and kubeconfig, including
cleanup. It forces the Go target to match the VM's native architecture and
compiles the test before starting the cluster. It creates only a
`k3d-hakopod-dev` control plane (1,536 MiB) and an owned worker (4,096 MiB), then
installs the pinned local-path provisioner. Node startup uses the encompassing
cgroup limit; after each node starts the runner also applies per-container CPU
limits of 1 and 2 CPUs respectively and disables swap. It adds that
worker's exact pool toleration to the disposable storage helper. It does not
install ingress, an operator API database or a local registry. Go execution is
bounded by `GOMAXPROCS=2` and `GOMEMLIMIT=512MiB`.
The pinned k3d version requires its tools container and image volume during
cluster creation even when no images are imported. Their identities are checked
and they are reclaimed with the cluster; the tools container is limited to
0.25 CPU and 128 MiB after creation.
The default selection runs all five cases. For an explicitly scoped retry,
`HAKOPOD_XEM_TEST_CASES=external-dependencies` runs only the two cases with both
PostgreSQL and Redis external, including TLS checks. Every run records its
selection in `test-selection.txt`; a subset alone is not a full matrix result.

Evidence is saved in `.local/xem-runs/<run>/`: source/catalog revisions, native
architecture, hashes of actual source and catalog files including dirty
changes, immutable container/node identities, filtered runtime output and
cleanup status. A successful run requires every application namespace and
volume to be reclaimed. The runner then verifies ownership again, deletes the
disposable cluster and removes the test binary and kubeconfig. The isolated
daemon and its image cache remain for the VM owner to stop and clean up.
INT/TERM stop and reap the current compilation, startup or test process group
before ownership cleanup, and record `interruption.txt`. A failed ownership
check retains resources for inspection and reports incomplete cleanup. The
systemd stop timeout remains the final bound if Docker itself stops responding.

This verifies only the VM's actual native architecture. The result must not be
described as both AMD64 and ARM64 acceptance unless both native runs completed.

## Reserve a development worker manually

Use a unique run identifier, for example `xem-acceptance-<Unix timestamp>`. Record
the run identifier, exact worker container ID and Kubernetes node UID before
running the suite. Do not reuse an existing worker. Verify the
kubeconfig's current context is `k3d-hakopod-dev` before every operation.

1. Verify that the public backend and portable frontend digests in
   `templates/blueprints/xem/hakopod.toml` are available for the worker's
   architecture. Use the upstream release images; no image build, Docker login
   or local registry is required.
2. Create one agent with `k3d node create <run> --cluster hakopod-dev --role agent
   --memory 4096m`. Label its container `com.hakopod.acceptance=xem` and
   `com.hakopod.acceptance-id=<run>`. Give the node
   `--k3s-node-label hakopod.io/xem-acceptance=<run>`,
   `--k3s-arg --node-taint=hakopod.io/xem-staging=<run>:NoSchedule`, and
   `--k3s-arg --kubelet-arg=max-pods=30`. Use `--wait --timeout 180s`.
   The staging taint prevents workloads from using the new node before its
   final pool is assigned. k3d may inherit another pool taint from an existing
   agent; adding a second `hakopod.com/pool` taint at creation can prevent
   kubelet from starting.
3. Verify the worker container ownership label and node
   `hakopod.io/xem-acceptance` label. On that node only, atomically set
   `metadata.labels.hakopod.com/pool=<run>` and replace its staging/inherited
   taints with `hakopod.com/pool=<run>:NoSchedule`. Record its UID. The test
   requires the exact node name, UID and pool and supplies a matching trusted
   workload policy. It checks available capacity before deploying.

The default six-service template requests 2,154 MiB, including 615 MiB for
MinIO. The suite retains those application resource requests and limits;
it does not shrink them to force the candidate onto a smaller node. Check total
VM headroom before running concurrent builds and acceptance.

## Run the suite manually on the VM

From the repository root, supply the recorded values:

```sh
GOMAXPROCS=2 GOMEMLIMIT=512MiB \
HAKOPOD_XEM_TEST=1 \
HAKOPOD_TEST_KUBECONFIG=/absolute/path/to/development/kubeconfig \
HAKOPOD_XEM_TEST_NODE='<owned node name>' \
HAKOPOD_XEM_TEST_NODE_UID='<observed node UID>' \
HAKOPOD_XEM_TEST_POOL='<run identifier>' \
go test ./internal/cluster -run '^TestLiveXemTemplate$' -count=1 -timeout=42m -v
```

The suite checks real administrator login, the NextAuth credential/session
flow and Secure/HttpOnly cookies, host and anonymous-account rejection,
application uploads, private-object denial, and application-signed downloads
whose bytes match the uploaded file. Bundled downloads use the public object
proxy with the original signed Host, path and query intact; direct S3 reads also
verify the stored bytes. It then restarts all services and checks
refresh tokens, account identity, files, Redis data and unchanged PVCs. The
external Redis case deliberately selects database 2. Before and after restart,
Redis `CLIENT LIST` must show real connections from the backend pod on the
configured database; silent fallback to in-memory rate limiting fails.
The pinned go-redis client opens its five idle pool sockets before authentication
and database selection. A native VM probe reproduced those sockets as
`db=0 cmd=NULL tot-cmds=0` while its initialized client used database 2. The
assertion excludes only that exact untouched-socket state, requires at least
one initialized backend connection, and rejects every initialized connection
on the wrong database. Focused regressions cover these failure cases.

The external PostgreSQL/Redis case with bundled storage uses temporary
certificate fixtures and the
actual backend image to check trusted PostgreSQL and Redis TLS, PostgreSQL
`verify-full` refusal of plaintext, and independent refusal of untrusted
certificates and wrong hostnames. Redis can otherwise fall back to in-memory
rate limiting when its connection fails; the TLS checks inspect the actual
connection outcome rather than trusting `/health`.

For an independent browser reviewer, optionally set
`HAKOPOD_XEM_BROWSER_EVIDENCE_FILE` to a new absolute path in a temporary
directory. The first combination writes an exclusive mode-0600 handoff file
and waits up to five minutes for `<path>.done`. The file contains only the
owned fixture connection details and generated development credentials. Do
not print or commit it. The test removes it before teardown. A reviewer can
use an isolated browser process with a local TLS proxy and per-process host
mapping for the configured `https://xem.example.test:8443` origin, forwarding the exact host and port to the local
port-forward. Do not change system DNS or the host trust store. This browser
fixture does not verify public ingress issuance or external DNS.

## Cleanup

Each subtest verifies its namespace UID and ownership labels, removes its
scoped test secrets and namespace, and waits for its PVCs and dynamic volumes
to be reclaimed. Failures produce bounded, filtered fixture diagnostics.

After the test exits:

1. Check there are no namespaces carrying `hakopod.io/xem-acceptance`. If a
   failed teardown retained resources, verify their recorded UID/labels and
   finish their own namespace/PV cleanup before removing the worker. Never
   delete unrelated namespaces, volumes, storage classes or nodes.
2. Recheck the exact worker container ID, run label and node UID against the
   recorded values. Delete only that node and its k3d worker. Verify the node
   is gone and check for an orphaned node-password secret belonging to that
   exact newly created node if startup failed before registration.
3. Remove temporary state and browser handoff files once
   they are no longer needed. Record whether cleanup completed.

A successful run is development verification for the recorded native
architecture. It does not establish public ingress TLS, external-provider
connectivity, email delivery, provider bucket policy/CORS or backup restoration.
