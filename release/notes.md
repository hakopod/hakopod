Hakopod 0.1.0-alpha.17 allows ten concurrent HTTP MCP sessions per bearer key and includes the public CI API forwarding fix.

## MCP connections

The per-key limit increases from two to ten sessions, allowing overlapping client reconnects. The global cap remains 32 and sessions expire after ten minutes. Integration tests verify ten accepted sessions, rejection of an eleventh and capacity restoration after DELETE.

## CI API

Machine bearer clients can fetch application specifications, submit plans and deployments, poll deployment status and recover idempotency results through the public self-hosted dashboard's /api/v1 routes. Request bodies, scope and idempotency keys are preserved. Browser cookies are not substituted for machine credentials.

## Upgrade and verification

Declared upgrade candidates are alpha.8, alpha.9, alpha.10, alpha.12, alpha.13, alpha.14 and alpha.15. Alpha.16 publication failed and it is not declared as an installable upgrade source.

Download this release's installer.sh, then run:

```sh
sudo sh installer.sh --upgrade --version 0.1.0-alpha.17
```

The session change passed the full Go suite with disposable PostgreSQL and the dashboard build. The tag was requested without waiting for candidate CI. Artifact publication remains gated on release builds, native smoke tests and installer host acceptance.

The separate HTTP MCP logs error (`unexpected end of JSON input`) remains unresolved. Publishing does not upgrade customer VMs. This is an alpha prerelease.
