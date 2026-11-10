# Managed Actions on cloud VMs (design)

Status: step 1 (AWS) and the AWS half of step 4 (runner images and a stopped
warm pool) are implemented, and launches, standbys and image builds have been
run against a real AWS account; see "Run jobs on AWS VMs" in
[managed-actions.md](managed-actions.md).
GCP, Azure and step 4 are design only. Where the implementation settled a
detail differently from the first draft, this document now describes what was
built.

Managed Actions runs each GitHub job in a single-use runner pod on the
installation's own nodes. That caps concurrency at whatever the cluster can
hold, and Docker jobs run inside gVisor with its known limits. This design adds
a second place to run a slot: a single-use VM in the customer's AWS, GCP or
Azure account, on spot capacity by default. Everything up to "start the runner"
stays as it is.

## What stays the same

- Demand: verified `workflow_job` deliveries wake a pool, and
  `scaledActionsReplicas` sizes it exactly as for pods (`replicas` is the
  maximum, `idle_seconds` and `min_replicas` keep their meaning).
- Registration: a slot's identity is persisted before GitHub is called, the
  control plane requests one JIT configuration per slot, and cleanup removes the
  GitHub runner by its recorded ID.
- Slot phases (`intent`, `starting`, `idle`, `busy`, `cleanup`), entitlement
  checks and drain behaviour. A VM slot is just a slot whose runtime is a VM.
- Licensing: this is part of `managed_actions` (Pro). There is no separate flag.

GitHub only in the first version. GitLab and Bitbucket keep using pods.

## Configuration

Cloud access belongs to the installation, not to an application. An
administrator defines capacity under `/api/v1/settings/actions-capacity` (CLI:
`hakopod actions capacity`; a dashboard page is still to come). The setting holds
no credential, only identifiers, so it is stored as plain JSON in the
installation settings, with an optimistic revision and an audit event on every
change:

```toml
[[capacity]]
name = "aws-spot"
cloud = "aws"
region = "eu-central-1"
role_arn = "arn:aws:iam::123456789012:role/hakopod-actions"
external_id = "generated-by-hakopod"
subnet_ids = ["subnet-0abc"]           # egress to GitHub required, nothing inbound
security_group_ids = ["sg-0abc"]
instance_types = ["c7i.xlarge", "c6i.xlarge", "m7i.xlarge"]
architecture = "amd64"
spot = true                             # false = on-demand only
disk_gib = 100
max_instances = 20                      # hard ceiling across all pools, standbys included
environments = ["shop/production"]      # project/environment pairs, or "*"
image = "runs-on-full"                  # or hakopod-minimal, ami-..., resolve:ssm:/...
warm_pool = 0                           # stopped standbys, 0-50
```

Hakopod's own AWS identity (environment keys, a web identity token or the
EC2 instance role; no shared config files) assumes
`role_arn`. The installation's external ID is generated on the first read of
the setting; a capacity may name its own instead. The API also returns the
installation ID, the value of the `hakopod-installation` tag. A capacity that a
pool or a live slot still names cannot be removed.

GCP takes a project, zone list, network/subnetwork and a service account
(Workload Identity Federation or a key); Azure takes a subscription, one
resource group, a subnet and a service principal. Hakopod ships a Terraform
snippet per cloud that creates exactly the role it needs:

- AWS: `ec2:CreateLaunchTemplate`, `DeleteLaunchTemplate`, `CreateFleet`,
  `RunInstances`, `CreateTags` on create only, `Describe*`,
  `TerminateInstances` conditioned on the `hakopod-installation` tag,
  `ssm:GetParameters` on Canonical's public parameters, and
  `iam:CreateServiceLinkedRole` for the fleet and spot service roles. Warm
  pools and `hakopod-minimal` add `StartInstances`, `ModifyInstanceAttribute`,
  `GetConsoleOutput`, `CreateTags` (only the slot, pool and pool-state keys),
  `CreateImage`, `DeregisterImage` and `DeleteSnapshot`, all conditioned on the
  installation tag (`aws:RequestTag` for the image and snapshot `CreateImage`
  creates). No `StopInstances`: standbys power themselves off. No
  `iam:PassRole`: runners get no instance profile. The policy and a Terraform
  snippet are in managed-actions.md.
- GCP: `compute.instanceAdmin.v1` on the project, `iam.serviceAccountUser` on the
  runner's service account.
- Azure: Virtual Machine Contributor and Network Contributor on the one resource
  group, never subscription-wide Contributor.

A pool opts in by naming the capacity:

```toml
[services.ci]
replicas = 10

[services.ci.actions]
organization = "acme"
credential = "github-runner-token"
labels = ["hakopod-spot"]
idle_seconds = 120
capacity = "aws-spot"        # new; omit for pods on this installation
spot_retries = 2             # new; 0 disables automatic reruns
```

`size`, `resources`, `architecture`, `node_name`, `workspace_size_gib` and
`cache` do not apply to VM pools; the capacity decides the machine. Validation
refuses them together with `capacity`, and refuses `capacity` for GitLab and
Bitbucket pools. Release acceptance refuses an active pool whose project and
environment the capacity does not list.

## Slot runtime

`internal/api/actions.go` already talks to its runtime through the
`actionsRuntime` interface (start, phase, delete, save config). A VM pool gets a
second implementation behind the same calls:

| Pod runtime | VM runtime |
|---|---|
| `SaveActionsConfig` writes a Secret | config is sealed with the authentication encryption key into the slot row until launch, then dropped |
| `StartActionsPod` | `Launch`: one instance, tagged `hakopod-installation`, `hakopod-capacity`, `hakopod-pool`, `hakopod-slot`; an instance already tagged with the slot is adopted instead |
| `ActionsPodPhase` | instance state mapped to the same phases (`Succeeded` once it shuts down); `missing` when the cloud no longer has it |
| `DeleteActionsPod` | terminate every instance tagged with the slot |
| `HasActionsConfig` | true only before the first launch, so a vanished VM is never relaunched with a spent configuration |

The VM runtime is chosen per slot from the slot's own configuration, so a pool
moved between pods and VMs still cleans up its old slots. `max_instances` is
checked before a runner is registered and enforced again, under a
per-capacity lock, when the slot reserves its place. A launch that started
nothing (no capacity for any listed type) retires the slot so a fresh one
retries; the instance and its ID are recorded on the slot.

Each cloud is a small package (`internal/actions/vm/aws`, `gcp`, `azure`) with
`Launch`, `State`, `Terminate`, `List(tags)` and `Check`. AWS speaks the EC2
and STS Query APIs over `net/http` (`internal/awsquery`), signed with the core
SDK module already in `go.mod`; adding the `config`, `ec2` and `sts` modules
would change `go.mod`, which requalifies every managed database runtime. For GCP and Azure, plain REST over `net/http` with their token
endpoints is a few hundred lines and avoids two large SDK trees; revisit if it
grows.

Launch details:

- AWS: a throwaway launch template, then `CreateFleet` type `instant`,
  price-capacity-optimized across every listed type and subnet,
  `InstanceInitiatedShutdownBehavior=terminate`, IMDSv2 required with hop limit
  1, an encrypted gp3 root volume, and the image resolved at launch from
  Canonical's public SSM parameter for Ubuntu 24.04. The template is deleted
  right after, since its user data holds the configuration. No client token is
  used: a fleet that started nothing would replay that failure for the token.
- GCP: `instances.insert` with `provisioningModel=SPOT`,
  `instanceTerminationAction=DELETE` and `maxRunDuration` set from
  `timeout_minutes`.
- Azure: `priority=Spot`, `evictionPolicy=Delete`, `maxPrice=-1`.

The instance boots the capacity's image (below). The boot script installs
Docker and the runner release pinned by version and SHA-256 only if the image
lacks them, writes the JIT configuration, runs `run.sh --jitconfig` once and
powers off.
The JIT configuration is delivered in user data. It is single-use and only
registers this one runner, and the VM is destroyed after the job; the
installation's GitHub credential never leaves the control plane.

The VM needs outbound HTTPS to GitHub and nothing inbound. It never talks to
Hakopod, so it works the same for an installation behind NAT.

## Images and warm pool

Measured in us-east-2 on on-demand `t4g.small` (x86: `t3a.small`), from the
launch (or claim) call, on the instance's clock; median [min–max] of three runs
(two on x86). "First job" is an image pull, a shallow `git clone` and reading
the runner's files (about 460 MB on arm64).

| Start | Runner starting | Docker answers | First job done |
|---|---|---|---|
| cold, stock Ubuntu (Docker and runner installed at boot, one run) | 73 s | 74 s | |
| cold, `runs-on-full` | 15.6 [14.7–16.6] s | 25.9 s | 42.8 s |
| cold, `hakopod-minimal` | 24.2 [22.1–42.3] s | 30.5 s | 53.7 s |
| cold, `hakopod-minimal`, 300 MiB/s volume initialization | 21.9 [20.9–36.3] s | 27.6 s | 38.8 s |
| standby, `runs-on-full` | 15.0 [14.9–15.4] s | 19.4 s | 31.9 s |
| standby, `hakopod-minimal` | 15.2 [15.1–19.3] s | 19.8 s | 32.0 s |
| x86 cold, `runs-on-full` | 18.4 s | 30.0 s | 54.1 s |
| x86 cold, `hakopod-minimal` | 50.8 s | 89.0 s | 227.5 s |

The claim itself (tags, two attribute changes, start) takes about 3 s of API
calls, included above. A `hakopod-minimal` build takes about 11 minutes, most
of it waiting for the snapshot.

What the numbers say: most of the gain is the image, and `runs-on-full` wins
or ties every cold launch, so it is the default. A freshly built image is slow
cold because every new volume lazily reads its blocks from the snapshot; the
first launch after a build is the slowest (31 to 42 s to the runner), and x86
is much worse (Docker and containerd took 15 to 18 s each). Provisioned volume
initialization (`VolumeInitializationRate`, about $0.03 a launch at 300 MiB/s
for an 8 GiB snapshot) makes the first job faster but not the runner start. A
standby removes the difference: both images start the runner in about 15 s. So
`hakopod-minimal` is for those who will not boot a third-party image, paired
with a warm pool.

Stage 1, images. A capacity names `image`:

- `runs-on-full` (default): RunsOn's public GitHub-compatible Ubuntu 24.04
  image, looked up at launch by owner `135269210855` and name
  `runs-on-v2.2-ubuntu24-full-<x64|arm64>-*`, newest first, cached an hour.
  Docker and RunsOn's build of the runner (2.338.2 at the time of writing) are
  preinstalled; the runner lives in `/home/runner`. Its root snapshot is
  30 GiB. RunsOn's image repository is MIT licensed and says the images are
  for RunsOn "or for your own usage"; RunsOn's commercial license does not say
  whether it covers them outside RunsOn. managed-actions.md says so to admins.
- `hakopod-minimal`: built in the customer's account by the Actions worker, one
  step per sweep, so a restart resumes it: launch stock Ubuntu with an 8 GiB
  disk and shutdown behaviour `stop`, install Docker, the pinned runner and
  `hakopod-runner.service`, mask unneeded units, power off; `CreateImage` with
  the image and snapshot tagged on creation; wait for `available`; terminate
  the builder; deregister older images of that capacity and delete their
  snapshots, tag-guarded. State lives in `actions_capacity_images`.
- `ami-…` or `resolve:ssm:/…`: the customer's own Ubuntu image.

The boot script is the same everywhere and skips what the image already has.

Stage 2, a stopped warm pool, after RunsOn and Depot. `warm_pool` standbys per
capacity are on-demand instances (EC2 cannot stop a one-time spot instance)
tagged `hakopod-pool-state=standby` and `hakopod-image`. Each boots once with
user data that sets up the runner user, reads the runner directory and starts
Docker, so the stopped EBS volume holds those blocks, then powers off. A job
claims one: a unique index on the slot's `instance_id` decides between
concurrent claims, then `CreateTags`, `ModifyInstanceAttribute` (terminate on
shutdown, then the job's user data) and `StartInstances`. A claimed standby
reads as starting for three minutes so the slot is not mistaken for finished.
If no standby is stopped, the slot launches cold, spot by default.

User data must run again on the standby's next boot. cloud-init runs user data
scripts once per instance, so on every image but `hakopod-minimal` the script
is sent as a MIME multipart document whose `text/cloud-config` part sets
`cloud_final_modules: [[scripts-user, always]]`; cloud-init fetches the new
user data at boot and runs it. Verified on AWS: a standby given new user data
while stopped ran it on its next start. `hakopod-minimal` turns cloud-init's
user-data modules off and runs the plain script from its own unit, which reads
user data over IMDSv2 on every boot.

One worker (an advisory lock) refills pools every 30 seconds within
`max_instances` minus running slots, and terminates standbys of a superseded
image, extras, and any stuck booting or stopping for 15 minutes. Slots reserve
against `max_instances` without counting standbys; the sweep trims extras
instead. The reaper leaves standbys and builders alone.

## Reaper

A worker loop lists instances tagged with this installation and terminates any
that:

- belong to no slot;
- are a second instance for a slot that already recorded another (a lost
  launch response);
- have run longer than `timeout_minutes` plus 15 minutes, idle or not;
- belong to a pool that was removed.

A capacity cannot be deleted while it is in use, and a pool moved to another
capacity drains its old slots through the normal retirement path. Slots in
`cleanup` are not reaped: a draining slot is marked `cleanup` while its job
still runs, and the reconcile terminates the VM once the runner is idle. The VM also
stops its runner after the same time limit on its own.

It also removes GitHub runners whose instance is gone, using the existing
cleanup path. `max_instances` is checked before every launch; reaching it leaves
the slot queued and says so in the pool status.

## Spot interruptions

GitHub does not requeue a job whose runner disappears; it fails with "lost
communication". So Hakopod reruns it:

1. When a slot's instance goes away, ask the cloud why: AWS state reason
   `Server.SpotInstanceTermination`, the GCP `compute.instances.preempted`
   operation, the Azure eviction event. Record the eviction (kept 30 days, shown
   in pool status).
2. When a `workflow_job` delivery reports a job on that slot's runner completed
   as failed, attach its run to the eviction. The worker then calls
   `POST /repos/{repo}/actions/runs/{run_id}/rerun-failed-jobs` with the jobs
   credential, once per run attempt and up to `spot_retries` times per run. A
   refusal is recorded on the eviction with what to fix and does not count.
   Each rerun grants the pool one on-demand launch per lost job; a runner that
   is already idle may still take the rerun on spot. Reruns therefore need the
   workflow_job webhook even for fixed pools.
3. An instance type evicted three times in 30 minutes is skipped for an hour;
   if every type is skipped, launches go on-demand.

Reruns repeat every failed job in that run. Jobs that must not run twice, such
as deploys, belong in a pool with `spot_retries = 0` or on pods.

## Interfaces

- Dashboard: capacity settings page; pool form gets a "Run on" choice (this
  installation / a capacity) and shows per-slot instance ID, type, spot or
  on-demand, and evictions.
- API, CLI and MCP: capacity CRUD under installation administration; pool status
  adds the instance fields and evictions. `hakopod actions capacity test NAME`
  assumes the role and reads the network settings; with `--launch` it also
  launches and terminates one small on-demand instance. MCP reads go through
  the generic `api_call`.

## Plan

1. AWS end to end: capacity settings, VM runtime, cloud-init, reaper,
   eviction reruns, docs. Implemented and unit-tested against fakes of EC2 and
   GitHub. Still open: acceptance in CI against a real account with a small
   budget (one job on spot, one forced interruption, one orphan reaped), and
   the dashboard pages.
2. GCP: implemented in `internal/actions/vm/gcp` and unit-tested against a
   fake Compute Engine API. Still open: a run against a real project. See
   "Compute Engine" below.
3. Azure.
4. Prebuilt images and a stopped warm pool: done for AWS (see "Images and warm
   pool"). Still open: a schedule for the pool size, and switching demand to
   `actions/scaleset` long polling once it leaves preview, so missed webhooks
   stop mattering.

Rough effort: two to three weeks for step 1, about a week each for 2 and 3.

## Compute Engine

The GCP provider implements the same `vm.Provider` over the Compute Engine
REST API with the standard library, so no Google SDK module enters go.mod.

- Identity: Hakopod's own token (metadata server, or a service account key
  file) is exchanged with `generateAccessToken` for one of the capacity's
  service account. This mirrors AssumeRole; no key is stored per capacity.
  Workload identity federation (for a control plane on AWS) is left until
  someone needs it.
- Tags become labels for filtering. Label values allow only `[a-z0-9_-]`, so
  values such as `application/service` or an image path are labelled by hash,
  and the exact tags are kept in instance metadata (`hakopod-tags`) and in an
  image's description. Listings filter by label and then compare exact tags.
- Instance IDs are `zone/name`; names are `hakopod-<slot>-<random>`, which also
  lets an IAM condition confine the role to Hakopod's VMs.
- Launch tries every machine type in every zone in order. Insert operations
  report `ZONE_RESOURCE_POOL_EXHAUSTED` or quota errors only once done, so each
  attempt waits for its operation.
- Spot VMs use `instanceTerminationAction = STOP`, so a preempted VM stays
  readable. The zone's `compute.instances.preempted` operations mark it
  interrupted. Compute Engine never deletes a VM on guest shutdown, so the
  slot and the sweep delete stopped runners; the sweep now reaps stopped
  instances that are not standbys or builders.
- The notice is 30 seconds, an ACPI shutdown. The runner receives the shutdown
  signal and GitHub fails the job at once, as measured on AWS, so there is no
  IMDS watcher on GCP. Because the failure can now arrive before any sweep, the
  `workflow_job` handler looks the slot's VM up before attaching the run.
- Runners boot cloud-init user data from the `user-data` metadata key, which
  Ubuntu's Compute Engine images read. The `hakopod-minimal` boot unit reads it
  from the Compute Engine metadata server when the DMI product name says it is
  on Compute Engine, and from IMDSv2 otherwise.
- Cloud Run jobs were considered and rejected: no Docker daemon (Docker actions
  and service containers fail), and a vCPU-hour costs several times a Spot
  VM's.

## Azure

The Azure provider implements the same `vm.Provider` over the Azure Resource
Manager REST API (compute `2024-07-01`, network `2024-05-01`) with the
standard library, so no Azure SDK module enters go.mod.

- Identity: Hakopod's own token, from AKS workload identity (a federated
  client assertion), an app registration's client secret, or the IMDS managed
  identity, in that order. The customer assigns that identity a custom role on
  the runners' resource group and on the VNet; nothing is stored per
  capacity. There is no impersonation step as on Google Cloud: the role
  assignment plays the part of the trust policy.
- Tags hold the exact Hakopod tags; Azure tag values take 256 characters, so
  nothing is hashed. Listings read the resource group's VMs, compare tags
  client side and read each match's instance view for its power state.
- Instance IDs are `resourceGroup/name`; names are `hakopod-<slot>-<random>`.
- Launch is one PUT of the VM with an inline NIC and Standard public IP,
  `deleteOption: Delete` on NIC, IP and OS disk, boot diagnostics on managed
  storage, a throwaway SSH key and the boot script as `customData`. Each size
  is tried in each zone; allocation errors (`SkuNotAvailable`,
  `AllocationFailed`, `ZonalAllocationFailed`, `Overconstrained…`, quota and
  `SpotVM…` codes) arrive in the async operation, so each attempt waits for
  it and deletes the failed VM before the next, which gets a new name.
- Spot VMs use `evictionPolicy: Deallocate` and `maxPrice: -1`. Hakopod never
  deallocates a runner, so a deallocated Spot VM is reported interrupted. A
  runner that powers off is `stopped`, still allocated and billed, and is
  deleted by the slot or the sweep as on Compute Engine.
- The notice is a `Preempt` scheduled event, at least 30 seconds ahead. The
  boot script recognises Azure by DMI (`Microsoft Corporation`, or Azure's
  chassis asset tag), polls `metadata/scheduledevents` and stops the runner
  as soon as an event names its VM.
- Not yet: warm pools (stopping and restarting a VM with new `customData` is
  not possible; `userData` would be) and `hakopod-minimal` images (a managed
  image or gallery version from a generalised VM). Validation refuses both,
  and every Claim and image call returns an error.

## Open questions

- Should capacity be shareable across projects, or bound to one project and
  environment like other installation grants? The draft binds it to a list of
  project environments.
- Windows and macOS runners are out of scope.
- GitHub's postponed per-minute fee for self-hosted runners would apply here too
  if it comes back.

## References

- GitHub JIT runners: https://docs.github.com/en/rest/actions/self-hosted-runners
- Rerun failed jobs: https://docs.github.com/en/rest/actions/workflow-runs#re-run-failed-jobs-from-a-workflow-run
- AWS spot interruptions: https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/spot-instance-termination-notices.html
- GCP spot VMs: https://cloud.google.com/compute/docs/instances/spot
- Azure spot VMs: https://learn.microsoft.com/en-us/azure/virtual-machines/spot-vms
- actions/scaleset: https://github.com/actions/scaleset
- RunsOn runner images: https://github.com/runs-on/runner-images-for-aws
- Run user data on every boot: https://repost.aws/knowledge-center/execute-user-data-ec2
- Prior art: RunsOn spot handling (https://runs-on.com/configuration/spot-instances/), github-aws-runners/terraform-aws-github-runner, cloudbase/garm
