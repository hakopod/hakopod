Hakopod 0.1.0-alpha.20 adds Requests: retained HTTP ingress logs and service routing inspection.

## Requests

Open Requests to search traffic across accessible applications, or a service's Requests tab to inspect current ingress routes and backend endpoints. Filter by project, service, method, response or host/path; inspect status, timings, response bytes, TLS and backend selection.

The existing runtime collects privacy-limited HAProxy logs into PostgreSQL. Request queries, headers, cookies and bodies are omitted, peer IPs are masked, and logs:read access is scoped before pagination. Retention is at most 24 hours or 100,000 records across the installation. Collection gaps and stale data are reported. Custom operator logging configuration is preserved.

Coverage is completed HTTP traffic through the managed ingress. Internal traffic, raw TCP and direct pod/host-port traffic are not captured. The route view shows current Kubernetes configuration, not a distributed trace. See [Requests documentation](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.20/docs/requests.md).

## Upgrade

This release adds PostgreSQL request-log, cursor and collection-status tables. The runtime configures privacy-limited logging on the owned platform ingress when no conflicting custom logger exists, causing a normal HAProxy reload; applications do not need redeployment.

Declared upgrade candidates are alpha.8, alpha.9, alpha.10, alpha.12, alpha.13, alpha.14, alpha.15, alpha.17, alpha.18 and alpha.19. Alpha.16 has no published installable release.

Download this release's installer.sh, then run:

```sh
sudo sh installer.sh --upgrade --version 0.1.0-alpha.20
```

The installer backs up PostgreSQL and configuration, migrates state, replaces the API/dashboard and checks readiness. Publishing does not upgrade customer VMs.

## Verification and limitations

The implementation passed the full Go suite with disposable PostgreSQL, focused authorization/retention/pagination/backend-binding/parser/API checks, Go vet, dashboard tests, TypeScript checking and the production dashboard build. A real named development Kubernetes cluster verified HAProxy HTTP capture, backend selection, current routes/endpoints and absence of query/header secrets in source logs. Independent visual review covered both themes, desktop/mobile, the shared header, routing interactions and filters.

TLS termination, long-lived WebSockets, burst-load behavior and multiple ingress replicas were not live-tested. The separate HTTP MCP logs error (unexpected end of JSON input) remains unresolved.

The tag is created before candidate CI completes as requested. Asset publication remains gated on release build, native smoke tests and installation/upgrade acceptance. This is an alpha prerelease.
