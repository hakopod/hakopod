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
- Management process killed during a scoped-key deployment: the same operation
  resumed after restart; 122 public traffic probes recorded zero errors. CLI
  status and logs required no browser session.
- Dashboard production build, type checks, session/CSRF/body-limit/HTTPS tests,
  production HTTP smoke and real API-backed overview in a browser.

**Full final combined gate is still pending:** the shared OrbStack Docker engine
became unresponsive during the final lifecycle rerun. Its database and Kubernetes
API stopped answering; the Go health endpoint remained responsive. Earlier runs
exposed and led to fixes for old-pod readiness and stale controller deadline
conditions. The final interrupted run is not recorded as a pass. See
`.local/acceptance-interrupted.json` and `.local/restart-acceptance.json` on the
development machine. Final post-review store/API regressions passed with the race detector against an
independent PostgreSQL16 process: terminated-backend recovery, immutable digest
persistence, terminal-state protection, owner scope reduction, targeted diff and
artifact preservation, rollback seeding, concurrent capacity limits and pagination.
The exact PostgreSQL17/K3s combined rerun remains needed.

To close the gate after Docker recovers, rebuild/restart the Go server, revoke
the temporary key noted in the build report, run `scripts/acceptance.py`, rerun
`tests/restart.py` with its explicit server PID, and finish dashboard application,
deployment and node browser checks. Preserve actual traffic-error counts.

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
