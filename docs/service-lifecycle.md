# Stop, resume and delete services

Application containers, deployment jobs and scheduled jobs use Kubernetes
`imagePullPolicy: Always`. Each new deployment resolves an image tag such as
`latest` against its registry and pins the resulting digest for that release.
Container starts check the registry; unchanged layers may be reused locally.
Restart, resume, scaling and rollback preserve the saved release digest. To run
a newer image behind a tag, submit a new deployment. A running container does
not update merely because a registry tag changes.

The service-card menu exposes Stop/Resume for long-running services and Delete for all services. The shared dashboard uses the same controls in Cloud and self-hosted installations.

Stop and Resume create a revision-checked, idempotent deployment. Stopping records `suspended = true` while retaining the configured replicas, autoscaling settings, image, variables, domains and volumes. Kubernetes replicas become zero and the HPA is removed. Resume restores the configured replicas and HPA. A stopped service is reported as stopped, not failed or healthy traffic. Stopping may make dependent services unavailable. It does not stop charges from the user's server provider.

The API routes are `POST /api/v1/applications/{id}/services/{service}/stop` and `/resume`, with `expected_revision` and an `Idempotency-Key` header. Both require scoped `deployments:write`. Active rollouts must finish or be cancelled first; Stop accepts a failed completed deployment when its immutable resolved images are available. Deployment jobs do not offer Stop/Resume.

Delete opens a confirmation dialog, validates and displays the full removal diff, then submits the reviewed deployment. Domain mappings for the removed service are removed. Remaining dependency references must be resolved before validation succeeds. Persistent volumes and backups are retained. Revision conflicts require closing and reopening the dialog against current configuration.

Verification: full Go tests with isolated PostgreSQL and dashboard typecheck/build/tests pass. Named k3d-hakopod-dev acceptance passes for real network isolation, HPA stop/removal and resume/restoration. Independent visual review passed desktop/mobile and both themes, including stale revision and keyboard behavior; see delete-service-ui-review.md. Production rollout is recorded separately.
