# Runtime alarms

Hakopod keeps an alarm inbox and optional email notifications in the existing Go
server and PostgreSQL database. Alarms run without an open dashboard. There is no
additional queue service, metrics server, informer cache, or worker per resource.

## Rules and observations

The native rules are application readiness, service readiness, and node
`NotReady`, `DiskPressure`, `MemoryPressure`, and `PIDPressure` conditions.
Application and service checks use the existing reconciler's runtime snapshots,
never the persisted deployment/application `status` label. A snapshot must have
the current application revision and an observation time no more than 120 seconds
old. Revision changes clear the snapshot; late results cannot overwrite a newer
revision. Failed runtime observations are recorded as unknown.

The alarm worker wakes every five seconds and processes at most 25 applications
per pass, retaining one application's configuration at a time. An unchanged
snapshot is skipped using its aggregate observation timestamp. It reads node
conditions at most once every 30 seconds, in pages of 100 and at most 200 nodes,
without scanning pods or metrics. Each evaluation pass has a 20-second deadline;
the node request has a five-second deadline. A persisted 45-second lease shares
this work across API processes. Runtime resync itself processes pages of up to 50
applications, so detection time includes resync delay, the alarm pass, and the
configured hold. These are bounded sampling intervals, not a real-time guarantee.

A condition must remain unhealthy for its hold before an incident fires. The
default hold is 120 seconds; the supported range is 0–3600 seconds. A fresh healthy
observation recovers an active incident immediately. Repeated observations create
no new notification. An unknown observation resets a pending hold and retains an
active incident without claiming recovery. A gap of more than five minutes also
resets a pending hold; active observations older than five minutes display as
unknown. Acknowledgement does not change the resource or silence future episodes.

Successful complete inventory reads can resolve rules for removed services or
nodes. Deleted applications are resolved by bounded database cleanup. These
incidents explicitly say that the resource was removed and report observation
status `unknown`; removal is not a claim that the resource became healthy.
Pending holds are reset on removal, including when a resource name is reused.
Transient API failures never count as removal.

## Settings and access

All rules are enabled by default, with email off. Application settings inherit
from their environment, then project, then the defaults. Installation settings
apply only to node conditions; they do not override project/application settings.
An exact scope override has its own optimistic revision. An inherited response
uses revision zero when creating the first override at that scope.

Project and environment settings require project administration and deployment
write access. Application overrides require deployment write access to that
application. Reading project alarms/settings and marking them read or acknowledged
require deployment read access in the incident scope. Installation node alarms
and settings require installation administrator access. API-key project,
environment, and application restrictions continue to apply. Every request checks
the caller's current identity, membership, and credential permissions.

Read and acknowledgement marks belong to the identity. A recovery or later
episode is unread again. Clients should send `expected_event_id` when marking an
item so a concurrent unseen transition returns HTTP 409 instead of being marked.

## Email delivery

Email requires both an explicit scope `email_enabled` setting and an operator's
configured SMTP delivery gate. The existing settings are:

- `HAKOPOD_SMTP_ENABLED=true`
- `HAKOPOD_SMTP_ADDRESS=host:port`
- `HAKOPOD_SMTP_FROM`
- `HAKOPOD_SMTP_USERNAME` and `HAKOPOD_SMTP_PASSWORD_FILE` when required
- The existing public dashboard URL, used for the inbox link

The equivalent operator TOML fields are documented in
[operator configuration](operator-configuration.md). Production SMTP requires
STARTTLS and validates the server certificate. The insecure loopback transport is
available only to trusted in-process tests. This feature does not enable SMTP or
send a verification message automatically. `email_available` means delivery is
enabled and configured; it is not evidence of successful receipt.

Recipients are enabled, email-verified human accounts that currently have read
access through direct or team project membership, a personal workspace, or
installation administration. Node recipients must be installation administrators.
Current license restrictions and identity project/environment limits apply.
There are no arbitrary recipient addresses in alarm settings. Membership,
verification, scope settings, and the latest incident transition are rechecked on
every attempt; removed recipients and superseded outage/recovery messages are
skipped. Enabling email does not backfill transitions originally recorded with
email disabled.

Application emails group the service rules: individual service incidents remain
in the inbox, while one aggregate application outage/recovery transition owns the
email. A failure of 20 services therefore does not send 20 extra messages to each
recipient. Node conditions have separate transitions.

The outbox is durable. Recipient fanout handles 25 identities per page with a
10,000-delivery pending queue cap; later pages wait for capacity. Each worker pass
attempts at most two messages sequentially. Deliveries have a 45-second lease,
at most six attempts, exponential delays of 30, 60, 120, 240, and 480 seconds, and
a 24-hour delivery deadline. The SMTP transport shares the existing two-message
concurrency cap and bounded network timeouts with account mail. A retry never
reuses a saved recipient address or stale permission object.

SMTP is not transactional with PostgreSQL. If the process stops after the server
accepts a message but before the sent result is committed, a retry can duplicate
that message. Transition creation and recipient jobs are deduplicated, but email
delivery is at least once at that boundary. The dashboard currently shows the
configured delivery state, not per-message receipt or failure counts. Operators
can inspect `alarm_email_deliveries` for pending, sent, skipped, or failed jobs;
stored failure descriptions do not include SMTP credentials or secret bodies.

## API and retention

- `GET /alarms`: optional `project`, `environment`, `application_id`, `status`,
  `cursor`, and `limit` filters. Pages default to 25 incidents and cap at 50.
  Active/unread counts use the same scope and status filters, independent of the
  cursor. Omitting scope returns only the caller's accessible incidents.
- `GET /alarm-settings` and `PUT /alarm-settings`: scope filters as above;
  omission selects installation node settings. PUT requires the full toggle,
  hold, email, and expected-revision values.
- `POST /alarms/{id}/read` and `POST /alarms/{id}/acknowledge`: optional
  `expected_event_id`, returning the updated incident.

The OpenAPI contract is generated from `api/contracts/alarms.py`. Recovered
incidents and their event/read/delivery records are retained for 30 days and
removed in batches of 100. Inactive observation states are also pruned after
30 days. Active incidents remain until recovery or confirmed resource removal.
Queries have scope, ordering, fingerprint-history, and outbox indexes; the
dashboard does not load all applications to build alarm summaries.

## Verification

`go test ./internal/store ./internal/api ./internal/cluster -run '^TestAlarm'`
includes hold/unknown/recovery behavior, duplicate and concurrent evaluation,
restart behavior, stale snapshots/revisions, removed/recreated resources, scoped
inbox/settings access, per-identity read guards, revoked recipients, superseded
mail, lease expiry, and SMTP failure/retry. Database tests create and remove
isolated PostgreSQL databases using `HAKOPOD_TEST_DATABASE_URL`. SMTP tests bind
only to loopback and send no external messages. A production SMTP provider and
external email receipt are not verified by these tests.

The local development check on 2026-09-13 confirmed that clearing disposable
caches relieved node disk pressure and restored the affected pods. The shop
answered all 83 HTTP probes during the management-server restart. Application
revisions and account setup were preserved. The authenticated browser then
loaded the real inbox, settings and current shop health. SMTP remained disabled.
The dashboard proxy has direct request tests for alarm routing, sealed sessions,
CSRF rejection and concurrent-event guards.
