# Managed Actions

Open **Catalog → Automation → Managed Actions** to create an organization or repository
GitHub Actions runner pool. Each replica is one concurrent job slot. This is a
Pro capability (`managed_actions` in the signed installation license).

## Installation

The installation needs the dedicated `hakopod-actions` sandbox. On a completed
installer-managed Linux installation, extract and verify the installer kit for
your installed Hakopod release, then run from that kit on the server:

```sh
sudo python3 ./installer/modules.py managed-actions
```

Run this during a maintenance window: it restarts the installer-owned K3s unit.
It preserves the existing containerd template, adds a separate pinned gVisor
runtime, and leaves the default runtime unchanged. It refuses an unowned
runtime, edited managed configuration, an external cluster, or any node that
publishes Actions eligibility without this installation's ownership label.
Other runtimes in the containerd configuration, such as a separately installed
gVisor handler named `runsc`, are left alone as long as they do not use the
`hakopod-actions` name or forward its workspace annotations.
Existing installations retain their original maintenance helpers on upgrade;
use the new release's verified kit for this optional module.

A first-time server installation tries the shared workspace profile
(`shared-overlay2-v1`), which runs Docker on overlay2 instead of VFS. If the
node's qualification probe fails, the node keeps VFS and the next maintenance
run qualifies it again. Existing installations keep their saved profile and
can enable it explicitly from that same verified installer kit:

```sh
sudo python3 ./installer/modules.py managed-actions --workspace-profile shared-overlay2-v1
```

This maintenance action withdraws the node's workspace capability before it
restarts K3s. It publishes the versioned capability only after the runtime
configuration and an isolated workspace probe pass. A failed probe leaves the
node ineligible for this profile. Enabling it affects only fresh GitHub job
pods scheduled on a node carrying the matching capability; running pods,
GitLab runners and Bitbucket runners keep their existing storage behavior.
This profile does not provide a build-speed guarantee. Check the release's
verification record before enabling it in production.

To return future jobs to VFS, run another maintenance action:

```sh
sudo python3 ./installer/modules.py managed-actions --workspace-profile vfs
```

Jobs already running keep their original workspace until they finish. Omitting
`--workspace-profile` preserves the profile previously selected by the
installer; a first-time server installation tries `shared-overlay2-v1` and falls
back to VFS. Agents use VFS.

## Additional runner nodes

An installer-managed server can run Managed Actions on additional Linux
machines, for example cloud spot instances, joined as K3s agents. Each machine
is prepared on its own host because the runtime check reads that host's
generated containerd configuration and verifies the pinned gVisor files there.
No server credential is copied to the agent.

Status: covered by installer and placement unit tests. It has not yet been
exercised on a real multi-node installation; treat the first rollout as an
acceptance run.

Requirements for each agent: Linux on AMD64 or ARM64 with systemd, Python 3.11
or newer (for example Ubuntu 24.04 or Debian 12), outbound HTTPS to GitHub for
the pinned runtime download, and the server's exact K3s release.

1. **Join the machine as a dedicated runner node.** In the dashboard, create a
   short-lived worker enrollment (see [the cockpit guide](cockpit.md)). It
   shows the server URL and an expiring token. Then, on the new machine:

   ```sh
   curl -sfL https://get.k3s.io | INSTALL_K3S_VERSION=<server's K3s release> \
     K3S_URL=https://<supervisor_host>:6443 K3S_TOKEN=<enrollment token> \
     sh -s - agent --node-name <name> --node-ip <private address> \
     --node-taint hakopod.io/actions-only=true:NoSchedule
   ```

   `kubectl get nodes -o wide` on the server shows its K3s release. The
   `hakopod.io/actions-only=true:NoSchedule` taint reserves the machine for
   runner pods: application workloads never schedule there, so interrupting
   the machine cannot take an application down. Runner pods tolerate only
   this exact taint; any other NoSchedule or NoExecute taint makes the node
   unavailable for Actions. Leave the taint out only if the machine should
   also run applications.

   The simplest network is the server's own private network, for example the
   same AWS VPC. Across networks, a tunnel such as Tailscale must make the
   server's `supervisor_host` and `node_ip` reachable from the agent, and the
   agent's node IP reachable from the server. Flannel VXLAN traffic is not
   encrypted and must not cross the public internet. Allow TCP 6443 from
   agents to the server, and TCP 10250 and UDP 8472 between nodes only.

2. **Adopt it on the server.** Only the server operator makes a node part of
   the installation:

   ```sh
   sudo python3 ./installer/modules.py managed-actions --adopt-node <name>
   ```

   Adoption refuses a control-plane node, a node that is not ready, a node of
   another installation, and an agent that is not on the server's exact K3s
   release. It warns when the dedicated taint is missing and prints the
   command for step 4, including the installation ID.

3. **Publish the RuntimeClass.** Run `modules.py managed-actions` on the server
   once as described above. To keep CI off the server and avoid restarting
   its K3s, publish only the RuntimeClass instead:

   ```sh
   sudo python3 ./installer/modules.py managed-actions --runtime-class-only
   ```

4. **Prepare the agent.** Copy the same verified installer kit to the agent and
   run on the agent:

   ```sh
   sudo python3 ./installer/actions_runtime.py agent --node <name> \
     --installation-id <id>
   ```

   The K3s binary and data directory are read from the running service, so a
   custom data directory or binary location works. Pass `--unit` if the
   agent's systemd unit is not `k3s-agent`. The command refuses, before
   changing anything, a node that is not adopted by that installation, a
   different host (the node's machine ID must match `/etc/machine-id`), a
   control-plane node, a K3s release that differs from the server's, a K3s
   binary writable by anyone but root, and a missing or changed RuntimeClass.
   It then withdraws the node's Actions label, installs the pinned runtime,
   restarts the agent's K3s, verifies the generated configuration and runtime
   files, and only then publishes `hakopod.io/actions-runtime=ready` with the
   node's own kubelet identity. Kubernetes restricts that identity to its own
   node.

Automatic placement then uses any ready, schedulable node carrying that label;
nodes without it are never used. A pool pinned to a node stays on it and waits
while the node is unavailable. Agents support the default VFS workspace
profile only: the `shared-overlay2-v1` qualification probe needs the server's
administrator credential and binary on the probed host. If the server has
qualified `shared-overlay2-v1`, automatic GitHub placement prefers qualified
nodes, so keep the server on VFS or pin pools to agents when CI must run there.

### Operating runner nodes

- **Maintenance.** Step 4 restarts the agent's K3s. Cordon and drain the node
  first if it is running jobs you do not want to lose.
- **Upgrades.** After upgrading Hakopod, repeat step 4 on every agent with the
  new release's kit so it runs the release's pinned runtime. After upgrading
  the server's K3s, upgrade each agent's K3s to the same release and repeat
  step 4.
- **Interruption.** A reclaimed spot machine ends its running jobs, and GitHub
  reports them as failed. A cloud interruption handler that cordons and drains
  the node on the interruption notice, such as AWS Node Termination Handler,
  avoids starting new jobs on a machine about to disappear. Delete the Node
  object of a terminated machine (`kubectl delete node <name>`) so its runner
  pods are released and replacements start on remaining eligible nodes.
- **Replacement.** A replacement machine, even with the same name, must be
  joined, adopted and prepared again.
- **Removal.** To stop new jobs on a node, remove its label with
  `kubectl label node <name> hakopod.io/actions-runtime-`, then drain it.
  Pools pinned to that node wait until it is prepared again.
- **Trust.** An adopted agent is a cluster member. Its kubelet can change its
  own node's labels, so join only machines you administer and protect their
  root access like the server's.
- **Availability.** The server's database and control plane remain on one
  machine. Agents add CI capacity, not availability.

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

## Scale to zero

A GitHub pool can stop all runners while no jobs are waiting. Set
`idle_seconds` and `replicas` becomes the maximum number of concurrent jobs:

```toml
[services.runner]
replicas = 4

[services.runner.actions]
organization = "your-team"
credential = "github-runner-token"
labels = ["hakopod"]
idle_seconds = 300
# min_replicas = 1 # Optional; keeps one runner online while idle.
```

| Setting | Default | Allowed values |
| --- | --- | --- |
| `idle_seconds` | Omitted (fixed pool) | 60–86,400 seconds |
| `min_replicas` | 0 | 0 up to `replicas`; requires `idle_seconds` |

The pool wakes from GitHub `workflow_job` webhook deliveries. Each queued job
whose `runs-on` labels all appear on the pool (its labels plus `self-hosted`,
`linux` and its architecture) and whose repository matches the pool's
repository or organization starts one runner, up to `replicas`. Idle runners
stay online for `idle_seconds` after the last job event, then the pool returns
to `min_replicas`. A runner that finishes its job is not replaced unless more
jobs are queued or `min_replicas` requires it. Busy jobs are never interrupted. An idle pool appears as
**Sleeping** on its service.

Deliveries arrive through a GitHub connection under **Settings → Git**. New
GitHub Apps created with builds enabled subscribe to **Workflow jobs**
automatically. For an existing App, or a repository or organization webhook
pointing at the connection's webhook URL, enable the **Workflow jobs** event in
GitHub. A connection limited to a project and environment only wakes pools in
that scope; the installation-wide connection can wake any matching pool.

GitHub does not retry failed webhook deliveries on its own. If Hakopod is
unreachable when a job is queued, that job waits until another job event for
the pool arrives, a runner from `min_replicas` picks it up, or you redeliver it
from the webhook's **Recent Deliveries** page. GitHub cancels jobs still queued
after 24 hours, and Hakopod forgets demand at the same limit. Use
`min_replicas = 1` where a missed delivery must not delay a job. GitLab and
Bitbucket pools keep a fixed size.

Each wake-up starts a fresh runner, so the first job after an idle period also
waits for runner and Docker startup.

### Live verification

`actions-live.yml` with `scale_to_zero` runs `TestActionsScaleToZeroGitHubLive`
on a GitHub-hosted machine. It deploys a pool with `idle_seconds = 60` to a fresh
named `k3d-hakopod-dev` cluster with the gVisor sandbox, checks that it deploys
asleep with no runner pods, then lets a companion job in the same workflow run
queue on the pool's label.

Real in this run: PostgreSQL, the deployment path, the webhook endpoint and its
HMAC check, demand recording, the scheduled reconciler, the sandboxed runner pod,
GitHub assigning and running the queued job on that runner, the job's
conclusion, and the pool returning to zero runners after the idle window.

Simulated: GitHub cannot reach a disposable CI machine, so the test builds each
signed `workflow_job` delivery from the real job's ID, labels and status, read
with the run's read-only token. CI never holds the repository administration
token, so the registrations are two pre-issued single-job JIT configurations,
and the runner's online and busy state comes from its pod and the real job record
rather than the self-hosted runners API. The JIT registration the idle window
keeps warm is not deleted from GitHub; GitHub removes the unused offline runner
later.

A repository administrator mints the registrations and dispatches the run:

```sh
label=hakopod-scale-$(date +%s)
scripts/actions/mint-scale-jit.sh hakopod/hakopod "$label"
gh workflow run actions-live.yml --repo hakopod/hakopod --ref <branch> \
  -f scale_to_zero=true -f runner_label="$label" -f jit_secret=HAKOPOD_ACTIONS_SCALE_JIT
```

Mint fresh registrations with a new label for each run.

## Run jobs on AWS VMs

A GitHub pool can run each job on a single-use EC2 instance in your own AWS
account instead of a runner pod. Concurrency is then bounded by your AWS quota
rather than by this installation's nodes, and Docker runs natively on the VM,
without gVisor. Spot capacity is the default.

This is new and has not yet been run against a real AWS account by the Hakopod
team. Try it with a small `max_instances` first.

How it works:

- Demand, `replicas`, `idle_seconds` and `min_replicas` behave as for pods.
- For each slot the control plane requests one single-use JIT configuration
  from GitHub and launches one instance with it in user data. The pool's GitHub
  credential never leaves the control plane.
- The instance boots stock Ubuntu 24.04, installs Docker and GitHub runner
  2.337.0 (checked against its published SHA-256), runs one job as an
  unprivileged user and powers off. Shutdown terminates the instance.
- Instances need outbound HTTPS to GitHub and nothing inbound. They never
  contact Hakopod.

### Prepare the AWS account

Read the installation's external ID and installation ID first:

```sh
hakopod actions capacity list
```

Create a role for Hakopod to assume. Its trust policy names the AWS identity
Hakopod runs as (its pod role, instance role or user; Hakopod uses the AWS SDK's
default credential chain) and requires the external ID:

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Principal": {"AWS": "arn:aws:iam::111111111111:role/hakopod-control-plane"},
    "Action": "sts:AssumeRole",
    "Condition": {"StringEquals": {"sts:ExternalId": "hakopod-EXTERNAL_ID"}}
  }]
}
```

Its permissions let it launch tagged instances and terminate only instances
tagged with this installation:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "Read",
      "Effect": "Allow",
      "Action": "ec2:Describe*",
      "Resource": "*"
    },
    {
      "Sid": "LaunchTemplates",
      "Effect": "Allow",
      "Action": ["ec2:CreateLaunchTemplate", "ec2:DeleteLaunchTemplate"],
      "Resource": "*"
    },
    {
      "Sid": "Launch",
      "Effect": "Allow",
      "Action": ["ec2:CreateFleet", "ec2:RunInstances"],
      "Resource": "*"
    },
    {
      "Sid": "TagOnCreate",
      "Effect": "Allow",
      "Action": "ec2:CreateTags",
      "Resource": "*",
      "Condition": {"StringEquals": {"ec2:CreateAction": ["CreateLaunchTemplate", "CreateFleet", "RunInstances"]}}
    },
    {
      "Sid": "TerminateOwnInstances",
      "Effect": "Allow",
      "Action": "ec2:TerminateInstances",
      "Resource": "*",
      "Condition": {"StringEquals": {"ec2:ResourceTag/hakopod-installation": "INSTALLATION_ID"}}
    },
    {
      "Sid": "UbuntuImage",
      "Effect": "Allow",
      "Action": "ssm:GetParameters",
      "Resource": "arn:aws:ssm:*::parameter/aws/service/canonical/*"
    },
    {
      "Sid": "FleetServiceRoles",
      "Effect": "Allow",
      "Action": "iam:CreateServiceLinkedRole",
      "Resource": "*",
      "Condition": {"StringEquals": {"iam:AWSServiceName": ["ec2fleet.amazonaws.com", "spot.amazonaws.com"]}}
    }
  ]
}
```

No `iam:PassRole` is needed: runners get no instance profile. Hakopod also
checks the installation tag itself before every termination.

The same in Terraform, with a security group that allows outbound traffic only:

```hcl
variable "hakopod_principal_arn" {}
variable "external_id" {}
variable "installation_id" {}
variable "vpc_id" {}

resource "aws_iam_role" "hakopod_actions" {
  name = "hakopod-actions"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { AWS = var.hakopod_principal_arn }
      Action    = "sts:AssumeRole"
      Condition = { StringEquals = { "sts:ExternalId" = var.external_id } }
    }]
  })
}

resource "aws_iam_role_policy" "hakopod_actions" {
  role = aws_iam_role.hakopod_actions.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      { Effect = "Allow", Action = "ec2:Describe*", Resource = "*" },
      { Effect = "Allow", Action = ["ec2:CreateLaunchTemplate", "ec2:DeleteLaunchTemplate", "ec2:CreateFleet", "ec2:RunInstances"], Resource = "*" },
      { Effect = "Allow", Action = "ec2:CreateTags", Resource = "*",
        Condition = { StringEquals = { "ec2:CreateAction" = ["CreateLaunchTemplate", "CreateFleet", "RunInstances"] } } },
      { Effect = "Allow", Action = "ec2:TerminateInstances", Resource = "*",
        Condition = { StringEquals = { "ec2:ResourceTag/hakopod-installation" = var.installation_id } } },
      { Effect = "Allow", Action = "ssm:GetParameters", Resource = "arn:aws:ssm:*::parameter/aws/service/canonical/*" },
      { Effect = "Allow", Action = "iam:CreateServiceLinkedRole", Resource = "*",
        Condition = { StringEquals = { "iam:AWSServiceName" = ["ec2fleet.amazonaws.com", "spot.amazonaws.com"] } } },
    ]
  })
}

resource "aws_security_group" "hakopod_runners" {
  name   = "hakopod-runners"
  vpc_id = var.vpc_id
  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

output "role_arn" { value = aws_iam_role.hakopod_actions.arn }
output "security_group_id" { value = aws_security_group.hakopod_runners.id }
```

Use subnets with a route to the internet (a NAT gateway, or a public subnet with
public IPs assigned on launch).

### Define the capacity

An installation administrator describes each account and region as a named
capacity. Write `capacity.toml`:

```toml
[[capacity]]
name = "aws-spot"
cloud = "aws"
region = "eu-central-1"
role_arn = "arn:aws:iam::123456789012:role/hakopod-actions"
# external_id = "..."               # optional; defaults to the installation's
subnet_ids = ["subnet-0abc1234"]
security_group_ids = ["sg-0abc1234"]
instance_types = ["c7i.xlarge", "c6i.xlarge", "m7i.xlarge"]
architecture = "amd64"              # or arm64, with Graviton types
spot = true                         # false: on-demand only
disk_gib = 100                      # encrypted gp3 root volume
max_instances = 20                  # hard ceiling across every pool
environments = ["shop/production"]  # project/environment pairs, or "*"
```

```sh
hakopod actions capacity set capacity.toml
hakopod actions capacity test aws-spot            # assume the role, read subnets and security groups
hakopod actions capacity test aws-spot --launch   # also start and terminate one t3.micro or t4g.micro
```

The same is available as `GET`/`PUT /api/v1/settings/actions-capacity` with
`expected_revision`, and `POST /api/v1/settings/actions-capacity/{name}/test`.
Every change is audited. A capacity that a pool still uses cannot be removed;
move the pool off it first. There is no dashboard page yet.

| Setting | Allowed values |
| --- | --- |
| `instance_types` | 1–20 types of the capacity's architecture |
| `subnet_ids` | 1–10 subnets |
| `security_group_ids` | 1–5 groups; `test` warns about inbound rules |
| `disk_gib` | 20–2,000; default 100 |
| `max_instances` | 1–1,000 |

### Use it from a pool

```toml
[services.ci]
replicas = 10

[services.ci.actions]
organization = "acme"
credential = "github-runner-token"
labels = ["hakopod-spot"]
idle_seconds = 120
capacity = "aws-spot"
spot_retries = 2   # default; 0 disables automatic reruns
```

`capacity` is for GitHub pools only. The capacity decides the machine, so
`size`, `resources`, `architecture`, `node_name`, `workspace_size_gib` and
`cache` are refused alongside it. A release is refused if the pool's project and
environment are not listed in the capacity's `environments`.

When every instance of `max_instances` is in use, the pool waits without
registering more runners and its status says which capacity is full. Pool
status (`GET /api/v1/applications/{id}/actions`) shows each slot's instance ID,
type and spot or on-demand lifecycle, and the pool's spot interruptions.

### Spot interruptions

GitHub fails a job whose runner disappears; it does not requeue it. When AWS
reclaims a spot instance (`Server.SpotInstanceTermination`), Hakopod records the
interruption. When the `workflow_job` delivery for the failed job arrives,
Hakopod asks GitHub to rerun the run's failed jobs with the pool's
`jobs_credential` (or `credential`), once per run attempt and at most
`spot_retries` times per run. The jobs credential needs Actions write
permission on the repository; a refusal is shown on the interruption in pool
status.

- Reruns need the **Workflow jobs** webhook described under
  [Scale to zero](#scale-to-zero), even for a fixed pool.
- Each rerun lets the pool's next launches use on-demand capacity, one per lost
  job. A runner that is already idle may still pick the rerun up on spot.
- An instance type interrupted three times within 30 minutes is skipped for an
  hour. If every listed type is skipped, the pool launches on-demand.
- Reruns repeat every failed job of the run. Put jobs that must not run twice,
  such as deploys, in a pool with `spot_retries = 0` or on pods.

### Cleanup

The Actions worker sweeps every capacity each minute and terminates instances
tagged with this installation that belong to no slot or to a removed pool, a
second instance launched for the same slot, and any that have run longer than
`timeout_minutes` plus 15 minutes. A slot being drained keeps its VM until its
job finishes; the pool then terminates it. That includes idle runners: a VM waiting for a job is replaced after
that time. Each VM also stops its runner after the same limit on its own, so it
does not outlive an unreachable control plane. GitHub runners of vanished
instances are removed by the normal slot cleanup.

Not verified yet: launching in a real AWS account, the IAM policy above in
practice, an actual spot interruption followed by a rerun, and boot time from
stock Ubuntu. Jobs pay for the Docker and runner install on every boot.

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
VFS is the fallback Docker storage driver. On a node that has passed the
`shared-overlay2-v1` qualification, fresh GitHub job pods use the same
bounded workspace for overlay2 data. Each job still receives a clean workspace;
the profile does not persist Docker layers or files between jobs. The maximum
lifetime includes startup and waiting for a job. Jobs requiring more disk or a
different runtime must use another runner type.

The GitHub runner-management token stays in control-plane secret storage. A pod
gets only its one-job JIT registration. Configuring this pool does not replace
Hakopod's application build provider.

## Reusing dependencies across jobs

Each job starts with a clean workspace, including with the optional shared
workspace profile. Use GitHub's `actions/cache` or the
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

## Docker Hub pull-through mirror

Every job starts with an empty Docker data directory, so each job pulls its
service and job images again. The managed-actions module also installs a Docker
Hub pull-through mirror, `registry-mirror` in the `hakopod-actions-mirror`
namespace. Runner Docker daemons try the mirror first. After the first pull,
public Docker Hub images such as `postgres` and `redis` come from inside the
cluster.

- The mirror caches public Docker Hub images only. It has no credentials. A
  private Docker Hub image fails on the mirror and Docker pulls it from Docker
  Hub directly with the job's own login, which is never sent to the mirror.
- Images from other registries, such as GHCR, are not mirrored.
- If the mirror is missing or down, Docker pulls from Docker Hub directly.
  Existing installations get the mirror the next time the managed-actions
  module runs.
- Runner pods can reach this one pod on TCP 5000 and nothing else in the
  cluster. A runner on an internal-only network gets no mirror access.
- The mirror is shared by every pool in the installation. A job can tell from
  pull timing whether another job recently pulled the same public image. Image
  contents are verified by digest, so a job cannot change what another job
  receives.
- The cache is an `emptyDir` of at most 20 GiB. A restart empties it, and Docker
  Hub entries expire after seven days.
- GitHub and GitLab runners use the mirror. Bitbucket runners do not yet.

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
this 2-core fixture, the runner container and the Docker sidecar each have a
950m CPU limit, with 100m reserved for the sandbox. A shell compilation and a
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
