# CI access through the self-hosted dashboard

Use the public HTTPS dashboard origin followed by `/api/v1`, for example
`https://hakopod.example.com:8443/api/v1`. CI supplies its own machine bearer key.
Browser cookies are not used. The Go API still validates key expiry, project,
environment, application restrictions and permissions on every request.

The dashboard forwards these machine API routes:

| Method | Route | Purpose |
| --- | --- | --- |
| GET | /applications | List applications with project/environment query parameters |
| GET | /applications/{id} | Fetch the current specification and revision |
| GET | /applications/{id}/provenance | Read accepted image-to-commit mappings |
| POST | /plan | Review an application or selected-service deployment |
| POST | /deployments | Accept the reviewed deployment |
| GET | /deployments/{id} | Poll deployment status |
| GET | /idempotency/{key} | Recover a deployment after an ambiguous connection failure |
| GET | /openapi.json | Inspect the API contract |

This fix is not included in alpha.15. Install a later release containing the fix
before using these routes through the dashboard. HTTP MCP has a separate route.

Use a short-lived scoped key with `deployments:read` and
`deployments:write` for deployment automation. Store it in CI's secret store.
The proxy preserves the request body, query scope and `Idempotency-Key`, and
returns canonical authorization, revision-conflict and rate-limit errors.
Only the listed methods and routes are forwarded; this is not an unrestricted
proxy for installation administration.

## Stop on a failed specification fetch

A 404 followed by “services not in the app spec” does not establish that the
services are missing. A script that continues after curl fails may be validating
an empty document or an error response.

Keep retrieval and validation as separate, fail-fast steps. For Bash:

```bash
set -euo pipefail
: "${HAKOPOD_ORIGIN:?Set the public HTTPS dashboard origin}"
: "${HAKOPOD_API_KEY:?Set a scoped machine key}"
: "${HAKOPOD_APP_ID:?Set the application ID}"

umask 077
app_file=$(mktemp)
trap 'rm -f "$app_file"' EXIT

curl --fail-with-body --silent --show-error \
  --connect-timeout 10 --max-time 40 \
  --header "Authorization: Bearer $HAKOPOD_API_KEY" \
  --output "$app_file" \
  "${HAKOPOD_ORIGIN%/}/api/v1/applications/$HAKOPOD_APP_ID"

jq -e '.spec.services | type == "object"' "$app_file" >/dev/null
jq -e --argjson selected '["setup","celery-beat","celery-worker","api"]' '
  .spec.services as $configured |
  ($selected - ($configured | keys)) as $missing |
  if ($missing | length) == 0 then true
  else error("services not in the Hakopod app spec: " + ($missing | join(", ")))
  end
' "$app_file" >/dev/null

# Continue with the reviewed plan/deployment workflow only after both checks pass.
```

Do not remove TLS verification, use browser session cookies in CI, or bypass the
public listener by exposing the internal API port. A deployment's acceptance is
not rollout success: poll the returned deployment ID until a terminal status.

See [the deployment workflow guide](https://hakopod.com/docs/ci-deployments/) for
selected-service payloads, revision handling and retries.
