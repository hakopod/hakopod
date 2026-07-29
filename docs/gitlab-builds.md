# GitLab CI source builds

Hakopod builds Dockerfiles or Cloud Native Buildpacks on GitLab.com runners and
deploys the resulting immutable image through the ordinary deployment plan. No
builder, Docker daemon, or additional queue runs on the management host. GitLab
source synchronization, source builds, and GitLab account sign-in are separate
integrations; enabling OAuth sign-in does not grant repository access.

## Prerequisites and setup

1. Enable CI/CD and Container Registry on a GitLab.com project. Nested paths such
   as `group/subgroup/project` are supported. The project must use its default
   `.gitlab-ci.yml` CI configuration path.
2. Configure the GitLab integration in Hakopod administration with an API token
   authorized to read the repository, commit the CI file, start/cancel pipelines,
   and read jobs/artifacts. The token's user must be permitted to create pipelines
   with variables under the project's CI/CD settings. Protect this credential as
   a repository-write credential; it stays in the platform Kubernetes Secret and
   is never embedded in generated CI or given to application pods.
3. Create a build and select GitLab, the repository, source branch, context,
   Dockerfile or buildpack preset, and the target architecture. Select a persistent
   `read_registry` pull credential scoped to the deployment environment when the
   output image is private. The runner's short-lived `CI_JOB_TOKEN` cannot serve as
   a persistent runtime pull credential.
4. Review the generated CI file. A global administrator explicitly installs it on
   the repository's default branch. Branch protection must permit that commit.
   Previewing or saving settings does not modify the repository or start a build.
5. Start a build, inspect its GitLab pipeline, then review and apply Hakopod's
   canonical deployment diff after the verified image is available.

There is **one Hakopod-managed build per GitLab repository** in this release.
Hakopod refuses to overwrite existing unowned `.gitlab-ci.yml` contents or a file
owned by another build. Combining an existing CI graph with Hakopod's job requires
a future composition workflow. Project settings that select a custom CI path are
also refused. A repeated install with already-matching, verified contents performs
no additional repository write; this recovers interrupted installation responses.

## Automatic builds and deployments

Enable automatic builds and install the resulting reviewed CI. A push to the
configured source branch triggers its native GitLab pipeline. That branch must
contain the exact current generated CI file, including when it differs from the
default branch. The installer only modifies the default branch.

Register an HTTPS GitLab webhook at `/api/v1/webhooks/gitlab` using the integration's
webhook secret. Enable **Pipeline events** so completed push pipelines appear in
Hakopod and can auto-deploy. **Push events** are separately used by TOML source
bindings. Neither a loopback dashboard nor an unexposed development API receives
internet webhooks.

Webhook notifications enter the shared durable build inbox, capped at 1,000
pending items across providers. One bounded worker uses the existing PostgreSQL
connection pool. Duplicate notifications reuse the same run. The worker verifies
the pipeline, committed CI file, successful job, source commit, and image result
before deployment. Newer branch commits suppress older automatic deployments.
Changed build settings, expired/revoked grants, or removed permissions block new
deployment work. Existing application workloads continue running.

## Runner and artifact boundaries

The generated job selects `saas-linux-small-amd64` or `saas-linux-small-arm64`
according to the target cluster architecture. GitLab documents both as 2-vCPU,
8-GB hosted runners with privileged Docker-in-Docker support. Those resources are
used on the provider only while building. Runner availability, compute allowances,
registry storage and usage charges belong to the operator's GitLab account.

Docker CLI and Docker-in-Docker images are pinned by manifest digest. The DinD
bridge disables TLS only inside the isolated CI job and uses `--mtu=1400` for the
documented hosted ARM networking constraint. The job runs natively, has a 30-minute
timeout and no automatic retries, and serializes builds for its build ID. Running
the generated job on a self-managed runner requires matching tags, architecture,
and privileged DinD support; this is not configured or verified by Hakopod.

Buildpacks use pinned Paketo images and a checksum-verified pack CLI. A manual
pipeline executes the reviewed CI from the default branch, fetches and checks out
the exact accepted source SHA, and records the source separately from the CI
commit. Images are published under
`registry.gitlab.com/<repository>/hakopod-<build-id-prefix>` and accepted only as
`@sha256:` digests. The 32-KiB result limit is independent of image size; image
layers never pass through the management process. Artifacts expire after seven
days at GitLab, so observe or deploy completed builds before expiry.

If pipeline creation is interrupted, Hakopod records `dispatch_unknown`. Retrying
the same idempotency key returns the durable request without starting another
pipeline. Refresh searches a bounded recent pipeline window for the exact request
variables. If no match is found, inspect GitLab before starting another request.
Cancellation targets a verified remote pipeline and does not delete deployed
workloads.

GitLab project administrators and repository maintainers remain trusted. They can
change project settings and execute code in their CI environment. CI verification
protects the deployment binding from stale or mismatched output; it is not a
hostile-repository sandbox or a reproducible-build attestation.

## Verification and sources

The automated fixture uses real temporary PostgreSQL databases and a local HTTP
GitLab implementation. It covers ownership refusal, custom-entrypoint refusal,
idempotent installation, exact-source dispatch, duplicate notifications, artifact
size and commit checks, canonical deployment, stale-source suppression,
interrupted-dispatch recovery, cancellation and permission revocation. Generated
YAML and POSIX shell parse for both architectures and both build modes.

No hosted repository writes, GitLab pipeline executions, account OAuth exchange,
or registry pushes were performed during these checks. A real GitLab project,
authorized integration token, runner allowance, webhook reachability and runtime
registry credential are the remaining provider acceptance prerequisites.

References checked on 2026-09-12:

- [GitLab hosted Linux runners and ARM MTU guidance](https://docs.gitlab.com/ci/runners/hosted_runners/linux/)
- [Repository files API and last-commit concurrency](https://docs.gitlab.com/api/repository_files/)
- [Pipeline creation, variables and cancellation](https://docs.gitlab.com/api/pipelines/)
- [Job artifacts API](https://docs.gitlab.com/api/job_artifacts/)
- [GitLab Container Registry authentication](https://docs.gitlab.com/user/packages/container_registry/authenticate_with_container_registry/)
- [pack v0.40.9 release](https://github.com/buildpacks/pack/releases/tag/v0.40.9)
