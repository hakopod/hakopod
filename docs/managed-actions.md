# Managed Actions

Open **Catalog → Automation → Managed Actions** to create an organization or repository
GitHub Actions runner pool. Each replica is one concurrent job slot. This is a
Pro capability (`managed_actions` in the signed installation license).

## Installation

The installation needs the dedicated `hakopod-actions` sandbox. On a completed
installer-managed, single-server Linux installation, extract and verify the
installer kit for your installed Hakopod release, then run from that kit:

```sh
sudo python3 ./installer/modules.py managed-actions
```

Run this during a maintenance window: it restarts the installer-owned K3s unit.
It preserves the existing containerd template, adds a separate pinned gVisor
runtime, and leaves the default runtime unchanged. It refuses an unowned
runtime, edited managed configuration, or a multi-node/external cluster.
Existing installations retain their original maintenance helpers on upgrade;
use the new release's verified kit for this optional module.

External cluster operators must provision the same pinned runtime on eligible
nodes and the RuntimeClass scheduling selector. Do not run the CI setup script
on an operator cluster: it deliberately replaces a disposable k3d configuration.
The module is not an automatic Cloud or BYOD node migration.

## Create a pool

1. Choose a project and environment, then open Managed Actions in the catalog.
   Existing pools use the same guided flow from **Configure pool**.
2. In **GitHub**, name the pool and choose an organization (the default) or one
   repository (`owner/repository`). Service names, credential references and
   optional runner groups are under the advanced disclosure.
3. In **Compute**, choose concurrent jobs, architecture and resources. Keep
   automatic placement or select a prepared runner node to put CI work on
   separate compute. A pinned pool waits if its node is unavailable; it never
   silently moves to another node. Nodes outside your environment allocation
   are not exposed. Resource cards show per-runner reservations and limits;
   the pool total reflects the selected concurrency. Existing custom values
   are preserved until you change them.
4. In **Jobs**, set workflow labels, maximum lifetime and temporary disk. Copy
   the displayed `runs-on` labels into your workflow. Caching is configured in
   the workflow, not enabled automatically by pool creation.
5. In **Review**, check the planned changes and capacity, save any missing
   credential, then deploy. Returning to a step or a failed request preserves
   the draft. The server checks the reviewed application revision before
   accepting deployment.

Organization pools use the GitHub default runner group unless you enter a
   runner group ID. Set which repositories can use the group in GitHub under
   **Organization Settings → Actions → Runner groups**. Hakopod does not change
   that access policy. A repository is not required for an organization pool.
During review, save an application-scoped fine-grained GitHub token with
   **Self-hosted runners: read and write** organization permission, or
   **Administration: read and write** for a repository pool. Both scopes also
   need **Actions: Read-only** under repository permissions for job steps and
   completed logs. Give the token access to every repository whose jobs the pool
   will run, including private repositories. Do not put the token itself in TOML
   or Git.
The service page shows GitHub-observed runner
   state and the time of its last observation.

The pool form and secret-entry review both include **How to create the GitHub
token**. In GitHub, open **Settings → Developer settings → Personal access
tokens → Fine-grained tokens**, create a token, and choose the organization or
account that owns the repositories as its **Resource owner**. Set a name and
expiration, select repositories, add the permissions above, then generate the
token and save it in Hakopod. If the organization requires approval, an owner
must approve the token before it can access those repositories.

Under **Resources per runner**, set CPU and memory reservations separately from
their limits. The form shows the total reservation across all concurrent slots.
Each slot includes the runner, Docker and sandbox overhead: reserve at least
200m CPU and 768Mi memory, with a memory limit of at least 4Gi. Requests must not
exceed their limits. CPU accepts cores or millicores (`0.25` or `250m`); memory
accepts quantities such as `1024Mi` or `1Gi`.

Scheduling checks reserved capacity, not current usage. For example, five slots
at 1200m each reserve six CPU cores even when the machine is idle. Lower CPU
reservations allow jobs to share spare CPU up to their limits, with slower builds
under contention. Capacity failures report the requested and available CPU and
memory and the shortage. Cloud allowances still apply. Editing an existing pool
preserves its resource overrides and inherited defaults until you change them.

Automatic node placement lets Kubernetes choose an eligible sandbox node.
Selecting a node keeps the pool on that exact node and preserves architecture,
resource, scheduling-taint and trusted Cloud allocation checks. The picker marks
nodes without a ready Managed Actions sandbox unavailable. A missing or
unavailable selected node never falls back to a different node; choose another
eligible node or restore the selected node before deploying.

Use your configured label in a workflow:

```yaml
jobs:
  test:
    runs-on: [hakopod]
    steps:
      - run: echo 'Hello from my runner'
```

The same configuration can be submitted through the API, CLI or TOML workflow:

```toml
schema_version = 1
name = "team-actions"

[services.runner]
size = "compute"
replicas = 2

[services.runner.actions]
organization = "your-team"
# runner_group_id = 42 # Optional; omit to discover GitHub’s default group.
credential = "github-runner-token"
labels = ["hakopod"]
timeout_minutes = 60
```

For a dedicated repository pool, replace `organization` with
`repository = "your-team/your-repository"` and omit `runner_group_id`.
Exactly one of `organization` or `repository` is required. Existing repository
configurations remain valid. Scope or group changes drain old runners using their
original registration scope before starting replacements. Keep the old credential
available until cleanup finishes.

The engine supplies the digest-pinned runner image. A runner application cannot
contain ordinary services, persistent volumes or custom host access. Create a
separate application for databases and application workloads. Runner pools
cannot be copied into previews or moved between applications.

## Isolation and capacity

Each single-job runner has its own Docker daemon inside gVisor. Docker actions,
job containers, service containers and Docker builds use that daemon. No host
Docker socket, host directory or Kubernetes credential is mounted. This is a
Linux sandbox; arbitrary devices, kernel modules and nested VMs are unavailable.

The declared per-slot CPU and memory budget includes the runner, Docker sidecar
and 512 MiB/100m sandbox allowance. At least 4 GiB of memory limit is required.
Workspaces default to 2 GiB of temporary disk. Set `actions.workspace_size_gib`
to 2–16 GiB for larger jobs. Source, tools, Docker images and build files share
this disk allowance. The node reserves it plus container/log headroom before
scheduling each slot; all workspace and Docker data is deleted after every job.
Docker uses VFS, so image layers and builds can require substantially more disk
than their compressed download size. The maximum lifetime includes startup and waiting for a job. Jobs requiring
more disk or a different runtime must use another runner type.

The GitHub runner-management token stays in control-plane secret storage. A pod
gets only its one-job JIT registration. Configuring this pool does not replace
Hakopod's application build provider.

## Reusing dependencies across jobs

Each job starts with a clean workspace. Use GitHub's `actions/cache` or the
cache option in an official setup action to download dependencies saved by a
previous job. GitHub stores the cache; runner workspaces and Docker daemons
remain private to one job. No extra Hakopod secret or persistent volume is
needed. Existing pools use the same workflow configuration after an upgrade.

For example, cache npm's downloaded packages using the lockfile as the key:

```yaml
steps:
  - uses: actions/checkout@v4
  - uses: actions/setup-node@v4
    with:
      node-version: '22'
  - uses: actions/cache@caa296126883cff596d87d8935842f9db880ef25 # v5
    with:
      path: ~/.npm
      key: ${{ runner.os }}-${{ runner.arch }}-npm-${{ hashFiles('**/package-lock.json') }}
      restore-keys: |
        ${{ runner.os }}-${{ runner.arch }}-npm-
  - run: npm ci
```

Choose the Node version required by your project; a cache does not install the
toolchain. `actions/setup-node` with `cache: npm` is an
alternative to a separate cache step. For other languages, cache the package
manager's download directory and include the dependency lockfile, operating
system and architecture in the key. Compiled dependencies also need a matching
compiler or runtime version. Run the package manager after restoration so a
fallback cache cannot replace lockfile validation.

GitHub enforces repository and branch access for these caches. The default
branch's cache may be restored from other branches, and pull requests can read
caches available to their base branch. Cache dependency downloads and build
outputs, never tokens, credentials or private files that a repository's pull
request authors should not read. Caches are an optimization: GitHub can evict
them, and a missing cache must leave the workflow able to install dependencies.

Runner 2.337.0 supports the current cache service and `actions/cache@v5`.
The promoted runner image includes tar and zstd, with archive round trips
smoke-tested on AMD64 and ARM64. The cache action selects zstd when present,
otherwise gzip. Existing saved image references remain valid: after the server
upgrade, busy runners finish their job and replacement runners use the promoted
image. Compression is part of GitHub's cache version, so the first zstd job can
miss an older gzip cache and populate a new one. A job container must provide
its own compatible archive tools. Installation firewalls must allow
GitHub Actions cache traffic, including `results-receiver.actions.githubusercontent.com`
and `*.blob.core.windows.net`, over HTTPS. The managed namespace's public egress
rule already permits this traffic without granting private-network access.

The cache mode in `actions-live.yml` saves a bounded fixture in one real managed
job, restores it in a different single-job runner, and checks an exact cache hit
and SHA-256. Its run-specific key can be removed after acceptance. A successful
archive smoke check alone does not verify GitHub upload, download or cross-job
reuse; consult the release verification record for the completed cache run.

## Building images for another architecture

`docker/setup-qemu-action` registers interpreters through the kernel's
`binfmt_misc` filesystem. The managed gVisor sandbox does not provide this
filesystem, so that action fails with `cannot mount binfmt_misc` even when the
nested Docker container requests `--privileged`. Do not add host mounts or
switch runners to a privileged host runtime to bypass this failure.

BuildKit has a separate user-space emulator path for Dockerfile `RUN`
instructions. The [runtime acceptance run](https://github.com/hakopod/hakopod/actions/runs/36533856191)
verified both AMD64 and ARM64 Dockerfile `RUN` instructions from each host
architecture with the pinned BuildKit image below, inside the managed gVisor
sandbox. This does not enable foreign
architecture `docker run` commands or arbitrary cross-architecture job containers.

For a BuildKit cross-build, omit `docker/setup-qemu-action` and configure a
container builder with its bundled emulators and native snapshotter:

```yaml
steps:
  - uses: actions/checkout@v4
  - uses: docker/setup-buildx-action@v3
    with:
      driver: docker-container
      # The single-job pod removes its whole private workspace on exit.
      cleanup: false
      driver-opts: |
        image=moby/buildkit:v0.33.0@sha256:6c2fa84a6b61ccd72899dde4239f8d5717f05f9a8ca6f3cad185fb1a95a94de3
      buildkitd-flags: --oci-worker-snapshotter=native
  # Authenticate to your registry before a build that pushes images.
  - uses: docker/build-push-action@v6
    with:
      context: .
      platforms: linux/amd64,linux/arm64
      push: false
      outputs: type=oci,dest=/tmp/image.tar
```

Both emulation and the native snapshotter consume the same per-job CPU, memory
and workspace limits. Emulated compilation may be substantially slower than a
native build. Native pools for each architecture remain an alternative for large
builds.

For languages with a native cross-compiler, keep compilation on the runner's
architecture and use emulation only for target execution. For example, a Go
build can use `FROM --platform=$BUILDPLATFORM golang:...` in its compile stage,
declare `ARG TARGETARCH`, and run `CGO_ENABLED=0 GOARCH="$TARGETARCH" go build`.
Use your verified digest-pinned compiler image. This avoids emulating the Go
compiler; CGO and architecture-specific system dependencies need a matching
cross-toolchain or a native pool. The runtime gate measures this route
separately from the fully emulated compiler route and executes both resulting
programs on their target architectures.

Local BuildKit cache disappears when an ephemeral runner is removed. To reuse
cache across jobs, add a private, repository-scoped registry cache to the
`docker/build-push-action` configuration after registry authentication:

```yaml
cache-from: type=registry,ref=ghcr.io/your-team/your-app:buildcache
cache-to: type=registry,ref=ghcr.io/your-team/your-app:buildcache,mode=max
```

Give unrelated repositories or trust boundaries separate cache references and
appropriate registry permissions. Registry cache is configured by the workflow;
Hakopod does not share private runner workspaces or Docker daemons between jobs.
The same-builder warm-cache result in the runtime report does not measure remote
cache download time or prove a cache hit in a subsequent GitHub job.

Use `cleanup: false` on `docker/setup-buildx-action` for these ephemeral runners.
Buildx's default cleanup deletes the compiler cache file by file through the
nested Docker API; the larger runtime acceptance build exceeded that API
request deadline on both host architectures. The pod owns its private Docker
daemon and workspace, so pod removal already reclaims them after the job. For
manual builders, `docker buildx rm --keep-state <builder>` stops the builder
without traversing its cache; retained state lasts only until the pod is removed.
Do not use this cleanup advice for a persistent shared runner.

## Updates and removal

Scaling down, pausing, configuration changes and license expiry drain busy jobs.
Draining and cleanup-pending runners continue to count against the pool's replica
limit. Completed runners are removed before replacements are registered.

Deleting a service cancels its running jobs. GitHub cleanup retries after API
outages; the application's Services page retains the cleanup status after its
service card disappears. Keep the application credential until cleanup finishes.
Application metadata cannot be deleted while registration cleanup remains.

Cloud requires a trusted Cloud Team grant and a separately provisioned eligible
sandbox node. User TOML cannot enable a Cloud entitlement. A runtime with no
trusted Cloud entitlement resolver fails closed.

## Verification

The implementation has tests for interrupted registration, missing JIT config,
draining capacity, expiry, deletion retries, secret isolation and lost controller
claims. These use real isolated PostgreSQL with deterministic provider failures.
`actions-live.yml` separately exercises real GitHub jobs through the product pod
builder; its credentials are single-job registrations, never the repository
administration token. Consult the release verification record for actual runs
and architecture coverage.

`actions-runtime.yml` runs a separate credential-free acceptance gate on both
AMD64 and ARM64 GitHub-hosted machines. The harness exports the product's runner
pod, namespace policies and quotas, then applies them only to a fresh named
`k3d-hakopod-dev` cluster. It checks:

- Compilation and execution of a Go checksum program on both target
  architectures, including execution of the compiler through BuildKit's
  user-space emulator.
- A multi-architecture manifest pushed to an isolated registry inside the
  sandbox, both platform images pulled back, and their programs executed again.
- Two simultaneous BuildKit requests with two worker operations allowed at a
  time, an intentionally failed build and successful reuse of its builder.
- Service DNS, workspace bind mounts, published loopback ports, namespace
  ingress and egress denial, and Kubernetes API denial with live reachability
  controls.
- Observation of the actual running runner image and removal of that observation
  after its pod is deleted; Docker sidecar restart before a build starts.
- A bounded 2.25 GiB write to a 2 GiB workspace, eviction within three minutes of
  the write, and a new pod for the same slot with no previous workspace, images
  or containers.

The gate runs on relevant pull requests, changes to `main`, and manual
dispatches. It tests the image digest pinned by that engine revision. Changes to
runner-image source are built and smoke-tested by `actions-runner-image.yml`;
promoting that image also requires updating the engine's digest pin and passing
this runtime gate. A pass against the previous pin does not qualify an
unpublished candidate image.

The `actions-runtime-<host>` artifact contains `report.json`, bounded fixture
logs and pod diagnostics. The report records phase times, manifest digest,
configured limits, sampled CPU, memory and disk peaks, and cleanup outcomes.
Startup is split using Kubernetes container timestamps, including the runner
file-copy step. Build timing separates builder startup, cold compilation and
push, native versus emulated compiler vertices, a warm-cache repeat and the
native cross-compiler route. The latter reuses already-downloaded base images.
Peaks are lower bounds sampled between checks; they are not continuous resource
profiles. A failed or missing gate is not successful runtime evidence.

This gate establishes a bounded acceptance workload, not an enterprise fleet
capacity claim: one active runner with a 2-core/4 GiB slot budget, an 8 GiB
workspace and two concurrent build requests on a 6 GiB disposable node. The
isolation probe adds a separate 64 MiB test pod briefly. Node loss, multi-node
scheduling, long-running fleet churn and provider quota behavior need their own
capacity and failure evidence. Do not extrapolate this workload's timings to a
larger fleet or an application's compiler, dependency graph and registry.

The slot budget includes the sandbox and is split between its containers. In
this 2-core fixture, the runner container has a 475m CPU limit and the Docker
sidecar has 1425m, with 100m reserved for the sandbox. A shell compilation and a
BuildKit compilation therefore have different CPU ceilings. The pod's declared
2-core total is not a 2-core limit for each container. Startup timings on these
fresh CI nodes also include cold image pulls; a node with cached pinned images
has a different startup workload.

The [September 29 runtime run](https://github.com/hakopod/hakopod/actions/runs/36538906615)
passed both host architectures at engine commit `d2f055b`. These are single-run
observations from the bounded fixture above, using its isolated local registry:

| Measurement | AMD64 host | ARM64 host |
| --- | ---: | ---: |
| Runner and Docker checkpoint, including cold image pulls | 33.36 s | 28.40 s |
| Runner file copy | 7 s | 5 s |
| Cold two-architecture build and push | 563.21 s | 378.90 s |
| Native compiler instruction | 77.98 s | 62.81 s |
| Emulated compiler instruction | 299.64 s | 227.85 s |
| Identical build and push using the same builder cache | 0.44 s | 0.42 s |
| Native cross-compilation and both target executions, base images cached | 80.58 s | 57.69 s |
| Two simultaneous readback builds on one runner | 3.09 s | 1.97 s |
| Stop builder while retaining state until pod deletion | 0.69 s | 0.55 s |
| Sampled runner working-set memory peak | 1.09 GiB | 1.07 GiB |
| Sampled ephemeral-storage peak | 6.34 GiB | 5.62 GiB |

The two host types use different CI hardware. These timings do not establish
architecture speed rankings or a production latency promise. The native
cross-compiler path avoids emulating the compiler, while the sub-second warm
result requires an existing builder cache. Neither measurement is a GitHub job
queue time or a restore from an external cache. Both hosts also passed actual
workspace eviction, replacement with a fresh workspace, physical volume
removal, namespace deletion and removal of the deleted runner's observed image.

Before migrating an existing workflow, `TestManagedActionsCandidateJobLive` can
run one disposable registration in `k3d-hakopod-dev`. It uses the product pod
builder with a 4 GiB memory budget and 8 GiB workspace. Set
`HAKOPOD_ACTIONS_CANDIDATE_TEST=1`, `HAKOPOD_TEST_KUBECONFIG` and
`HAKOPOD_ACTIONS_CANDIDATE_JIT_FILE` (a mode-0600 file containing only that JIT
configuration), then run the test with a 118-minute Go test timeout. An optional
`HAKOPOD_ACTIONS_CANDIDATE_LOG_FILE` saves at most 4 MiB of private diagnostics to
a new mode-0600 file before cleanup. Run candidates sequentially. A runner exiting
successfully does not establish a successful workflow: check GitHub's job
conclusion and recorded runner identity separately.
