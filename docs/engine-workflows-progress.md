# Engine workflows validation

Scope: automatic framework setup, coding-agent integration, scheduled jobs,
preview environments, automatic release recovery and build secrets.

## Implementation

- Framework detection for Astro, Next.js, SvelteKit, TanStack Start, Vite, Node and HTML;
  generated digest-pinned static/Node recipes; explicit dashboard review.
- BuildKit secret mounts using CI secret references; no secret values in configuration.
- Scoped stdio MCP tools, read-only by default, optional deployment of expiring reviewed plans.
- Five-field cron schedules with IANA timezone, bounded retries/history, no overlapping runs,
  pause/resume and actual run history.
- Isolated image-based previews, 1–72-hour lifetime, three per project, bounded resources,
  durable cleanup, ownership checks, no production secret/data inheritance.
- Durable safe release recovery. This preserves existing stateless recovery by default;
  jobs, volumes and service additions/removals require review.
- Cloud owner-only preview management through scoped node credentials. Workspace members can
  inspect previews. Limits remain enforced by the canonical engine on every revision.

## Verification on September 15, 2026

- Full Go suite passed with disposable PostgreSQL; go vet passed.
- Shared dashboard tests, typecheck and production build passed. The composed Cloud
  dashboard also passed tests, typecheck and build.
- Real generated Docker builds served Astro, Next.js, SvelteKit and TanStack Start.
  Plain HTML/BuildKit-secret runtime acceptance passed after fixing static COPY ownership.
- Real named-cluster schedule test passed: creation, concurrency prevention, pause, resume,
  failure observation and removal. API tests verify digest/schedule retention through actions.
- Real named-cluster preview test passed: foreign namespace ownership is rejected; retained
  backing storage, PVCs, namespace and native secrets are removed; cleanup is idempotent.
  Unit tests additionally reject foreign claims, foreign PVs and changed cleanup markers.
- Real named-cluster recovery test passed: failed revision remains failed and prior workload
  becomes healthy. Durable PostgreSQL restart/reclaim and cancellation tests passed.
- Source approval, preview expiry/fair cleanup, backup denial and scoped gateway tests passed.
- Dashboard API forwarding tests passed, including previews, detection, session/CSRF and
  exact endpoint boundaries. OpenAPI and dashboard types were regenerated.
- Independent rendered UI review approved: 100 baseline combinations and 38 targeted
  checks, both themes, desktop/mobile, keyboard/touch and short landscape. See
  [the review report](engine-workflows-ui-review.md) for synthetic-fixture limits.

Live Kubernetes tests use only the named `k3d-hakopod-dev` cluster and disposable
acceptance applications. An initial final-preview retry failed because the local
node had real disk pressure. Clearing unused Docker build cache restored capacity;
the passing retry did not bypass the node's scheduling protection.

## Current limits

Preview creation accepts already-built images. It does not automatically subscribe
an application to GitHub pull-request or GitLab merge-request events. CI may call
the CLI/API; expiry starts cleanup even if the CI close event is missed.

MCP is usable with scoped credentials against a self-hosted API. Cloud account
device login and scoped Cloud automation credentials are not implemented. The
workspace header supports existing Cloud bearer-session integrations; it does not
make the private enrolled-worker API public or grant new access.

This is source and acceptance evidence. Production publication and Azure rollout
must be verified separately against the exact Cloud artifact and engine pin.
