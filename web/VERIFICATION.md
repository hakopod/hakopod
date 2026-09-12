# Dashboard and runtime verification

Verified locally on 2026-09-12. This record covers the Hatch migration and the
following account, workspace, runtime and TOML changes. It distinguishes checks
against real services from isolated browser fixtures.

## What changed

The dashboard consumes `@hakopod/hatch-ui` from the public
[Hatch repository](https://github.com/hakopod/hatch-ui), pinned at
`659c6807375ef407149c8f2f7f158238c2350695`. Settings use the open Hatch layout;
project and environment selectors share the header. Navigation and operation
tabs have red active states. Browser appearance supports presets and a custom
accent with readable text contrast.

The project wizard collects a name, stable ID and description, then an
environment and review. The assistant button opens task guidance and links to
existing controls. It does not call an AI model.

TOML editing supports a full-window editor and retains advanced settings when
switching to forms. Application plans include private TCP/UDP ports, peer
allowlists, named and temporary mounts, filesystem ownership and read-only
settings. The operator can load startup configuration from a separate versioned
TOML file with restricted secret-file references.

Signup is opt-in and verifies email. New users choose an invitation or a private
workspace. Password recovery keeps MFA enabled and invalidates browser/CLI
sessions. Paid team and shared-project permissions remain enforced in Go.

## Automated checks

- `go test ./...`, `go vet ./...` and both Go binary builds passed. Database
  acceptance used disposable PostgreSQL databases, not the installation's data.
- Account tests covered signup gates, verification, single-use recovery,
  invitation choices, personal isolation, license restrictions, provider/PKCE
  flows, CLI approval invalidation and bounded mail delivery. SMTP and OAuth
  services in these tests were local fixtures.
- Project tests covered metadata, duplicate-ID safety, legacy environment
  creation, scoped listing beyond 200 unrelated projects and the personal marker.
- Operator tests covered strict TOML fields, environment precedence, invalid
  inputs, secret-file limits and permissions. Application tests covered network,
  mount and filesystem rules, retained fields and shared configuration review.
- Dashboard TypeScript, formatting, production build and all 33 tests passed:
  18 server boundary tests and 15 UI regressions. Tests include account response
  handling, personal/paid permissions, account-switch scope, accent contrast,
  draft preservation, TOML rendering and bounded metric history/freshness.

The unchanged pinned Hatch source previously passed its build, TypeScript and
25 tests. Public source snapshot tests checked deterministic restoration, safe
archive extraction and consumer-only packaging. A clean dashboard copy restored
that snapshot and built without local credentials or private repositories.
Those checks belong to the migration; the UI dependency did not change in this
follow-up.

## Real cluster acceptance

`TestLiveMountsPortsAndPeerPermissions` passed on `k3d-hakopod-dev`. It created an
owned temporary application, checked the behavior, then removed that fixture.
Existing applications and volumes were preserved.

The test verified TCP service port 9090 reaching target 8081, UDP port 9999
reaching target 9998, an allowed peer followed by denied traffic, read-only root
and secondary mount, writable bounded tmpfs, UID/GID and working directory.
Persistent data and the original claim UID survived a subsequent deployment.

ReadWriteMany configuration is validated and requires an explicit capable storage
class. Multi-node CSI sharing was not exercised; the local provisioner supports
ReadWriteOnce. Rollback changes configuration, not stored data.

## Browser checks

The initial migration was checked using real API data in the development
preview: application and service pages, nodes, deployments, metrics, logs,
topology, inspection sheets, catalog and settings. The log query returned real
matching rows. No accounts, keys, provider configuration or infrastructure were
changed for browser QA.

The follow-up used an independent headless Chrome profile and temporary Vite
fixtures rendering the actual components and routes. Each fixture was labelled
as artificial data, intercepted API requests and blocked external traffic.
All 36 browser checks passed with no browser errors. Temporary fixture servers
and browser processes were stopped. Screenshots and reports are kept locally under
`work/ui-migration/operations-fixture/`; they are not part of the product.

Checks covered desktop and mobile Ink/Paper layouts, custom accent persistence,
compact header/settings, project validation and failed-request draft retention,
assistant focus, full-window TOML editing and advanced-field round trips.
Deployment rows, their copy controls and menus operated independently. Log
volume appears above the query controls.

Metric checks distinguished source timestamps from request completion, retained
at most 24 unique ordered samples and showed aging data as stale. Polling paused
when requested, on other service tabs and in a hidden document, then resumed
when appropriate. Hidden-document behavior used a synthetic visibility event
in the isolated browser. A manual probe reads the latest available cluster sample;
it does not force metrics-server to produce a new measurement.

Account checks covered login, optional signup, configured/disabled providers,
recovery, verification links, the first installer screen and invitation/personal
onboarding. Personal users retain deployment controls on Free, cannot share
personal workspaces, and fall back to their own scope after an account switch.

Earlier isolated fixtures also checked build pipelines and rollback retries.
Ambiguous retries retained their original key/revision; definitive conflicts
required a fresh plan. TOML highlighting retained exact canonical text and
bounded colored output without a new dependency.

Live email delivery and GitHub/GitLab/Google client setup were not exercised.
The local installation still has signup disabled and no SMTP or OAuth clients.
Provider, backup, TLS, licensing and terminal operations need their own external
configuration and acceptance. These UI checks do not establish those external
systems are provisioned.

## Resource measurements

These totals cover all emitted client assets, including lazy routes. They are
not initial page transfer sizes. Gzip was calculated separately per file at
level 9; HTTP compression depends on deployment configuration.

| Asset | Files | Raw bytes | Gzip bytes |
| --- | ---: | ---: | ---: |
| JavaScript | 93 | 1,271,646 | 400,477 |
| CSS | 3 | 136,187 | 25,359 |
| Fonts | 2 | 236,032 | 109,697 |

The main JavaScript entry is 377,413 bytes. The 331,178-byte xterm chunk loads only
after Connect. Project creation, appearance and guidance panels load on demand.
This follow-up adds no runtime package dependency. The portable UI source archive
remains 135,846 bytes and contains only the consumer library.

After restart and HTTP smoke checks, a process sample showed 36,848 KiB API RSS
and 103,760 KiB dashboard RSS. These are point-in-time observations, not peak or
capacity guarantees. Go retains its 192 MiB soft memory target, and Node its
192 MiB old-space cap. Neither setting caps total process memory.

Bounds remain: 25 applications per page, 24 metric samples, at most 1,000 matched
log entries, live logs limited to 1,000 lines/256 KiB, 500 terminal scrollback lines
and a 16 KiB input queue. Service metrics poll every 15 seconds only in the active
foreground view, with cancellation on exit. TOML highlighting scans at most
65,536 UTF-16 units and emits at most 512 colored spans, keeping the complete
remaining source visible without highlighting.

The restarted production preview returned HTTP 200 for its account pages,
settings and proxied auth status. Existing administrator setup remained complete.
See [the dashboard README](README.md#authentication-and-limits) for further limits.
Long-running browser memory and large-cluster load tests were not performed.
