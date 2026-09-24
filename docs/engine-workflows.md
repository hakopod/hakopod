# Build and release workflows

Hakopod shares these features between the engine, dashboard and CLI. Cloud routes
workload operations through the selected workspace's scoped node credential. Free
Cloud workloads, including previews and scheduled jobs, run on your own node;
they do not receive included compute.

## Automatic framework setup

Open **Builds > Create build**, select the repository and continue to **Build recipe**.
Hakopod reads metadata at one resolved commit and fills the first untouched recipe.
The framework cards show the selected and detected framework. Filter by category,
choose another supported recipe, or open **Build settings** to edit its runtime,
commands, output directory and server port. Switching frameworks preserves each
recipe's draft while the framework picker stays open; manual recipes are starting
points and need review against the repository's scripts and adapter.

**Detect again** refreshes the observation without replacing edited settings.
**Use detected settings** explicitly restores the repository suggestion. Changing
the source invalidates old detection notes. If detection fails, retry or configure
a recipe, Dockerfile or Cloud Native Buildpacks manually. The summary identifies
the real build location: GitHub Actions or GitLab CI. The final review includes
the effective commands; saving prepares a workflow for review, and installation
remains an explicit repository write by an authorized repository manager.

This picker is part of the Free, shared open-source dashboard. Cloud uses the same
component and detection API. No Cloud subscription is required for framework setup.

| Framework | Suggested runtime |
| --- | --- |
| Astro | Static, or Node when `@astrojs/node` is installed |
| Next.js | Node; static for an explicit `output: 'export'` |
| SvelteKit | `adapter-node` or `adapter-static` |
| TanStack Start | Node; confirm the chosen adapter's output path |
| Vite | Static `dist` output |
| Node | Reviewed package build/start scripts |
| Plain HTML | Static files |

An existing Dockerfile takes precedence. Detection reads configuration as text;
it does not execute configuration modules and may need correction for conditional
configuration, custom adapters or monorepos. Choose the package directory as the
build context. Commit a lockfile for reproducible dependency installation.

Static output runs in a digest-pinned nginx image as a non-root user on port 8080.
Node output runs in a digest-pinned Node 24 image as a non-root user. Generated
recipes honor the selected port. Static hosting cannot execute SSR or API routes;
use a Node runtime for those applications.

GitHub Actions or GitLab CI builds and publishes the image. The production API
host never builds repository code. Provider CI minutes, runner support and registry
permissions remain necessary. Private images also need a runtime pull credential.

An installation administrator may detect a new repository. Scoped deployers may
only detect the exact repository and connection already approved in that
application's source binding. Switching repositories needs new approval. Cloud
workspace membership never grants access to installation-wide Git credentials.

The API is `POST /api/v1/builds/detect` with the existing build input: project,
environment, application name, repository, provider, branch and context path.
For delegated detection include `application_id`, service, and the approved
`connection_id`. The response contains the resolved commit, warnings and a
reviewable `framework` plan or the existing Dockerfile path.

## Build secrets

Build settings accept references to provider-side secrets, not secret values:

```json
{
  "build_secrets": {
    "npm_token": "NPM_TOKEN"
  }
}
```

Configure `NPM_TOKEN` in GitHub Actions secrets or as a masked GitLab CI variable.
GitLab protected variables are available only to protected refs. Missing required
values fail before building. The maximum is 16 references.

For framework recipes, the secret is mounted at `/run/secrets/npm_token` and
available as `NPM_TOKEN` only during dependency installation and the build step.
For a custom Dockerfile, explicitly opt into the BuildKit mount:

```dockerfile
RUN --mount=type=secret,id=npm_token,required=true \
    NPM_TOKEN="$(cat /run/secrets/npm_token)" npm ci
```

Build arguments remain public values. Secrets are unavailable in buildpack mode.
A build script can intentionally print a secret or embed it in generated files;
BuildKit does not protect against the code to which you grant the secret. Inspect
public output and use trusted branches. The temporary mount and environment are
not part of the runtime image. Runtime secrets are configured separately.

## CI through the public dashboard

See [CI API access](ci-api.md) for machine bearer authentication, public REST
routes and fail-fast specification retrieval before selected-service deployments.

## Coding agents through MCP

`hakopod mcp` runs a bounded MCP server over stdio. It requires an explicit project
and environment and uses the CLI's saved credential or `HAKOPOD_API_KEY`.
Use a short-lived project/environment-scoped key; keep credentials out of the
repository and the MCP configuration itself.

A client using the `mcpServers` configuration format can use:

```json
{
  "mcpServers": {
    "hakopod": {
      "command": "hakopod",
      "args": ["mcp", "--project", "demo", "--environment", "development"]
    }
  }
}
```

Available tools inspect applications, services, runtime pods and metrics, custom
domains, deployments and logs. Build provenance maps accepted image digests to
recorded Git commit SHAs. Tools also suggest a framework recipe from supplied
metadata and plan TOML changes.

Self-hosted installations also expose a [Streamable HTTP MCP endpoint](http-mcp.md)
at `/api/v1/mcp?project=demo&environment=development`, using a scoped bearer key. There is no host terminal,
shell execution or implicit local-file access. Returned application content and
logs are explicitly untrusted data.

Deployment is absent by default. Add `--allow-deploy` only when the agent should
be able to deploy after review. `plan` returns a canonical plan and a ten-minute
`plan_id`; `deploy` accepts that ID, retains its reviewed revision and reuses the
same idempotency key on retry. It cannot silently replace the reviewed TOML.

Use the HTTPS API of a self-hosted installation with its scoped machine credential.
Cloud's enrolled-worker control API is private and is not a public MCP endpoint.
`--workspace ID` (or `HAKOPOD_WORKSPACE`) forwards a Cloud workspace header for
integrations already holding a Cloud bearer session; it does not grant access or
turn a node key into a Cloud account token. Cloud account device-login and scoped
Cloud automation credentials are not exposed by this release. Do not use a broad
operator session as a replacement for a scoped automation key.

## Scheduled jobs

Add a schedule to a job service in application TOML:

```toml
schema_version = 1
name = "reports"

[services.report]
image = "ghcr.io/example/reports:1.0"
size = "small"
command = ["node", "report.js"]

[services.report.job]
timeout_seconds = 300
retries = 1

[services.report.job.schedule]
cron = "0 * * * *"
timezone = "UTC"
history_limit = 1
```

The release resolves the image to a digest. Cron uses exactly five fields and an
IANA timezone. Kubernetes prevents overlapping runs, bounds retries and execution
time, and retains the last one or two successful and failed runs. Long-running
jobs skip overlapping starts. It does not replay every missed tick after downtime.

The service page shows the actual retained runs, completion times and observation
freshness. `Scheduled` means the schedule is configured; it does not assert that
the job completed. A failed most recent run of the current revision remains
visible as failed. History from older revisions remains labeled by revision.

**Pause schedule** sets `suspended = true`. It prevents new runs and allows an
active run to finish. Resume restores scheduling. New schedules remain paused
until their release has reconciled. Ordinary deployment jobs still run once
before their dependants; other services cannot depend on a scheduled job.

## Preview environments

An application has a **Previews** tab. Create a preview from an already-built
image, review its explicit TOML and acknowledge data expiry. Production variables,
secret values, volumes and data are not copied. The default starter includes only
one service's image and basic port settings; add the rest explicitly.

Previews use unique application namespaces and generated HTTP hostnames. They
support up to four small services, one replica per service and 5 GiB of fresh
storage. Public TCP, custom domains, shared virtual networks, external secret
providers, certificate mounts and AWS identities are unavailable. Scheduled jobs
must remain paused. Native secret references resolve only inside the new preview's
application scope; a missing value must be configured there before deployment
can become healthy. Backup/restore jobs are unavailable for temporary previews.

At most three previews may be active or deleting in one project. The limit and
resource budget apply to subsequent revisions as well as creation. Lifetime is
1–72 hours. Cleanup starts at expiry, cancels pending deployments and deletes the
owned namespace, volumes and native secrets. Transient cleanup failures remain
visible and retry; a stuck namespace does not delay ordinary deployments.
Cleanup switches only the preview's verified backing volumes to Delete, including
on storage classes that normally retain data, then waits for the provisioner to
reclaim them. An unbound retained claim or changed ownership requires resolution
before cleanup can finish; the preview remains visibly deleting in that case.

```sh
hakopod preview-create APPLICATION_ID --name pr-123 \
  --branch feature/example --file preview.toml --ttl 24h \
  --acknowledge-data-expiry
hakopod previews APPLICATION_ID
hakopod preview-delete PREVIEW_ID --name pr-123
```

The CLI uses the selected project/environment. A scoped CI job may call the same
API with an idempotency key and the parent application's current revision:

```json
{
  "name": "pr-123",
  "branch": "feature/example",
  "ttl_hours": 24,
  "expected_parent_revision": 7,
  "discard_on_expiry": true,
  "toml": "schema_version = 1\nname = 'preview'\n[services.web]\nimage = 'ghcr.io/example/app:tested-commit'\nport = 8080\npublic = true\n"
}
```

POST this to `/api/v1/applications/APPLICATION_ID/previews`; use the returned
preview ID for inspection and deletion. DELETE `/api/v1/previews/PREVIEW_ID`
requires `{"confirmation":"pr-123"}`. A Git branch is a label, not an instruction
to build or check out that branch. Automatic PR/MR webhook lifecycle is not
included; CI can create/delete previews after a trusted build, and expiry starts
cleanup if the close action is missed. Never pass deployment credentials or build
secrets to untrusted fork code.

Self-hosted project administrators and scoped project/environment machine keys
can manage previews. Cloud preview mutations are workspace-owner-only. Editors
and viewers can inspect them. Preview workloads consume the same BYO resources
and node quotas as ordinary applications.

## Automatic release recovery

Safe recovery preserves the existing default for compatible stateless applications:

```toml
[recovery]
on_failure = "safe"
```

Use `on_failure = "disabled"` to leave a failed rollout for manual intervention.
Safe recovery requires a previous successful release with the same service names.
Jobs, schedules, persistent volumes and service additions/removals stop automatic
recovery and show an explanation requiring operator review.

Recovery selection and progress are durable in PostgreSQL. A restarted worker
resumes the selected recovery rather than retrying the failed release. The
attempted deployment stays **Failed**. A separate recovery result records whether
the prior revision was restored, skipped or also failed. A successful restoration
makes the application **Recovered** and runtime inspection uses the restored spec.

Recovery restores workload configuration. It does not restore database contents,
reverse migrations, or restore previous secret values. Inspect the failed
release and correct it before deploying another revision.
