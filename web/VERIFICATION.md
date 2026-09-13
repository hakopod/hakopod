# Dashboard and runtime verification

Verified locally on 2026-09-12 and 2026-09-13. This record covers the Hatch
migration, account and runtime changes, compact dashboard, and virtual networks.
It distinguishes checks against real services from isolated browser fixtures.

## September 13 layout and service settings follow-up

Application lists now have no outer box or resting card outlines. Summary counts
sit on one compact row and wrap on small screens. The heading actions use the
same Button component. Application and service grids use explicit responsive
columns, with four on desktop and five from 1680 pixels upward.

Sixteen isolated browser checks passed at widths from 320 to 2560 pixels with
no browser errors. Both grids used 1/1/2/4/5/5 columns at
320/390/768/1447/1800/2560 pixels. The summary measured 28 pixels on desktop and
46 pixels when wrapped on mobile. Both action buttons measured 32 pixels on
desktop and 36 pixels on mobile, with matching top alignment. Two-card lists
kept their column widths. Native links, copy buttons, action menus and keyboard
focus remained independent. The fixture used artificial records and intercepted
requests; it did not change live applications.

Services now have direct Environment and Secrets tabs with persistent URLs.
Environment shows only the selected service's plain values and opens its
reviewed editor. Secrets lists its bound references and saved metadata, groups
aliases, and names other services sharing a reference before a value is changed.
Values remain write-only. New secret values require a separate reviewed binding
change; saving a value does not deploy or attach it automatically. Viewer roles
have no write controls. Secret dialogs block dismissal during saving and preserve
failed drafts. Name validation matches the Go slug rules, including Chrome's
current HTML pattern syntax.

Fourteen isolated service checks passed without browser errors or external
requests. They verified service context through editing and reloads, exact
reviewed binding payloads, shared and missing references, write-only saves,
invalid names, metadata failures, viewer controls and retained failed drafts.
Long values, names and dialogs fit a 390-pixel viewport without page overflow.
Both the service and application secret dialogs resisted dismissal while saving.

The full Go suite passed again with two workers and disposable test databases.
Dashboard TypeScript, formatting, all 43 tests and the production build passed.
No backend or Kubernetes behavior changed in this follow-up.

The refreshed production preview returned HTTP 200 for applications, both new
service tab URLs, settings and proxied auth status. The existing administrator
setup and both application revisions were preserved. These HTTP checks verify
route availability; the isolated browser checks verify interactive behavior.

The checklist is in [dashboard feedback](../docs/dashboard-checklist.md). The
sections below also retain evidence from earlier feature and migration passes.

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

The compact follow-up merges desktop navigation into a 56-pixel header, narrows
scope selectors, and keeps the application heading and its actions on one row.
Application cards use native links with independent copy and menu controls. Provider cards
share borders and guidance actions have horizontal padding. Secondary page help
is available from a keyboard-accessible tooltip.

Node inspection adds blue CPU and purple memory charts with readable Ink/Paper
contrast. It reuses the node query, checks every 15 seconds while inspecting or
30 seconds on the list, and offers pause and manual probe controls. History is
limited to 24 actual source samples and clears when inspection closes.

HAProxy now exposes 20 reviewed settings, including backend check/queue/FIN
timeouts, drain deadlines, load balancing and connection behavior. The editor
submits only changed fields. Go validates bounds and choices, preserves unrelated
configuration, and retains authorization, revision checks and audit records.

The latest pass uses one `SelectField` API throughout the dashboard. It preserves
empty/disabled choices, native required validation and keyboard interaction.
Nested-page parent links sit before the logo. Application and service headings
avoid repeated scope, cards match the catalog, deployment events use consistent
spacing, and service charts share the node resource component.

Project administrators can add environments from the header. Service environment
editing separates ordinary values from secret references, merges unrelated
concurrent edits and requires explicit conflict resolution. Failed requests keep
the draft. Catalog setup now guides required variables and credentials using a
[review of all 14 upstream templates](../docs/template-requirements.md).

[Virtual networks](../docs/virtual-networks.md) provide environment-scoped shared
segments, application grants, service membership and private peer rules. Forms
and TOML use the same Go validation, revision checks and audit records. Updates
and deletes also check network identity to reject stale reviews after recreation.

## Automated checks

- `go test ./...`, `go vet ./...` and both Go binary builds passed. Database
  acceptance used disposable PostgreSQL databases, not the installation's data.
- Account tests covered signup gates, verification, single-use recovery,
  invitation choices, personal isolation, license restrictions, provider/PKCE
  flows, CLI approval invalidation and bounded mail delivery. SMTP and OAuth
  services in these tests were local fixtures.
- Project tests covered metadata, duplicate-ID safety, legacy environment
  creation, scoped listing beyond 200 unrelated projects and the personal marker.
  New environment tests covered project-admin authority, cross-project denial,
  duplicates, capacity bounds and audit records.
- Operator tests covered strict TOML fields, environment precedence, invalid
  inputs, secret-file limits and permissions. Application tests covered network,
  mount and filesystem rules, retained fields and shared configuration review.
- Dashboard TypeScript, formatting, production build and all 43 tests passed:
  18 server boundary tests and 25 UI regressions. Tests include account response
  handling, personal/paid permissions, account-switch scope, accent contrast,
  draft preservation, TOML rendering, SelectField semantics, parent navigation,
  environment merging, network connection preservation and bounded metrics.
- Network database/API tests cover grant scope, queued/partial deployments,
  successful detach, recreated identities, stale reviews and bounded connection
  metadata without environment values. Focused network and shared runtime tests
  also passed with the race detector.
- Template tests cover credential formats, generation without replacement,
  certificate/key matching, derived URL/password consistency and reviewed
  deployment acceptance. Showcase cancellation now checks retry deadlines using
  PostgreSQL's clock; immediate cancellation and retry tests passed.
- The three repository script tests and nine installer tests passed; generated
  API contract modules passed Python syntax checks.

The unchanged pinned Hatch source previously passed its build, TypeScript and
25 tests. Public source snapshot tests checked deterministic restoration, safe
archive extraction and consumer-only packaging. A clean dashboard copy restored
that snapshot and built without local credentials or private repositories.
Those checks belong to the migration; the UI dependency did not change in this
follow-up. The dashboard now declares the existing Radix Select version directly.

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

The September 13 HAProxy acceptance ran on the same development cluster with
controller 3.2.15 (chart 1.54.0) and HAProxy 3.2.23. It verified all 12 newly
supported properties in the generated HAProxy configuration, checked the result
with `haproxy -c`, restored the original ConfigMap data and annotations, and
verified the restored generated configuration. No test settings remain applied.

`TestLiveVirtualNetworksAcrossApplications` passed in 29.79 seconds on the same
development cluster. Four isolated applications verified cross-application DNS,
private TCP/UDP port mappings, denied unlisted peers, denied other segments and
environments, and removal of peer permission. Owned fixture namespaces were
cleaned up. This is IPv4 K3s NetworkPolicy acceptance; it does not establish
cross-cluster routing, traffic encryption, subnet allocation or other CNI behavior.

## Browser checks

The initial migration was checked using real API data in the development
preview: application and service pages, nodes, deployments, metrics, logs,
topology, inspection sheets, catalog and settings. The log query returned real
matching rows. No accounts, keys, provider configuration or infrastructure were
changed for browser QA.

The follow-up used an independent headless Chrome profile and temporary Vite
fixtures rendering the actual components and routes. Each fixture was labelled
as artificial data, intercepted API requests and blocked external traffic.
All 36 checks from the September 12 follow-up passed with no browser errors. Temporary fixture servers
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

The September 13 compactness suite passed 17 browser checks with no browser
errors. It covered header widths from 390 to 1792 pixels, single-row application
headings, whole-row navigation, separate copy controls, contiguous provider cards,
guidance padding and keyboard focus. Node checks covered Ink/Paper contrast,
pause/resume, source timestamps, the 24-sample cap, staleness, unavailable values,
failed probes, slower list polling and cancellation on hide or tab exit. HAProxy
checks verified changed-field-only requests, invalid-value rejection and failed
draft retention. This suite used isolated fixtures, not live configuration edits.

Earlier isolated fixtures also checked build pipelines and rollback retries.
Ambiguous retries retained their original key/revision; definitive conflicts
required a fresh plan. TOML highlighting retained exact canonical text and
bounded colored output without a new dependency.

The latest isolated dashboard suite passed 26 browser checks: header widths
320/768/1280/1342, keyboard and required-field focus, empty/disabled options,
FormData, scope switching, environment permissions/retries and eight parent
routes. The operations suite passed 23 checks covering cards and separate
actions, service chart colors/cancellation, environment merge conflicts, network
TOML/forms, reviewed retries and explicit reload after network recreation.
Both used disposable Chrome profiles with external requests blocked and no
live-account or configuration writes. Their fixture servers and browsers closed.

Fourteen additional catalog browser checks used the actual template components
and Go-generated plans with an intercepted credential API. They covered required
references, invalid/failed draft retention, saved-secret reuse, explicit
replacement, generated connection URLs, PEM limits, provider-supplied keys,
exact reviewed deployment payloads, retries and a 390-pixel layout. No real
credentials or deployments were created by these fixtures. Review hides the
configuration help sidebar to avoid repeating guidance already present in the
warnings. All warning text remains in one compact list, and required secrets
appear before the full deployment diff. The fixture server, exporter and browser
processes were removed.

Live email delivery and GitHub/GitLab/Google client setup were not exercised.
The local installation still has signup disabled and no SMTP or OAuth clients.
Provider, backup, TLS, licensing and terminal operations need their own external
configuration and acceptance. These UI checks do not establish those external
systems are provisioned.

## Resource measurements

These totals cover emitted JavaScript, CSS and fonts, including lazy routes. They
are not initial page transfer sizes. Gzip was calculated separately per file at
level 9; HTTP compression depends on deployment configuration.

| Asset | Files | Raw bytes | Gzip bytes |
| --- | ---: | ---: | ---: |
| JavaScript | 105 | 1,358,114 | 431,342 |
| CSS | 3 | 148,913 | 27,395 |
| Fonts | 2 | 236,032 | 109,697 |

The main JavaScript entry is 401,703 bytes. The 331,178-byte xterm chunk loads only
after Connect. Project creation, appearance and guidance panels load on demand.
Network routes, environment editors and the new service secrets panel also load
separately. The secrets panel is 5.68 kB raw, 2.44 kB gzipped, and adds no polling.
This pass adds no
chart library, network daemon or model runtime. The portable UI source archive
remains 135,846 bytes and contains only the consumer library.

After restart and HTTP smoke checks, a process sample showed 26,800 KiB API RSS
and 109,952 KiB dashboard RSS. These are point-in-time observations, not peak or
capacity guarantees. Go retains its 192 MiB soft memory target, and Node its
192 MiB old-space cap. Neither setting caps total process memory.

Bounds remain: 25 applications per page, 24 metric samples, at most 1,000 matched
log entries, live logs limited to 1,000 lines/256 KiB, 500 terminal scrollback lines
and a 16 KiB input queue. Service metrics poll every 15 seconds only in the active
foreground view, with cancellation on exit. TOML highlighting scans at most
65,536 UTF-16 units and emits at most 512 colored spans, keeping the complete
remaining source visible without highlighting.

The restarted production preview returned HTTP 200 for applications, networks,
network creation, catalog, settings and proxied auth status. Existing
administrator setup and both application revisions remained unchanged.
See [the dashboard README](README.md#authentication-and-limits) for further limits.
Long-running browser memory and large-cluster load tests were not performed.
