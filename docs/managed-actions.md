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
2. Choose an application name, GitHub organization (the default) or repository
   (`owner/repository`), labels, replica
   count, architecture and maximum runner lifetime.
3. Organization pools use the GitHub default runner group unless you enter a
   runner group ID. Set which repositories can use the group in GitHub under
   **Organization Settings → Actions → Runner groups**. Hakopod does not change
   that access policy. A repository is not required for an organization pool.
4. During review, save an application-scoped fine-grained GitHub token with
   **Self-hosted runners: read and write** organization permission, or
   **Administration: read and write** for a repository pool. Both scopes also
   need **Actions: Read-only** under repository permissions for job steps and
   completed logs. Give the token access to every repository whose jobs the pool
   will run, including private repositories. Do not put the token itself in TOML
   or Git.
5. Review the changes and deploy. The service page shows GitHub-observed runner
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
