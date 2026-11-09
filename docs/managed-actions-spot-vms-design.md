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

Measured on `t4g.small` in us-east-2, launch (or claim) call to the boot script
starting `run.sh`, on the instance's clock:

| Start | Runner starting | Docker answers and `config.sh --version` runs |
|---|---|---|
| cold, stock Ubuntu (Docker and runner installed at boot) | 73 s | 74 s |
| cold, `runs-on-full` | 16 s | 25 s |
| cold, `hakopod-minimal` | 52 s | 92 s |
| standby, `runs-on-full` | 14 s | 21 s |
| standby, `hakopod-minimal` | 15 s | 21 s |

The earlier hand measurement of stock Ubuntu was about 21 s from launch to
`running` plus about 65 s of cloud-init installs. The claim itself (tags, two
attribute changes, start) took about 3 s of API calls, included above.

What the numbers say: most of the gain is the image. Starting a stopped
instance is barely faster than launching one (14 s against 16 s to the runner
on `runs-on-full`); a standby mainly saves the lazy EBS reads of a cold volume
(Docker and the runner answer 4 s sooner) and hides a freshly built image's
slow first boots (`hakopod-minimal`: 15 s warm against 52 s cold, where Docker
alone took 17 s to start reading blocks from the new snapshot). These are
single runs on `t4g.small`, timed on the instance clock.

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
2. GCP.
3. Azure.
4. Prebuilt images and a stopped warm pool: done for AWS (see "Images and warm
   pool"). Still open: a schedule for the pool size, and switching demand to
   `actions/scaleset` long polling once it leaves preview, so missed webhooks
   stop mattering.

Rough effort: two to three weeks for step 1, about a week each for 2 and 3.

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
