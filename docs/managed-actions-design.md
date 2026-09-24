# Managed Actions implementation plan

Status: not available in the product. The repository-scoped GitHub client has unit
coverage; the runner controller, entitlement gates, dashboard and live workflow
acceptance are still required before enabling a catalog entry.

Managed Actions will offer replicated, single-job GitHub runners as a Pro
capability. Each replica is one concurrent job slot. A completed job loses its
workspace and registration before its slot can accept another job. Scaling down
and ordinary upgrades drain active jobs; explicit deletion must clearly state
that it cancels running work.

Provider credentials belong in scoped secret storage. They are used only by the
control plane to obtain one-job JIT configuration. They must not appear in the
runner's environment, image, TOML or logs. Registration identity must be persisted
before calling GitHub, so a lost response can be reconciled by name instead of
creating extra runners. Long-lived organization-wide tokens are unnecessary for
the repository-scoped first implementation.

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
