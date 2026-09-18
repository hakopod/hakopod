Hakopod 0.1.0-alpha.14 adds selected multi-service deployments through the API and makes public custom domains visible in application and service views.

## Selected service deployments

Send `"services": ["setup", "api", "celery-worker", "celery-beat"]` to `POST /api/v1/plan` or `POST /api/v1/deployments` alongside the application configuration and scope. Selected services share one accepted application revision. Unselected services retain their accepted configuration, resolved image digests and registry credential references.

The existing `service` string remains supported. Use either selector; omit both for an application-wide release. Empty arrays, duplicate names, missing services and more than 20 selections are rejected. Shared configuration changes require an application-wide plan. Service dependencies determine ordering; the selection array does not. Rollouts are not atomic across containers.

The Deployments tab links to [the CI deployment guide](https://hakopod.com/docs/ci-deployments/), covering scoped keys, planning, retries and rollout status. API clients and OpenAPI types include the new selector.

## Public endpoints

Application headers and service Networking show configured custom domains alongside observed public HTTP endpoints. Compact previews open a searchable list for larger sets, including service names and copy controls. Long domains remain accessible without stretching the page.

Custom routing status is displayed separately. Pending setup, unavailable status and active configuration are distinguished; a configured domain is not a claim that DNS, TLS or the workload is healthy. Verification-only domains that have not been added to the application are excluded.

Log searches and live logs now wrap long lines by default. The Wrap control can still switch to horizontal scrolling.

## Upgrade

Supported source versions are alpha.8, alpha.9, alpha.10, alpha.12 and alpha.13, subject to the native candidate acceptance matrix. Alpha.11 has no installable assets.

Download this release's `installer.sh`, then run:

```sh
sudo sh installer.sh --upgrade --version 0.1.0-alpha.14
```

The installer validates the upgrade path, backs up PostgreSQL and configuration, replaces the API/dashboard and verifies readiness. Application workloads remain running. Do not bypass upgrade guards or use `--resume` to change versions.

## Verification and artifacts

API integration tests cover group plans using JSON and TOML, invalid selections, shared configuration rejection and acceptance as one durable revision. Dashboard tests cover endpoint collection, status, deduplication and URL filtering. UI review coverage is recorded in `docs/public-endpoints-ui-review.md`.

Publication requires Go/dashboard CI, native packaged smoke tests and fresh-install/upgrade acceptance with managed PostgreSQL on AMD64/ARM64 and local/TLS-verified external PostgreSQL on AMD64. Release assets include binaries, dashboard, installer, checksums, SBOMs, provenance and acceptance reports. The readiness helper is `ghcr.io/hakopod/hakopod-probe:v0.1.0-alpha.14`; use the immutable reference in `probe-image.txt`.

This is an alpha prerelease. These checks do not certify arbitrary customer workloads, public ACME issuance or database restores. No customer VM is upgraded by publishing this release.
