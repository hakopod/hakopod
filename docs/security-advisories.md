# Security advisories

Hakopod checks running applications against published security advisories,
shows the affected apps in the dashboard, notifies your channels, and offers a
one-step update where one is possible. Automatic patching is available per app
and is off by default.

This is not a vulnerability scanner. No findings does not mean an app is free
of vulnerabilities. See [Limits](#limits).

## Overview

Advisories come from two sources:

- **The Hakopod-signed feed.** Hakopod maintainers publish advisories for
  images and runtimes that Hakopod users commonly run. The feed is signed with
  Ed25519, and Hakopod checks the signature against a public key embedded in
  the binary. The signed message is the payload prefixed with the domain string
  `hakopod-security-advisories-v1` and a newline, so a signature made for
  another purpose cannot be reused here. A feed is valid for 14 days after it
  is signed. A feed past its expiry is kept but marked stale; one-step updates for its findings
  are refused while the feed is stale. A feed that has never been fetched and
  verified also counts as stale, so updates for signed-feed findings are
  refused until the first verified feed arrives (for example, before the feed
  branch is published). A feed whose revision is older than the
  last accepted one is rejected, so an attacker cannot roll the feed back.
  The feed is fetched from `DefaultFeedURL` in `internal/advisory/feed.go`:

  ```
  https://raw.githubusercontent.com/hakopod/hakopod/security-advisories/security-advisories.signed.json
  ```

  Set `HAKOPOD_ADVISORY_FEED_URL` on the server to fetch it from a mirror. The
  mirror must serve the same signed file; the signature is still checked.

- **OSV.dev.** Hakopod queries `https://api.osv.dev` for the runtime versions
  it finds. Coverage is limited to runtimes OSV tracks: Go through the Go
  `stdlib` package, and Node.js, Python, PHP, Ruby and Java through the
  Bitnami ecosystem. An OSV advisory's revision is its published date (its
  modified date when it has none). An OSV finding that shares an identifier
  (advisory id or CVE alias) with a signed-feed finding on the same service is
  dropped, so each issue is shown once. Set `HAKOPOD_OSV_URL` to use another
  OSV endpoint.

The feed status shows one of these errors when a fetch fails: "Could not reach
the advisory feed.", "The advisory feed has not been published yet.", "The
advisory feed returned HTTP <code>.", "The advisory feed signature does not
verify." or "The advisory feed is invalid."

The dashboard shows a banner when any app you can see is affected, and the
review page lists every finding with its evidence and fix.

API: `GET /api/v1/security/advisories` returns the feed status, the affected
applications and a summary. `GET /api/v1/applications/{id}/security` returns
one application. Both need `deployments:read` on the application.

## How matching works

Matching uses only what Hakopod already knows about a deployment. Nothing is
executed inside workloads.

Evidence per service:

- **Resolved image digest.** Each deployment records the `sha256` digest its
  image tag resolved to. Image advisories list affected digests and tags.
- **Runtime version.** Hakopod reads the image configuration environment for
  `NODE_VERSION`, `PYTHON_VERSION`, `GOLANG_VERSION`, `PHP_VERSION`,
  `RUBY_VERSION` and `JAVA_VERSION`, as set by the official runtime images.
  Runtime advisories list affected version ranges.

A service whose image sets none of these variables, or whose image
configuration could not be read, is reported as unknown for runtime
advisories. Image advisories still apply to it. Unknown is never treated as
safe.

Each finding carries a confidence:

- **Confirmed** ("Affected"): the resolved digest or the runtime version
  matches the advisory.
- **Possible** ("Possibly affected"): only the image tag matches.

Schedule:

- The signed feed is checked hourly. Failed fetches back off up to six hours
  and honour a `Retry-After` header.
- Evaluation uses the app's latest succeeded deployment, so a later failed
  rollout does not hide what is running. An app is re-evaluated when that
  deployment changes or a new feed revision arrives.
- A failed evaluation backs off from 1 minute, doubling up to 6 hours.
- OSV results are cached and refreshed every 24 hours.

To re-check one app now, use **Check again**, or
`POST /api/v1/applications/{id}/security/check`, which needs
`deployments:write`. Checks are limited to one per 30 seconds per app (HTTP
429 otherwise). It checks the latest succeeded deployment, even while a newer
one is in progress or has failed. It returns 409 only when the app has never
had a succeeded deployment.

### Incomplete checks

A check can be incomplete: OSV.dev returned more results than Hakopod reads,
OSV.dev was unavailable, or the registry could not be read for an image. The
app then shows what is missing, and it is never treated as clean. If the user
who turned on automatic patching loses deploy access to the app, automatic
patching pauses and the app shows that too. The same happens when the
approval expires (see [Automatic patching](#automatic-patching)).

## Notifications

Findings are sent through the app's
[deployment notification](deployment-notifications.md) destinations using the
per-app event **Security advisories** (`security_advisory`). The event is
turned on for every existing destination when you upgrade, and you can turn it
off per destination.

Slack integrations receive the event `security.advisory`.

Each finding is sent once per app, advisory, advisory revision and service. A
maintainer publishing a new revision of an advisory sends it again. Only
confirmed findings notify; possible findings appear in the dashboard only.
Confirmed findings from the signed feed notify at any severity. Confirmed OSV
findings notify only when their severity is critical or high. At most five
new notifications are queued each time an app is evaluated. Findings already
sent do not count towards that limit, so the rest go out on later evaluations.

Every automatic patch failure is also sent through the `security_advisory`
event. The dashboard notes: "Security advisories go to app notification
channels that include them."

## Updating affected apps

Each finding names a fix:

- **Update** (`reresolve`): for image services configured by tag. Hakopod
  creates a new revision that resolves the image tags again, picking up the
  fixed image the tag now points at.
- **Rebuild** (`rebuild`): for services built from source. Hakopod rebuilds the
  same commit, which pulls the current base image.
- **Manual** (`manual`): Hakopod cannot fix this for you. Cases:
  - The image is pinned by digest. Change the pinned digest.
  - The service uses a Hakopod-generated Dockerfile whose base image Hakopod
    pins. Update Hakopod, then rebuild.
  - The advisory itself asks for a manual change; its message says what to do.

API: `POST /api/v1/applications/{id}/security/apply` with
`{"advisory_id", "service", "expected_revision"}` and an `Idempotency-Key`
header of 8 to 128 characters. It needs `deployments:write` and returns `202`
with the new deployment or build. It returns `409` when the app revision
changed, when the latest revision is still deploying, or when it failed ("Roll
back or fix it, then update"). It returns `422` when the fix is manual, the finding is no longer present, a
signed-feed finding is being fixed while the feed is stale, or a rebuild has no
build configuration.

## Automatic patching

Automatic patching is per app and off by default. Turn it on from the app's
security page, or with `PUT /api/v1/applications/{id}/security/policy`
(`{"auto_patch", "expected_policy_revision"}`, needs `deployments:write`). The
user who turned it on is recorded.

Turning it on creates a deployment grant key for that user, limited to this
app. Automatic updates run with that key. It expires after 90 days and is not
renewed; the app shows "Approval expires" with the date. When it expires,
patching pauses with a note; turn automatic patching off and on again to
approve again. The key is revoked when patching is turned off or the app is
deleted.

Hakopod patches automatically only when all of these hold:

- the advisory comes from the Hakopod-signed feed, not OSV, and the feed is
  not stale;
- the advisory's action is `update`;
- it is an image advisory that lists fixed digests;
- the match is confirmed by digest and the service is configured by tag;
- the tag now resolves to one of the fixed digests.

The approving user must still have
`deployments:write` on the app; otherwise patching pauses (see
[Incomplete checks](#incomplete-checks)). Hakopod applies at most one automatic
update per scan.

Each app, advisory, revision, service and deployment gets one attempt. It is
never retried. After the deploy succeeds, Hakopod checks the app again and
marks the attempt verified when the advisory no longer applies. The attempt
fails when the update is not accepted within 10 minutes, the deployment fails
or is cancelled, or the advisory still applies afterwards. A failure notifies
your channels and leaves the finding open. If the patch deployment failed, the
apply endpoint returns 409 until that failed revision is rolled back or fixed;
only then can you update again. If the app
changes revision in the meantime, the attempt is marked superseded. Attempts
are listed on the app's security page.

## Settings

Installation admins can turn either source off, for example on an air-gapped
installation that cannot reach GitHub or OSV.dev. Turning a source off stops
fetching from it.

API: `GET /api/v1/security/settings` is readable by any signed-in user; its
`can_manage` field says whether you can change them.
`PUT /api/v1/security/settings` with
`{"feed_enabled", "osv_enabled", "expected_revision"}` needs an installation
admin. Both sources are on by default.

## Limits

- Hakopod does not scan operating system packages, language dependencies or
  application code. It only matches image digests, tags and the runtime version
  variables listed above.
- Coverage is only as good as the advisories published. No findings does not
  mean an app is free of vulnerabilities.
- Images that do not set a runtime version variable are unknown for runtime
  advisories.
- A tag-only match is reported as possible; Hakopod does not guess further.

## Maintaining the feed

### Add or change an advisory

Edit `security-advisories.json` at the repository root on `main`:

```json
{
  "advisories": [
    {
      "id": "HAKOPOD-2026-0001",
      "revision": 1,
      "source": "hakopod",
      "title": "Node.js October 21, 2026 security releases (highest severity: high)",
      "message": "Node.js 22.x, 24.x and 26.x before 22.23.4, 24.22.1 and 26.12.1 are affected.",
      "severity": "high",
      "url": "https://nodejs.org/en/blog/vulnerability/october-2026-security-releases",
      "aliases": ["CVE-2026-105482", "CVE-2026-105483"],
      "published_at": "2026-10-21T00:00:00Z",
      "action": "update",
      "targets": [
        {"runtime": {"runtime": "node", "ranges": [
          {"introduced": "22.0.0", "fixed": "22.23.4"}
        ]}}
      ]
    }
  ]
}
```

Fields:

- `id` is unique and never reused. `revision` starts at 1; increase it when an
  advisory changes in a way users must hear about again.
- `aliases` (optional, up to 20) lists other identifiers such as CVE ids. An
  OSV finding sharing one of them on the same service is merged into this
  advisory's finding.
- `severity` is `critical`, `high`, `moderate` or `low`. `action` is `update`
  or `manual`. `url` must be `https://`.
- Each target is either `runtime` (`runtime` plus `ranges` with `introduced`
  and `fixed` or `last_affected`) or `image` (`repository`, and any of
  `affected_digests`, `affected_tags`, `fixed_digests` as `sha256:` digests).
  Automatic patching needs `fixed_digests`.

Unknown fields are rejected. A feed holds at most 500 advisories.

### Signing and publishing

`.github/workflows/security-advisories.yml` runs on every push to `main` that
changes `security-advisories.json`, daily, and on manual dispatch. It runs
`go run ./cmd/sign-advisories security-advisories.json`, which reads the
private key from the repository secret `HAKOPOD_ADVISORY_SIGNING_KEY` (a
base64 64-byte Ed25519 private key), refuses a key that does not match the
embedded public key, sets the revision from the current time, sets a 14-day
expiry, and verifies its own output. The workflow commits the result as
`security-advisories.signed.json` to the orphan branch `security-advisories`.

The daily run re-signs an unchanged file so the feed never expires while the
workflow is healthy. If it stops, installations mark the feed stale after 14
days.

### Rotating the signing key

The public key is embedded from `internal/advisory/signing-public-key.txt`, so
rotation needs a release:

1. Generate a new Ed25519 key pair.
2. Replace `signing-public-key.txt` with the new public key (base64) and ship a
   release.
3. Once installations have upgraded, replace the `HAKOPOD_ADVISORY_SIGNING_KEY`
   secret and run the workflow.

Installations still on the old release reject feeds signed with the new key and
eventually mark their feed stale, so give users time to upgrade between steps 2
and 3.
