# Deploy to Hakopod from GitHub Actions

Add this action as a step to deploy selected services in an existing Hakopod
application. Supply an API URL, a scoped machine token, the application ID and a
JSON array of service names. You can also update images, environment variables
and existing secret bindings for those services.

Release distribution: [hakopod/deploy](https://github.com/hakopod/deploy). The
`@v1` examples follow compatible releases in that major version. For production
workflows, pin a full, reviewed release commit SHA for immutable action code.

## Add a deployment step

Create a machine key with `deployments:read` and `deployments:write`, restricted
to the intended project, environment and application. Store it as the GitHub
Actions secret `HAKOPOD_API_TOKEN`. Set the repository or environment variables
`HAKOPOD_API_URL` and `HAKOPOD_APPLICATION_ID` to your public API origin and
existing application ID.

```yaml
- name: Deploy API and worker
  id: deploy
  uses: hakopod/deploy@v1
  with:
    api-url: ${{ vars.HAKOPOD_API_URL }}
    api-token: ${{ secrets.HAKOPOD_API_TOKEN }}
    application-id: ${{ vars.HAKOPOD_APPLICATION_ID }}
    services: '["api", "worker"]'
```

The step retains each service's saved image unless you supply `image`. It waits
for deployment success by default and fails if the deployment fails. The action
requires a runner that supports Node.js 24 JavaScript actions; GitHub-hosted
runners provide this runtime. The deployment step needs no checkout, Hakopod CLI,
Node setup or package installation.

Serialize deployments for the same application so separate workflow runs do not
race on its revision. Add a job-level concurrency group, using the same group
wherever that application is deployed:

```yaml
concurrency:
  group: hakopod-${{ vars.HAKOPOD_APPLICATION_ID }}
  cancel-in-progress: false
```

GitHub concurrency groups coordinate runs within one repository. The action
also checks the application's revision before accepting a deployment.

## Inputs

| Input | Required | Default | Description |
| --- | --- | --- | --- |
| `api-url` | Yes | — | Public HTTPS origin, with an optional `/api/v1` suffix, such as `https://hakopod.example.com/api/v1`. Loopback HTTP is allowed for local development. |
| `api-token` | Yes | — | Scoped machine bearer token with `deployments:read` and `deployments:write`. |
| `application-id` | Yes | — | ID of an existing application accessible to the token. |
| `services` | Yes | — | JSON array of 1–20 unique existing service names or service objects described below. |
| `image` | No | Saved image | Digest-pinned image for every selected service; an individual service's `image` overrides it. |
| `env` | No | No changes | JSON object of environment variable patches applied to every selected service. Values must be strings or `null`. |
| `workspace` | No | No header | Cloud workspace ID, forwarded in `X-Hakopod-Workspace` when required by your endpoint. |
| `wait` | No | `true` | Wait for successful completion. Set to `false` to return after the API accepts the deployment. |
| `timeout` | No | `600` | Maximum action duration in seconds, from 1 to 3600. |
| `poll-interval` | No | `5` | Seconds between deployment status checks, from 1 to 60. |

Each `services` entry is either a name string or an object containing `name` and
optional `image`, `env` and `secrets` fields. Images must use a digest, such as
`ghcr.io/example/shop@sha256:…`; mutable tags alone are not accepted.

Environment patches change only the selected services. A string sets a value,
including an empty string. `null` removes the service-level entry; an inherited
application default may then apply. A service's `env` entries override the
shared `env` patch for matching names. Other service settings and unselected
services are preserved.

Hakopod still reconciles the resulting application revision, including its
deployment jobs. Selecting services limits configuration updates; it does not
prevent existing deployment jobs from running again. A retained mutable image
tag on a selected service can resolve to a newer image; use an explicit digest
when you need an exact artifact.

The `secrets` field maps environment variable names to existing Hakopod secret
reference names. `null` removes a service's binding. Create or rotate secret
values in Hakopod before running this action; it does not upload secret bodies.
When switching a name between a plain environment value and a secret binding,
explicitly remove the old entry in the same service patch.

## Update environment variables and secret bindings

This example updates both services' `RELEASE_CHANNEL`, sets an API-specific
value and removes a worker override. It replaces `DATABASE_URL` on the API with
an existing secret binding, and changes the worker's `CACHE_URL` from a secret
binding to a plain value.

```yaml
- name: Deploy selected service configuration
  uses: hakopod/deploy@v1
  with:
    api-url: ${{ vars.HAKOPOD_API_URL }}
    api-token: ${{ secrets.HAKOPOD_API_TOKEN }}
    application-id: ${{ vars.HAKOPOD_APPLICATION_ID }}
    env: '{"RELEASE_CHANNEL":"stable"}'
    services: |
      [
        {
          "name": "api",
          "env": {"LOG_LEVEL":"info", "DATABASE_URL":null},
          "secrets": {"DATABASE_URL":"production-database-url"}
        },
        {
          "name": "worker",
          "env": {"LOG_LEVEL":null, "CACHE_URL":"redis://cache:6379"},
          "secrets": {"CACHE_URL":null}
        }
      ]
```

For dynamic values, use GitHub's `toJSON` function so quotes and line breaks are
encoded correctly. Keep credentials in Hakopod secret bindings:

```yaml
env: '{"RELEASE_COMMIT":${{ toJSON(github.sha) }}}'
```

To deploy different images, put an `image` field in each service object. For
example, `api` can use the API image digest while `worker` uses the worker image
digest. Omitting that field uses the shared `image` input, or the service's saved
image when there is no shared input.

## Deploy a built image by digest

Build and push images before this step. Hakopod must already have permission to
pull a private image; a GitHub registry login authorizes the build runner only.
After a `docker/build-push-action` step named `build`, use its digest output:

```yaml
- name: Deploy the published image
  id: deploy
  uses: hakopod/deploy@v1
  with:
    api-url: ${{ vars.HAKOPOD_API_URL }}
    api-token: ${{ secrets.HAKOPOD_API_TOKEN }}
    application-id: ${{ vars.HAKOPOD_APPLICATION_ID }}
    services: '["api", "worker"]'
    image: ghcr.io/example/shop@${{ steps.build.outputs.digest }}
    env: '{"RELEASE_COMMIT":${{ toJSON(github.sha) }}}'
```

Use the same registry and image repository that your build step pushed. Its
`digest` output includes the `sha256:` prefix.

For separate builds named `build_api` and `build_worker`, replace the `services`
input with per-service image references:

```yaml
services: |
  [
    {"name":"api", "image":"ghcr.io/example/shop-api@${{ steps.build_api.outputs.digest }}"},
    {"name":"worker", "image":"ghcr.io/example/shop-worker@${{ steps.build_worker.outputs.digest }}"}
  ]
```

## Outputs

| Output | Description |
| --- | --- |
| `deployment-id` | Accepted deployment ID. |
| `application-id` | Application ID returned by the API. |
| `revision` | Accepted application revision. |
| `status` | Last observed deployment status; with `wait: 'false'`, this may still be queued or running. |
| `idempotency-key` | Unique request key generated for this action invocation. |

For example, a later step can read `${{ steps.deploy.outputs.deployment-id }}`.
A successful step with `wait: 'false'` confirms acceptance only; inspect the
deployment in Hakopod or poll its API to confirm rollout success.

## Planning, retries and failures

The action fetches the current application, applies the requested service
patches, requests a plan and submits the selected-service deployment with the
reviewed revision. It stops on a failed fetch, unknown service, missing secret
reference, failed plan or revision conflict. A concurrent edit therefore
requires a new run against the current specification.

Each invocation generates an idempotency key. If the deployment request has an
ambiguous connection failure, the action tries to recover the accepted result
through `GET /api/v1/idempotency/{key}`. It does not blindly repeat the deployment
POST. If recovery cannot confirm acceptance, inspect Hakopod before rerunning.

Requests and polling are bounded. A timeout or canceled GitHub workflow stops
the action's wait; it does not cancel a deployment already accepted by Hakopod.
Check that deployment before starting a replacement run.

The action uses the same `/api/v1` API as the dashboard and CLI. See
[CI API access](../../docs/ci-api.md) for endpoint and token setup.
