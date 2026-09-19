# Attach Git commits to images built by external CI

When your pipeline builds and pushes an image, send its exact image digest and
the commit actually checked out for that build in the same deployment request.
Hakopod stores these claims atomically with the immutable release. Nothing is
inferred from a tag or the current branch HEAD.

This feature requires a release containing the CI provenance change; alpha.17
and earlier do not accept this field.

## Request shape

Both POST /api/v1/plan and POST /api/v1/deployments accept a top-level
`provenance` map keyed by service. Each entry requires:

- `image`: the exact full digest-pinned image reference in that service's spec.
- `commit_sha`: full lowercase 40- or 64-character Git commit ID.
- `provider`: github, gitlab or other.
- `repository`: owner/repository (nested GitLab groups are supported).
- Optional `branch` and `run_url`. Run URLs must use HTTPS without credentials,
  query parameters or fragments.

Send one entry for each service using that build. For a selected-service release,
entries may only describe selected services. Up to 20 entries are accepted.
Use the existing scoped deployment key; no new permission or separate registration
request is needed.

## GitHub Actions example for the four-service backend

Run this after the checkout, image build and push. The workflow must export
IMAGE_DIGEST from its image-build step (for example, the build-push-action digest
output). If the build used a different checkout or remote context, obtain the SHA
from that exact source instead of blindly using GITHUB_SHA.

The following extends an existing complete deployment request in request.json.
That request must already contain the correct spec, scope and expected revision.
It verifies that the image posted for every selected service is the image this
build produced.

```bash
set -euo pipefail
umask 077
: "${IMAGE_DIGEST:?Export the sha256 digest from the image build step}"
image="ghcr.io/narstri/backend.drf@$IMAGE_DIGEST"
commit_sha=$(git rev-parse HEAD)
run_url="$GITHUB_SERVER_URL/$GITHUB_REPOSITORY/actions/runs/$GITHUB_RUN_ID"

jq --arg image "$image" \
   --arg sha "$commit_sha" \
   --arg repo "$GITHUB_REPOSITORY" \
   --arg branch "$GITHUB_REF_NAME" \
   --arg run "$run_url" \
   --argjson selected '["setup","celery-beat","celery-worker","api"]' '
  . as $request |
  if all($selected[]; $request.spec.services[.].image == $image) then
    .services = $selected |
    .provenance = (
      $selected | map({
        key: .,
        value: {
          image: $image, commit_sha: $sha,
          provider: "github", repository: $repo,
          branch: $branch, run_url: $run
        }
      }) | from_entries
    )
  else error("Every selected service must use the digest from this build")
  end
' request.json > request-with-provenance.json
```

Submit request-with-provenance.json to the plan endpoint, review the result, and
deploy with the same provenance and reviewed spec/revision. If constructing a
deployment body from the plan response, carry forward `.provenance` as well as
`.spec` and `.expected_revision`. The metadata is included in the idempotency
hash: changing it while reusing the same key returns a conflict.

Do not substitute the CI config/workflow repository when source was checked out
from a different repository. The recorded repository must identify the code in
the image.

## What MCP returns

The application provenance endpoint and MCP provenance tool return the claim in
the service's `builds` list, with `source: "ci_reported"`, the commit, image,
repository, workflow URL, reporting identity and deployment ID. A unique claim
sets `source_status: "reported_build"` and the service-level `commit_sha`.

These records are authenticated CI assertions, not independently verified
attestations. Hakopod does not fetch the workflow URL or verify a registry
signature. Clients can distinguish them from `source: "hakopod_build"` records.

Mappings remain available across later releases using the same exact image.
Conflicting commits or repositories produce `ambiguous` and omit the authoritative
top-level SHA. Truncated results produce `incomplete` where matches were found.

For ticket closure, verify the reported commit and repository against your trusted
CI run, deployment success, and each service's runtime image IDs and readiness.
Accepted release metadata alone is not proof of a running fleet or a successful
replay.

Existing revisions with unknown provenance are not silently backfilled. Run a new
reviewed deployment with the known digest and its actual build metadata. Do not
guess a commit for an existing digest.
