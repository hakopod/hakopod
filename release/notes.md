Hakopod 0.1.0-alpha.19 adds application deployment notifications.

## Deployment notifications

Configure email, Slack, Discord and signed generic webhooks from an application's Deployments → Notification settings page. Choose success, failure or cancellation events, review destination settings, send a test and inspect recent delivery history.

Notifications use a durable PostgreSQL outbox with bounded retries and retention. Email reuses installation SMTP. Destinations are encrypted and write-only; generic webhooks use HMAC-SHA256 signatures. HTTP destinations require public HTTPS and reject private addresses and redirects.

See [deployment notifications](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.19/docs/deployment-notifications.md) for setup and receiver verification. This release also includes alpha.18's external CI commit-to-image provenance support.

## Upgrade

This release adds notification tables and a deployment-status trigger, as well as the preceding immutable provenance migration. Declared upgrade candidates are alpha.8, alpha.9, alpha.10, alpha.12, alpha.13, alpha.14, alpha.15 and alpha.17. Alpha.16 has no published installable release; alpha.18 was still awaiting publication when this candidate was prepared.

Download this release's installer.sh, then run:

```sh
sudo sh installer.sh --upgrade --version 0.1.0-alpha.19
```

The installer backs up PostgreSQL and configuration, migrates state, replaces the API/dashboard and checks readiness. Publishing does not upgrade customer VMs.

## Verification and limitations

The notification implementation passed the full Go suite with disposable PostgreSQL, Go vet, dashboard tests, TypeScript checking and the production dashboard build. Local SMTP and mocked HTTP receivers verified delivery, payloads and signatures. Independent UI review covered both themes and desktop/mobile layouts; its scoped limits are recorded in docs/deployment-notifications-ui-review.md. No live provider delivery or VM deployment was performed.

The tag is created before candidate CI completes as requested. Asset publication remains gated on release build, native smoke tests and installation/upgrade acceptance. The separate HTTP MCP logs error (unexpected end of JSON input) remains unresolved. This is an alpha prerelease.
