# ADR 0001: Stable Ingress for the first working release

Status: accepted for Milestone 1. Research and local verification: 2026-09-12.

## Decision

Use HAProxy Kubernetes Ingress Controller Community 3.2.15 with `networking.k8s.io/v1` Ingress. The versioned `hakopod-platform` chart pins upstream chart 1.54.0 and a multi-platform controller image digest. One controller handles only class `haproxy`; its administrative, profiling and metrics listeners have no public Service port. Built-in K3s Traefik and ServiceLB are disabled.

HAProxy Unified Gateway was evaluated first. It is a real open-source Apache-2.0 project: GitHub's current release is v1.0.7 (2026-08-10), and its Helm chart 1.2.0 declares Kubernetes `>=1.26`. Its 1.0 release documentation covers hostname/path HTTPRoute routing, weighted traffic, GatewayClass/Gateway, TLSRoute and associated HAProxy CRDs. It documents Gateway API 1.3/1.4 and experimental 1.5 compatibility. These are upstream claims, not a conformance run performed by Hakopod.

HUG is deferred because the current milestone needs one HTTP ingress controller and rolling Deployments. Adding Gateway API and HAProxy-specific resource lifecycles, listener ownership and certificate attachment now would expand an unverified release surface. No claim is made that HUG is incompatible or uses more memory. Its weighted routing becomes valuable with a tested progressive-delivery milestone. cert-manager's Gateway integration exists; since 1.15 it uses `enableGatewayAPI` configuration, and Gateway CRDs must exist at startup. Certificate automation has not been tested here, so Milestone 1 advertises HTTP explicitly.

The migration boundary is the Go routing builder. Preserve application domains and Services, introduce a dedicated platform Gateway and per-application HTTPRoutes, test accepted/resolved references and certificate renewal, then transfer traffic before deleting old owned Ingresses. Never run two controllers claiming the same class or host/port.

## Pinned local combination

| Component | Pinned version | Reason / verification |
| --- | --- | --- |
| k3d | 5.9.0 | SHA-256-verified release binary; no global installation |
| K3s | 1.35.8+k3s1 | Latest 1.35 maintenance release; matches client-go 0.35.8 and installed kubectl 1.35; actual arm64 node ready |
| HAProxy Ingress Community | 3.2.15 / chart 1.54.0 | Chart supports Kubernetes >=1.23; actual controller ready on 1.35.8 |
| PostgreSQL | 17.11-alpine3.24 | Current 17 maintenance image; actual health check passes |
| Example Python | 3.13.15-alpine3.24 / 3.14.7-alpine3.24 | Multi-platform OCI indexes inspected for amd64 and arm64 |

K3s 1.36.4 is the latest upstream stable at research time. Hakopod deliberately tests the maintained 1.35 minor instead of adopting an untested latest minor. Image pins live in `deploy/local/versions.env`, chart values and the sample TOML. k3d helper images follow its pinned 5.9.0 release; they are development infrastructure, not a production distribution.

## Licensing

Hakopod code is Apache-2.0. K3s and both HAProxy controllers are Apache-2.0; k3d is MIT; PostgreSQL has the PostgreSQL License; Python has PSF licensing. HAProxy the separate proxy executable is GPLv2, with LGPL exportable headers and its documented OpenSSL exception. The controller and HAProxy licenses are distinct. Upstream containers and their notices are retained intact; Hakopod does not copy their source or link their code. The API image has a separate SBOM; complete inventories and redistribution review for all platform images remain release gates.

## Sources

- https://www.haproxy.com/documentation/haproxy-unified-gateway/release-notes/
- https://github.com/haproxytech/haproxy-unified-gateway/releases/tag/v1.0.7
- https://github.com/haproxytech/haproxy-unified-gateway/blob/v1.0.7/LICENSE
- https://github.com/haproxytech/helm-charts/blob/main/haproxy-unified-gateway/Chart.yaml
- https://cert-manager.io/docs/usage/gateway/
- https://github.com/haproxytech/kubernetes-ingress/releases/tag/v3.2.15
- https://github.com/haproxytech/helm-charts/tree/main/kubernetes-ingress
- https://github.com/k3s-io/k3s/releases/tag/v1.35.8%2Bk3s1
- https://github.com/k3d-io/k3d/releases/tag/v5.9.0
- https://github.com/haproxy/haproxy/blob/master/LICENSE
- https://www.postgresql.org/support/versioning/
