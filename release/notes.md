Hakopod 0.1.0-alpha.16 fixes public CI API forwarding through the self-hosted dashboard.

## CI deployment access

Machine bearer clients can now fetch application specifications through the public HTTPS dashboard at `/api/v1/applications/{id}`, submit plans and deployments, poll deployment status and look up idempotency results. Previously this route returned 404, which could lead CI scripts to report that services were missing from an empty specification.

The proxy preserves selected-service payloads, revision values, query scope and idempotency keys. Canonical API authorization remains authoritative. Browser cookies are not substituted for machine credentials. See [CI API access](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.16/docs/ci-api.md).

## Upgrade

Declared upgrade candidates are alpha.8, alpha.9, alpha.10, alpha.12, alpha.13, alpha.14 and alpha.15. Publication remains gated on the native installer acceptance matrix.

Download this release's `installer.sh`, then run:

```sh
sudo sh installer.sh --upgrade --version 0.1.0-alpha.16
```

The installer validates the upgrade path, backs up PostgreSQL and configuration, replaces the API/dashboard and checks readiness. Publishing does not upgrade customer VMs.

## Verification and known limits

The CI forwarding change passed 43 server tests, 83 UI tests, TypeScript checking, dashboard build and local Go tests. PostgreSQL integration was not enabled in that local run. The tag was requested before candidate CI completed; release publication still requires its full build, smoke and native host checks.

The separate HTTP MCP logs error (`unexpected end of JSON input`) is not fixed in this release. No new Git commit mapping for externally built images is added.

This is an alpha prerelease. Assets include checksums, SBOMs, provenance and acceptance reports after successful publication.
