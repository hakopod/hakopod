# Slack integration validation record

This record separates implemented behavior from checks run on the designated
Hakopod development VM. It does not describe a deployment or a published Slack
app.

## Implemented behavior

Hakopod exposes Slack configuration through **Settings → Integrations → Slack**.
Self-hosted administrators receive a manifest, create and install their own
Slack app, then authorize it. Hakopod Cloud workspace owners authorize the
published Hakopod Cloud app. A managed Cloud node relays only committed events
for its configured project and environment; it does not receive or submit a
Cloud workspace ID.

The API stores configuration revisions and OAuth generations, queues selected
alarm, audit, deployment, application and service events durably, bounds
pending or leased events to 10,000, and leases Cloud relay events. A failed
Cloud enqueue returns the lease to `pending` with a bounded backoff. The
associated regression checks that the lease clears and a later retry remains
possible. Cloud relay event IDs are deterministic fixed-length hashes, so a
maximum-length source ID remains within the Cloud ID limit across retries.

The self-hosted feature gate is the `slack_notifications` entitlement. The
dashboard may show the Pro requirement, but the API remains the enforcement
point. A managed Cloud relay node does not need a local Slack entitlement;
Cloud checks that workspace's active Pro eligibility before accepting and again
before delivering the event.

## Catalog expansion status

The selectable catalog contains alarm opened and resolved, audit, deployment
lifecycle and intent, application lifecycle and accepted configuration, and
service configuration and outcome events. The database validates every catalog
ID and limits a selection to 64 event types. Accepted deployment and
configuration events carry only identifiers, scope, revision and fixed event
metadata. They exclude service messages, images, commands, variable and secret
values, specifications and log bodies. Self-hosted alarm rows and rendered
messages likewise exclude alarm summaries.

Focused VM checks cover catalog rendering, selection, transaction rollback,
database constraint round trips, status and intent idempotency, redaction,
source scope and bounded Cloud event IDs. The opt-in real-cluster test remains
separate from Slack transport: it deploys two digest-pinned services only to
the named `k3d-hakopod-dev` development cluster, checks committed self-hosted
and Cloud outbox rows, and removes its owned preview namespace.

## VM validation

The following commands ran in bounded systemd units on the designated
development VM. Logs are retained under
`/srv/hakopod-backup-scratch/slack-pro-20261004/evidence/`.

| Scope | Result | Evidence |
| --- | --- | --- |
| Complete platform Go suite with disposable PostgreSQL | Passed: `go test -count=1 -p=2 ./...`; live flags remained unset | `platform-full-db-v1.log` |
| Expanded uncached Slack store checks with disposable PostgreSQL | Passed: `go test -count=1 -v ./internal/store -run Slack`; seven cases, no skips | `platform-db-store-slack-v3.log` |
| Expanded uncached Slack API checks with disposable PostgreSQL | Passed: `go test -count=1 -v ./internal/api -run Slack`; nine top-level cases, no skips | `platform-db-api-slack-v3.log` |
| Expanded real-cluster Slack outbox acceptance | Passed: two digest-pinned services deployed twice on `k3d-hakopod-dev`; selected self-hosted and Cloud outbox records verified and the owned preview namespace removed | `platform-live-slack-v3.log` |
| Public dashboard typecheck | Passed: `pnpm typecheck` | `dashboard-final-typecheck-v4.log` |
| Public dashboard production build | Passed: `pnpm build` | `dashboard-final-build-v4.log` |
| Public dashboard tests | Passed: 218 UI tests; no failures or skips; the same command also runs the server suite | `dashboard-final-test-v4.log` |
| SDK tests | Passed: 52 tests; no failures or skips | `expanded-sdk-tests.log` |

Cloud and website validation are recorded in their respective repositories.
Earlier successful commands without a test database, and empty redirected logs
from failed command dispatches, are not evidence for the final database-backed
or frontend implementation.

Both current development-only review fixtures returned HTTP 200 after the
source sync:

- Self-hosted dashboard: `127.0.0.1:14595/scripts/slack-ui-review.html`
- Composed Cloud dashboard: `127.0.0.1:14596/scripts/slack-ui-review.html`

The earlier complete platform command, `go test ./...`, did not receive
`HAKOPOD_TEST_DATABASE_URL` and therefore does not validate the database-backed
Slack paths. The current full command ran uncached with a disposable PostgreSQL
role and bounded package parallelism; it passed with live flags unset. The
focused database-backed Slack commands above provide verbose, zero-skip
evidence for the catalog paths. The named `k3d-hakopod-dev` context and its
three development nodes were verified before the opt-in real-cluster Slack
outbox acceptance. That run passed in 146.454 seconds without starting Slack
transport; its temporary PostgreSQL role and owned preview namespace were
removed afterward.

## UI review

The independent reviewer approved the affected dashboard UI after inspecting
the current 40-case matrix, recovery states, screenshots, real element bounds,
and keyboard and pointer interaction. Coverage and limitations are recorded in
[the Slack UI review record](slack-ui-review.md). The review fixture uses
artificial API and workspace records only. Actual touch, assistive technology,
live OAuth and Slack transport were not part of that review.

The related website UI review is recorded in the website checkout's
`docs/ui-review.md`. Its public wording points people to
**Settings → Integrations → Slack**.

## Not verified or released

No Hakopod production deployment has occurred. The Hakopod Cloud Slack app has
been created and installed into its owning Slack workspace with the approved
scopes. Public distribution remains inactive; it has not been published to
Slack's directory, and no external workspace installation has been performed.
No live Hakopod OAuth callback, Slack channel discovery, message
delivery, retry, audit event, alarm event, entitlement check, or managed-node
relay has been tested against Slack. The VM fixtures do not authenticate a
customer, mutate a real installation, or establish production availability.
