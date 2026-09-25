# Managed Actions implementation plan

Status: the runner controller, entitlement gates, catalog form, durable cleanup
and optional installer module are implemented on the feature branch. See
[Managed Actions](managed-actions.md) for the operator workflow. Release remains
gated on real workflow acceptance and final visual review; implementation is not
evidence of a deployed release.

Managed Actions will offer replicated, single-job GitHub runners as a Pro
capability. Each replica is one concurrent job slot. A completed job loses its
workspace and registration before its slot can accept another job. Scaling down
and ordinary upgrades drain active jobs; explicit deletion must clearly state
that it cancels running work.

Provider credentials belong in scoped secret storage. They are used only by the
control plane to obtain one-job JIT configuration. They must not appear in the
runner's environment, image, TOML or logs. Registration identity must be persisted
before calling GitHub, so a lost response can be reconciled by name instead of
creating extra runners. Pools select exactly one organization or repository.
Organization pools use a credential with organization Self-hosted runners
permission and either an explicit runner group or GitHub's default group. GitHub
owns the group's repository policy. Each durable slot keeps its original scope
and credential reference so edits cannot redirect observation or cleanup.

The official runner image was verified at version 2.337.0 on September 24, 2026:
`ghcr.io/actions/actions-runner:2.337.0@sha256:e5496277be5d09bc968b3d64911b74e219ac4a3f2edce956a3ecf9271bea1ef4`.
Its index includes Linux AMD64 and ARM64. It runs as user 1001. The bundled Docker
CLI is not a Docker daemon and does not make Docker-based actions work by itself.

The user requested shell/JavaScript actions, Docker actions, job containers, service containers and Docker builds. The initial runtime investigation uses a separate Docker daemon inside a gVisor sandbox for each job:

- Shell and JavaScript actions can run as non-root single-job containers, with
  bounded CPU, memory, temporary workspace and duration.
- `container:`, `services:` and Docker builds use the in-sandbox daemon. The runtime must pass networking, bind-mount and build acceptance before the product is enabled. Mounting the host Docker socket or handing a runner Kubernetes credentials is outside the engine's application security contract.
- Both Compose examples in the supplied reference (youssefbrr/self-hosted-runner at 2c9581c610279a35bb0dcd56b4017dafe8a98109) mount the host Docker socket. They are not suitable as shared-host deployment presets. Its ARM image is Linux ARM64, not a macOS runner.
- The public gVisor Docker tutorial documents in-sandbox capabilities, required runtime flags and Docker networking limitations. This implementation must fail closed if its dedicated runtime is unavailable; it must never fall back to runc. Linux AMD64 and ARM64 are the initial targets. Arbitrary host devices, host mounts, kernel modules and nested virtual machines are outside that runtime.

The scripts under `scripts/actions/` are disposable CI acceptance fixtures only. They do not install or enable Managed Actions in customer installations.

All modes need current Pro or Cloud Team authority before creating another
runner, including background replenishment. Losing access stops new assignments
without silently killing a running job. Cloud entitlement must come from trusted
control-plane configuration, never a customer-supplied TOML flag. BYOD engines
need the same revocation and restart behaviour.

Before release, acceptance must cover a real workflow, concurrent slots, failed
registration responses, API outages, process restart, scale-down, deletion,
license expiry, secret rotation and cross-workspace isolation. A passing mock
GitHub response is not evidence that a real Actions runner has registered or run
a workflow.

References:

- https://docs.github.com/en/rest/actions/self-hosted-runners#create-configuration-for-a-just-in-time-runner-for-a-repository
- https://docs.github.com/en/actions/hosting-your-own-runners/managing-self-hosted-runners/autoscaling-with-self-hosted-runners
- https://github.com/actions/runner/blob/main/images/Dockerfile

- https://gvisor.dev/docs/tutorials/docker-in-gvisor/
- https://gvisor.dev/docs/tutorials/docker-in-gke-sandbox/
- https://github.com/youssefbrr/self-hosted-runner

## Verified Docker runtime

The named k3d development-cluster acceptance passed on both Linux AMD64 and ARM64
in GitHub run 36018667516 at commit 122e4a5. This proves nested container execution,
a shared workspace bind mount, user-defined network aliases, published service
ports and a BuildKit Dockerfile RUN step. It does not yet prove GitHub registration,
a real workflow, tenant isolation or the product lifecycle.

The tested profile uses gVisor release-20260907.0 with systrap, net-raw,
allow-packet-socket-write, and a memory-backed root overlay. Docker 29.8.1 uses its
legacy iptables binaries, VFS storage with the containerd image store disabled,
and explicit in-sandbox TCP/UDP SNAT. The Docker data mount is memory-backed and
bounded; no host Docker socket, host path or Kubernetes credential is exposed.
Using the daemon image's default nftables tools broke service DNS; selecting its
provided legacy tools fixed that failure on both architectures.

## Implementation boundary

Represent a pool as a service in the existing versioned application spec so its
replicas, resource budget, placement, revision review and deletion remain part of
the normal deployment model. An Actions service must not fall through to an
ordinary Deployment. Its provider credential is a local scoped secret reference
read only by the control plane; it must participate in missing-secret setup but
must never become a runner environment variable or mounted file.

Durable pool and slot records need to preserve registration intent and its
unique runner name before GitHub calls. A lost JIT response cannot be recreated:
find and remove that registration before issuing another one. Store the returned
one-job config only in an owned, short-lived Kubernetes Secret. Keep cleanup
records until both pod and registration are gone. Application deletion must not
cascade away the only registration-cleanup record.

Use the shared management lifecycle for the controller. Check Pro entitlement
for self-hosted installations, and trusted application/workspace entitlement for
Cloud and BYOD, including background replenishment. No request-supplied license
flag is authority. Resource accounting must continue to include draining slots;
never release capacity merely because the desired replica count was reduced.

Before enabling the preset, implement the controller, deployment admission,
observation, deletion, secret handling and UI together. Run a real GitHub workflow
covering JavaScript actions, Docker actions, a container job, service containers,
and docker build. Then verify concurrent slots, restarts, ambiguous registration,
API outages, scaling, deletion, credential rotation, license expiry and isolation.
