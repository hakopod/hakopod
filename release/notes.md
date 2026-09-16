Hakopod 0.1.0-alpha.9 adds service management within existing applications and improves workload visibility, backups and rollout diagnostics.

## Add and move services

- Choose **Add service** from an application to use a catalog template, Docker Compose, a container image or an existing service's image. Configure the service and review the deployment before applying it.
- Add catalog templates without replacing the application's existing services. Single-service templates accept a unique service name. Existing settings, deployed image digests and shared secrets are preserved; conflicting names and resources are rejected.
- Move a stateless service to another application in the same project and environment through a reviewed handover. The original keeps running until the destination deployment succeeds and you confirm removal. Moves preserve the running image and effective environment, and copy local secret references without disclosing values or overwriting destination secrets.
- Persistent data, jobs, dependencies, custom domains and other application-bound resources require an explicit migration. Source builds and deployment history stay with the original application. See [service management documentation](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.9/docs/application-service-management.md).

## Operations and reliability

- Workload CPU and memory metrics can use bounded kubelet statistics when the Kubernetes metrics API is unavailable. Live pod logs and deployment event streams work through the dashboard proxy.
- Deployment containers use `imagePullPolicy: Always` while retaining immutable image digests. Restarting a service keeps its deployed digest; a new deployment resolves a mutable image tag again.
- Recent out-of-memory kills remain visible in rollout diagnostics after a container restarts, with guidance to increase the service size or reduce application worker memory.
- The embedded management runtime initializes backup support. Backup and restore entry points are easier to find, and optional external data workflows are linked from the relevant pages.
- Certificate-renewal acceptance now tolerates unrelated metadata updates while checking the certificate behavior.

## Prebuilt artifacts

Includes Linux amd64/arm64 server and CLI archives, the dashboard bundle, macOS amd64/arm64 CLI archives, and the installer kit. Target machines do not compile sources.

The multi-platform readiness helper is `ghcr.io/hakopod/hakopod-probe:v0.1.0-alpha.9`. Use the immutable reference in the attached `probe-image.txt` for Infrastructure > Setup.

Download this release's `installer.sh`, inspect `sh installer.sh --help`, and select `--version 0.1.0-alpha.9`. Check the attached `upgrade.json` for verified upgrade paths. Website bootstrap publication is separate.

Release publication requires Go/dashboard checks, native packaged smoke tests, Ubuntu 24.04 systemd/K3s host acceptance and both probe-image architectures. Assets include checksums, SBOMs, dependency notices, provenance and acceptance reports. Verify provenance with `gh attestation verify FILE --repo hakopod/hakopod`.

This remains an alpha release. The service-management UI was reviewed in both themes on desktop and mobile; development-cluster acceptance verified staged service moves and additive catalog deployment using fixed fixture images. These checks do not certify every catalog image, arbitrary private repositories, every Linux distribution or live customer data migrations. Private Cloud pages are not included in public archives.
