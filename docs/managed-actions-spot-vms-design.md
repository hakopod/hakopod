# Managed Actions on cloud VMs (design)

Status: design, not implemented. Nothing in this document is available in a
release yet.

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
administrator defines capacity in **Settings → Actions capacity** (API:
`/api/v1/settings/actions-capacity`), stored encrypted like other installation
credentials and audited on every change:

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
max_instances = 20                      # hard ceiling across all pools
```

GCP takes a project, zone list, network/subnetwork and a service account
(Workload Identity Federation or a key); Azure takes a subscription, one
resource group, a subnet and a service principal. Hakopod ships a Terraform
snippet per cloud that creates exactly the role it needs:

- AWS: `ec2:CreateFleet`, `RunInstances`, `CreateTags`, `Describe*`,
  `TerminateInstances` conditioned on the `hakopod-installation` tag, and
  `iam:PassRole` only if an instance profile is configured (none by default).
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

`size`, `workspace_size_gib` and `cache` do not apply to VM pools; the capacity
decides the machine. Validation refuses them together with `capacity`.

## Slot runtime

`internal/api/actions.go` already talks to its runtime through the
`actionsRuntime` interface (start, phase, delete, save config). A VM pool gets a
second implementation behind the same calls:

| Pod runtime | VM runtime |
|---|---|
| `SaveActionsConfig` writes a Secret | config is kept encrypted on the slot (it already is) and handed to the VM at launch |
| `StartActionsPod` | `Launch`: one instance, tagged `hakopod-installation`, `hakopod-pool`, `hakopod-slot` |
| `ActionsPodPhase` | instance state mapped to the same phases; `missing` when the cloud no longer has it |
| `DeleteActionsPod` | terminate by instance ID recorded on the slot |

Each cloud is a small package (`internal/actions/vm/aws`, `gcp`, `azure`) with
`Launch`, `State`, `Terminate` and `List(tags)`. AWS uses the SDK already in
`go.mod`. For GCP and Azure, plain REST over `net/http` with their token
endpoints is a few hundred lines and avoids two large SDK trees; revisit if it
grows.

Launch details:

- AWS: `CreateFleet` type `instant`, price-capacity-optimized across the listed
  types, `InstanceInitiatedShutdownBehavior=terminate`, IMDSv2 required with hop
  limit 1.
- GCP: `instances.insert` with `provisioningModel=SPOT`,
  `instanceTerminationAction=DELETE` and `maxRunDuration` set from
  `timeout_minutes`.
- Azure: `priority=Spot`, `evictionPolicy=Delete`, `maxPrice=-1`.

The instance boots stock Ubuntu 24.04. Cloud-init installs Docker and the runner
release pinned by version and SHA-256 (the same version as the runner image),
writes the JIT configuration, runs `run.sh --jitconfig` once and powers off.
The JIT configuration is delivered in user data. It is single-use and only
registers this one runner, and the VM is destroyed after the job; the
installation's GitHub credential never leaves the control plane. Prebuilt images
come later, when boot time matters.

The VM needs outbound HTTPS to GitHub and nothing inbound. It never talks to
Hakopod, so it works the same for an installation behind NAT.

## Reaper

A worker loop lists instances tagged with this installation and terminates any
that:

- belong to no slot, or to a slot in `cleanup`;
- have run longer than `timeout_minutes` plus 15 minutes;
- belong to a pool that was deleted or lost its capacity.

It also removes GitHub runners whose instance is gone, using the existing
cleanup path. `max_instances` is checked before every launch; reaching it leaves
the slot queued and says so in the pool status.

## Spot interruptions

GitHub does not requeue a job whose runner disappears; it fails with "lost
communication". So Hakopod reruns it:

1. When a slot's instance goes `missing` while the slot is `busy`, ask the cloud
   why: AWS state reason `Server.SpotInstanceTermination`, the GCP
   `compute.instances.preempted` operation, the Azure eviction event. Record
   `evicted` on the slot.
2. When the job completes as failed and its slot was evicted, call
   `POST /repos/{repo}/actions/runs/{run_id}/rerun-failed-jobs` with the jobs
   credential, up to `spot_retries` times per run. A rerun launches on
   on-demand capacity.
3. An instance type evicted three times in 30 minutes is skipped for an hour.

Reruns repeat every failed job in that run. Jobs that must not run twice, such
as deploys, belong in a pool with `spot_retries = 0` or on pods.

## Interfaces

- Dashboard: capacity settings page; pool form gets a "Run on" choice (this
  installation / a capacity) and shows per-slot instance ID, type, spot or
  on-demand, and evictions.
- API, CLI and MCP: capacity CRUD under installation administration; pool status
  adds the instance fields. `hakopod actions capacity test NAME` launches and
  terminates one instance to prove the credentials.

## Plan

1. AWS end to end: capacity settings, VM runtime, cloud-init, reaper,
   eviction reruns, docs. Acceptance in CI against a real account with a small
   budget: one job on spot, one forced interruption, one orphan reaped.
2. GCP.
3. Azure.
4. Prebuilt images and an optional warm instance per pool; switch demand to
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
- Prior art: RunsOn spot handling (https://runs-on.com/configuration/spot-instances/), github-aws-runners/terraform-aws-github-runner, cloudbase/garm
