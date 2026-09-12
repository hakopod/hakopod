# Milestone tracker and verification

The first milestone's acceptance contract was written before implementation in
docs/IMPLEMENTATION-CONTRACT.md. Implementation, individual verification and
the full acceptance gate are recorded separately below.

## Current operations expansion — 2026-09-12

This update adds dedicated nested forms, 13 deployable database/application/agent
presets and a guided Xem entry, first-install sample shop, GitHub/GitLab TOML
import, avatars and team usernames, verified custom domains, explicit host-shell
grants, and encrypted S3-compatible backups with schedules and reviewed restores.
The [template guide](templates.md) and [backup guide](backups.md) describe exact
requirements and recovery scope.

The full Go race suite passed against disposable PostgreSQL databases, followed
by the affected backup retry regressions and `go vet`. Dashboard build, strict
types, formatting and 20 tests passed. Generated contracts reproduce unchanged.
Nine installer tests and Bash syntax passed.

Real cluster tests verified Redis, ClickHouse and secure CockroachDB authentication,
persistent restarts and cleanup; sample web/private-catalog checkout and removal;
custom HTTP/TLS domain routing; and scoped host shell input/output and cleanup.
Backups restored 1,200 PostgreSQL rows, two MySQL rows and a management fixture
account into fresh databases. Expired reviews, replaced pod UIDs, existing
database names and corrupt objects were refused with originals unchanged.
Transaction fault tests cover sample cancellation/connection loss and accepted
source/restore retries. Completed restore receipts follow the 90-day job lifetime.

The retained API and dashboard were rebuilt and restarted with 95 shop traffic
requests and no errors. The owner remained claimed, shop stayed healthy at
revision 18, and cli-check stayed healthy at revision 2. Observed process RSS was
28.6 MiB for Go and 74.6 MiB for Node, with their 192 MiB soft/heap targets.
These are local snapshots, not peak-memory or host-capacity guarantees. One
backup/restore runs at a time and uses a reusable 8 MiB upload buffer.

Large optional workspaces and GPU inference have manifest/configuration review
only. Public provider execution, public S3 compatibility, ACME, physical GPUs,
full host installation and reboot recovery remain separate acceptance gates.
Current visual browser QA remains unavailable because the connector lacks its
authentication token. The UI and private issuer repositories are unchanged;
the public release excludes private signing code and local credentials.

## Historical cockpit and installer expansion — 2026-09-12

This iteration adds signed Free/Pro entitlements, GitLab.com sign-in, GitLab TOML
synchronization and CI builds, bounded SQL-like log search, pod/database terminals,
a separately versioned shadcn/Radix component library, branded service/node
inspectors, and an interactive amd64/arm64 Linux installer. The private issuer
and public UI are separate local Git repositories; no hosted repositories or
production signing keys were created.

Final verification passed:

- The complete Go race suite and `go vet`, using temporary PostgreSQL databases.
  Regressions cover paid-feature expiry/replay, provider ownership/recovery,
  terminal credential isolation, stalled writes, exhausted database pools,
  bounded JSON metadata, cancellation and non-finite numeric comparisons.
- Dashboard type checks, formatting, 19 tests and production builds. Production
  HTTP smoke passed on the final local dashboard and packaged Linux runtime.
  Tailwind scans only application/component source; packaging rejects missing
  SSR stylesheet references.
- A separate real K3s application exercised network isolation, pod replacement,
  rollout, failed-readiness recovery and rollback: 570 traffic requests, zero
  errors. Management was then killed after applying a workload; the same accepted
  operation resumed, with 102 requests and zero errors. Temporary keys were
  revoked. The exact fixture application/namespace was removed after ownership
  and namespace UID checks; audit metadata remains.
- Real container terminals accepted input, resized to 27×88, returned exit 7,
  rejected foreign/stale pods, and stopped on disconnect. Actual Kubernetes logs
  verified CRI timestamps, structured predicates, severity and sampled histograms.
- Fresh installer archives passed extracted-kit tests on native Linux arm64 and
  emulated Linux amd64 in 512 MiB containers: binary execution, systemd unit
  parsing, permissions, dry-run, resume preservation, actual SSR/static assets,
  authentication boundaries and checksums. The dashboard's observed RSS was
  77.4 MiB and 112.4 MiB respectively; these are point measurements.

The running local Go API and Node dashboard were observed at 23.4 MiB and
65.3 MiB RSS after the checks, with 192 MiB soft/heap targets. The development
K3s node, including application pods, used about 760 MiB; PostgreSQL about 60 MiB.
These observations are not peak bounds or minimum host requirements. Logs and
terminals use bounded queues and retention, optional terminal code loads on
connection, and no retained logging or metrics stack was added.

The user claimed the live installation during development. The owner and
credentials were preserved; setup now reports complete. `shop` remains revision
18 and `cli-check` revision 2. Earlier unclaimed-owner statements below describe
historical checks, not the current installation.

Remaining gates are full systemd/K3s host installation, reboot/restore and public
ACME issuance, real external OAuth/repository credentials and CI execution,
physical worker/GPU verification, and current visual browser QA. The browser
connector remains unavailable. GitLab currently owns one reviewed CI entrypoint
per repository and refuses unrelated CI replacement. Log search covers bounded
Kubernetes-retained samples, not an archival logging service. The current local
release runs Free; Pro activation needs a vendor public verification key embedded
in a trusted release. See [licensing](paid-features.md), [GitLab builds](gitlab-builds.md),
[observability](observability.md), and the [installer](../installer/README.md).

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

The [cockpit expansion](cockpit.md) adds installer-defined human ownership, accounts,
teams/project roles, passkeys, TOTP, CLI browser consent, scoped secrets, authenticated
registries, TLS attachment/issuer controls, canonical configuration editing,
GitHub TOML bindings and source builds, templates/persistent workloads, service/pod
monitoring and administrator appearance/proxy controls. External OAuth/GitHub setup,
public DNS/ACME staging/renewal, a verified Ubuntu installer, Infisical integration,
and backup/key lifecycle remain explicit operational gates.

The expanded API passed the real-cluster lifecycle gate: revisions 14–17 covered
deployment, image update, intentionally failed readiness with recovery, and
explicit rollback; 587 public traffic probes recorded zero errors. The updated
management process was then terminated after applying a workload: the same
operation resumed as revision 18, with 100 traffic probes and zero errors.
Temporary deployment keys were revoked and the application finished healthy.

Additional real-cluster checks verified PostgreSQL data survives restart, Valkey
authentication and commands, Uptime Kuma startup, and Gitea administrator setup
and authentication after restart on the same PVC. Gitea's `app.ini` is persistent
and a completed installation stays locked. Uploaded TLS passed a HAProxy handshake;
HAProxy edits passed the generated configuration validator. Fixtures were cleaned.

Human authentication, sessions, roles, invitations, TOTP/recovery, WebAuthn,
OAuth/SMTP fixtures, source repository approval, durable queue recovery and build
artifact verification passed PostgreSQL race tests. An isolated production
dashboard/API additionally passed first-owner setup, login, team management and
cookie/session revocation checks. No owner was created in the live installation.
Visual checks of the expanded browser screens remain unavailable because the
browser connector lacks its authentication token; earlier screenshots are historical.

## Milestone 3: cluster expansion

Dashboard worker enrollment, expiring K3s bootstrap credentials, observed node
resources and bounded cordon/drain controls are implemented. A real temporary tainted
worker joined the named development cluster using a generated short-lived credential
and was cleaned up. Revocation was observed after K3s cache propagation. Physical
Linux workers and control-plane/ingress HA remain separate tests.

## Milestone 4: operational hardening

Architecture/recovery notes, CI, release image and artifact preparation are
present. No production-readiness claim: external backup restoration, upgrades,
HA database/control plane/ingress topology, retained observability, capacity
load testing and host-level failure injection remain acceptance gates.
