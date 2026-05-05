# Milestone tracker and verification

The first milestone's acceptance contract was written before implementation in
docs/IMPLEMENTATION-CONTRACT.md. Implementation, individual verification and
the full acceptance gate are recorded separately below.

## Milestone 1: working deployment path

Implemented: Go API/reconciler, explicit PostgreSQL migration, durable accepted
operations, scoped expiring/revocable API keys, strict TOML, generated TypeScript
API client, CLI, real TanStack dashboard, isolated K3s environment, HAProxy
routing, private DNS/policies, immutable digests, targeted/group updates,
readiness, failure recovery, rollback, logs and node observations.

Verified individually on real infrastructure during this build:

- PostgreSQL17: concurrent duplicate requests, stale revision rejection,
  serialized releases, reconnect/reclaim, owner/key authorization boundaries,
  expired/revoked/disabled identities, no stored plaintext key.
- K3s1.35.8 arm64: public web proxies to private API; ClusterIP and direct-pod-IP
  boundaries; stable DNS after pod replacement; no public API ingress.
- Same installed CNI across two containerized nodes, including permitted and
  denied communication and DNS after replacement. Temporary worker removed.
- Named networks and internal-only egress, mixed memberships, required external
  DNS, actual HPA replica ownership and preserved restart annotations.
- Actual failed readiness with old healthy pod retained, controller deadline,
  and restoration of the prior healthy configuration.
- Partial group failure with one service ready and another unready: both prior
  configurations restored. Invalid registry tags are rejected before Kubernetes
  mutation; a separate kubelet test observed real `ImagePullBackOff` diagnostics.
- Management process killed during a scoped-key deployment: the same operation
  resumed after restart. The latest interruption happened after workload apply;
  97 public traffic probes recorded zero errors. CLI status and logs required no
  browser session.
- Dashboard production build, type checks, session/CSRF/body-limit/HTTPS tests,
  production HTTP smoke and real API-backed overview in a browser.

**The full PostgreSQL17/K3s lifecycle and restart gates passed on 2026-09-12.**
The lifecycle run deployed a baseline, updated the private API image, recovered
from an intentionally unready release and explicitly rolled back: 585 public
traffic requests, zero errors. The restart run resumed the same accepted
operation after management-process termination: 97 requests, zero errors. Both
temporary scoped keys were revoked. Credential-free evidence is recorded in
`.local/acceptance.json` and `.local/restart-acceptance.json` on the development
machine. The earlier engine outage and interrupted run remain historical
diagnostics in `.local/acceptance-interrupted.json`; they are not pass evidence.

Final post-review store/API regressions passed with the race detector against
the restored PostgreSQL17 database, with no skipped tests: terminated-backend recovery, immutable digest
persistence, terminal-state protection, owner scope reduction, targeted diff and
artifact preservation, rollback seeding, concurrent capacity limits and pagination.

The rebuilt arm64 API image passed isolated startup, embedded-contract, license
and native CLI checks; its server bytes match the release archive. Actual image
and Go/dashboard dependency SBOMs are generated. Browser deployment submission
succeeded through revision 13, and live node views, mobile navigation and the
light theme were checked. Final key-management checks passed: scoped creation,
copy without an extra form submission, an 89-day rotation retaining the same
scope and 15-minute old-key overlap, revocation followed by HTTP 401, and audit
history. At a 390-pixel viewport there was no horizontal overflow, and ten Tab
presses skipped the closed mobile navigation. The final dashboard type checks,
regression tests and production build passed. Full Go tests and `go vet` also
passed; no temporary live-test databases or namespaces remained.

## Milestone 2: usable single-node release

Some foundations are implemented: scoped key lifecycle, stage/review/deploy,
named networks/internal egress, live logs and CPU HPA. Still required: Ubuntu
24.04 disposable-VM installer verification, human authentication/member roles,
OS credential-store integration, private registries, ownership-verified custom
domains, cert-manager/ACME staging/renewal, built-in secret bindings and encryption
key lifecycle, Infisical operator integration and outage tests, persistent drafts.

## Milestone 3: cluster expansion

Cross-node CNI communication is verified locally. Dashboard enrollment, bounded
K3s token lifecycle, physical worker readiness, cordon/drain/remove UX and
disruption-budget blockers remain unimplemented. Extra workers are not control
plane or ingress endpoint HA.

## Milestone 4: operational hardening

Architecture/recovery notes, CI, release image and artifact preparation are
present. No production-readiness claim: external backup restoration, upgrades,
HA database/control plane/ingress topology, retained observability, capacity
load testing and host-level failure injection remain acceptance gates.
