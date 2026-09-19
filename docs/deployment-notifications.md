# Deployment notifications

Open an application's **Deployments → Notification settings** page to configure
email, Slack, Discord or generic webhook destinations. Each application supports
up to five destinations, independently enabled for deployment success, failure
and cancellation. There are no destinations enabled by default.

A deployment notification describes the final release result. It is not a
continuous application-health alarm or proof that a subsequent replay succeeded.
Existing deployments are not replayed when a destination is added.

## Configure a destination

1. Give it a recognizable name and choose a channel.
2. Enter one recipient email or the incoming webhook URL.
3. For generic webhooks, choose a random 32–256 character signing secret and
   configure the same secret in your receiver.
4. Select the events and whether to enable the destination.
5. Review and save, then use **Send test** to queue a test message.

Email uses the installation's existing SMTP settings. An administrator must
enable SMTP delivery first; Cloud users cannot change operator SMTP settings.
A destination can be saved paused while SMTP is unavailable.

For Slack, create an incoming webhook in your Slack app and paste the
https://hooks.slack.com/services/ URL. For Discord, use Server Settings →
Integrations → Webhooks and paste its https://discord.com/api/webhooks/ URL.
Slack link previews/markup and Discord mentions are disabled in sent messages.

Destinations and webhook signing secrets are encrypted using the installation's
authentication encryption key. They are never returned through the settings API.
When editing, blank destination/secret fields retain the saved values. Changing
the channel type requires entering a new destination.

Readers need application deployments:read access. Managing destinations and
sending tests require deployments:write access. Keep destination access aligned
with who should receive application deployment metadata.

## Delivery behavior

A PostgreSQL outbox records notification jobs atomically when a deployment moves
to succeeded, failed or cancelled. Sending happens in a bounded background worker;
delivery failures do not change the deployment's result.

- Successful HTTP responses are 2xx. HTTP 408, 429, 5xx and connection failures retry.
  Other responses, including redirects, fail without automatic retry.
- Delivery has at most five attempts, exponential retry delays and a 24-hour deadline.
- Delivery is **at least once**. A receiver may accept a message just before a
  connection/process failure; use the stable event ID to deduplicate retries.
- Only two messages are attempted per worker cycle, and each attempt has a deadline.
- The pending queue is capped at 10,000 jobs. If full, the deployment records a
  notification_skipped event instead of blocking its rollout.
- Recent history shows the latest 25 results for the application. Results are
  retained for 30 days; removed destinations also remove their delivery history.
- Changing destination settings skips pending jobs for its old revision. Disabling
  or deleting a destination stops future attempts, but an in-flight send may finish.
- Test messages are limited to one per destination per minute.

Webhook requests only use public HTTPS addresses on port 443. Every connection
resolves and validates addresses before dialing them directly. Private, local,
link-local and reserved addresses are rejected, environment proxies are not used,
and redirects are not followed. Internal-only webhook receivers are not supported.

Delivery history contains status, attempts and a sanitized failure explanation.
Provider response bodies, webhook URLs, secrets, application configuration,
environment variables and workload logs are not included.

## Generic webhook protocol

Receivers get a JSON POST with Content-Type: application/json and these headers:

- X-Hakopod-Event-ID: stable delivery event ID.
- X-Hakopod-Timestamp: signing time as Unix seconds.
- X-Hakopod-Signature: sha256= followed by the hexadecimal HMAC-SHA256.

Compute the HMAC with your secret over the timestamp string, a literal dot, and
the **exact raw request body**. Compare signatures in constant time, enforce a
short timestamp tolerance (for example five minutes), and deduplicate event IDs.
Do not reserialize the JSON before verifying.

Example payload (fixture values):

```json
{
  "schema_version": 1,
  "event_id": "deployment-id-destination-id",
  "deployment_id": "deployment-id",
  "application_id": "application-id",
  "application_name": "example",
  "project": "demo",
  "environment": "production",
  "revision": 12,
  "status": "succeeded",
  "recovery_state": "",
  "occurred_at": "2026-09-19T00:00:00Z",
  "dashboard_url": "https://hakopod.example/applications/application-id?tab=deployments"
}
```

Test messages have status=test and no deployment_id. The dashboard URL is present
only when the installation's public URL is configured.

## API

- GET /api/v1/applications/{id}/notifications: destinations, history and availability.
- POST /api/v1/applications/{id}/notifications: create with expected_revision=0.
- PUT /api/v1/applications/{id}/notifications/{target}: update using its revision.
- DELETE the target route with expected_revision to remove it.
- POST the target route plus /test with expected_revision to queue a test.

Use the OpenAPI NotificationInput schema for request bodies. These application
settings are exposed through the normal authenticated dashboard transport; the
limited external CI proxy does not expose notification administration.

## Verification

Automated tests use disposable PostgreSQL, a local SMTP fixture and isolated HTTP
receiver mocks. They verify lifecycle enqueueing, duplicate suppression,
authorization, encrypted/redacted settings, provider payloads, webhook HMAC,
retry outcomes, revision changes and destination removal. No real Slack, Discord,
webhook account or external mailbox was contacted during development.
