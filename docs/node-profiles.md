# Node profiles

A node profile lets an installation administrator grant one service a relaxed pod
security profile on one dedicated node. It exists for services that manage their
own sandbox, such as a Kelvo node: it installs its own Landlock and seccomp policy
and needs a cgroup v2 subtree it can write to.

Status: implemented and covered by unit tests (grant matching, pod template
changes, strict file reading, TOML validation). It has not been exercised on a
real cluster, so the end-to-end behaviour is unverified.

Read this first: a privileged container is effective root on its node. Hakopod
cannot police what the service does with it. Use a dedicated node that runs
nothing else you care about.

## Approve a profile

Create an administrator file, for example `/etc/hakopod/node-profiles.toml`, owned
by root or the server user and not writable by group or others:

```toml
schema_version = 1

[[bindings]]
name = "kelvo-node"
project = "platform"
environment = "production"
application = "kelvo"
service = "node"
node = "kelvo-1"
seccomp = "unconfined"
privileged = true
```

| Field | Meaning |
| --- | --- |
| `name` | Alias the service references; unique, lowercase, at most 63 characters |
| `project`, `environment`, `application`, `service` | Exact names; no wildcards |
| `node` | Kubernetes node name the service must be pinned to |
| `seccomp` | `runtime-default` or `unconfined` |
| `privileged` | Lets the container write its own cgroup subtree |

Point the server at it with `[node_profiles] file = "..."` in the operator TOML, or
set `HAKOPOD_NODE_PROFILES_FILE`. The file is read at startup; restart after a
change. Managed Cloud installations and shared workload runtimes refuse it.

## Use it

```toml
[services.node]
image = "ghcr.io/example/kelvo@sha256:..."
node_profile = "kelvo-node"
node_name = "kelvo-1"
update_strategy = "recreate"
replicas = 1
```

Hakopod checks the grant at plan time, at acceptance and on every deployment. It
refuses the service unless `node_name` equals the binding's `node` and there is a
single replica without autoscaling. Previews and service transfers never inherit a
profile. Managed Actions and serverless services cannot use one.

Put the profiled service in an application of its own. The application's namespace
is relaxed from the restricted pod security level to privileged, which affects
every service in it. Only the profiled service container gets the profile; helper
containers keep the restricted settings.

## Singleton and replacement

A profiled deployment always uses the Recreate strategy: Kubernetes stops the old
pod before it starts the new one, so a restart never runs two copies on the node.
Local volumes already stay on their node, so keep Kelvo's journal on a named volume
or a local path on the pinned node.

Kubernetes cannot promise at-most-one pod when a node is partitioned. For Kelvo
that is safe because the node claims its worker identity in its own coordination
store with a lease, and a second owner is refused. Do not rely on Hakopod alone for
fencing a service that has no such claim.

## Kernel prerequisites

Hakopod does not probe the kernel. Check the node before you approve a profile:

```sh
cat /sys/kernel/security/lsm            # must include landlock
stat -fc %T /sys/fs/cgroup              # cgroup2fs
cat /sys/fs/cgroup/cgroup.controllers   # cpu memory pids
```

Landlock ABI 3 or newer needs Linux 6.2 or newer. Ubuntu 24.04 and Debian 13 ship
kernels new enough, but a new enough kernel is not enough: check that `landlock`
appears in the first command's output, and do the same on any other distribution.

## Fixed egress address

Hakopod does not route traffic. To keep database connections on a fixed address,
join the dedicated node to your tailnet and send its internet traffic through the
exit node that owns the allow-listed address. Run nothing else on that node: an exit
node replaces its default route. Add a firewall rule so the service user reaches the
internet only through `tailscale0`, which makes an exit-node outage break database
connections instead of leaking from the node's own address. Pinning the service to
that node with `node_name` is what ties the service to the route.
