# Dashboard verification

Verified locally on 2026-09-12 for the Hatch migration. This record describes the
checks performed, rather than treating every implemented feature as verified.

## What changed

The dashboard now consumes `@hakopod/hatch-ui` from the public
[Hatch repository](https://github.com/hakopod/hatch-ui), pinned at
`659c6807375ef407149c8f2f7f158238c2350695`. The console uses a top navigation bar,
Ink/Paper themes, Ion actions, Forest state indicators, self-hosted fonts, dense
operation tables, inspection sheets and grouped settings. Longer forms remain
on nested pages. Destructive account, access and configuration actions require
typed confirmation.

This migration changes presentation and browser interactions. It does not change
Go authorization, reconciliation, database schemas or Kubernetes operations.

## Automated checks

- Dashboard TypeScript, formatting and production build passed.
- Dashboard tests passed: 16 server boundary/stream tests and four existing
  regressions covering copy-button form safety, canonical configuration, project
  membership roles and host-terminal authority.
- Three TOML rendering regressions also passed: exact text/escaping, string and
  comment boundaries, and bounded highlighting of oversized or nested input.
- Hatch at the pinned commit passed its build, TypeScript and all 25 tests.
- Public UI snapshot tests passed: deterministic restoration, safe archive
  extraction and consumer-only packaging. The snapshot contains 47 files and
  excludes the Hatch documentation site, example dashboard and dependencies.
- A clean dashboard copy, without local credentials or installed dependencies,
  restored the shipped snapshot, installed from the frozen lockfile offline,
  typechecked and built successfully. Public builds do not need private repos.
- `go test ./...` passed. Kubernetes behavior was not changed in this migration.

## Browser checks

The production preview used the existing development installation and real API
data. Checks covered applications, service details, nodes, deployment stages and
events, resource metrics, topology, log queries, pod/node inspection sheets,
catalog requirements, settings and source-provider forms. No management mutation,
account creation, provider write or cloud provisioning was submitted for UI QA.

At desktop and 390px mobile widths, light and dark views were inspected. Mobile
application, service and node pages stayed within the viewport. Login was checked
in both themes at desktop and mobile sizes without submitting credentials.
Preferences, command search and mobile navigation were keyboard-tested: closing
a dialog returns focus, arrow keys select results, and the command shortcut does
not open another dialog over an existing one.

A real log histogram bucket returned 40 matching rows. Search narrowed the
application list correctly. Live logs were started and paused again. Terminal
setup was inspected without starting a shell. An incorrect API-key confirmation
kept revocation disabled; the dialog was cancelled and the key remained intact.

The development installation had no build configurations. A separate temporary
fixture harness rendered the actual build route with queued, running, failed
and successful API-shaped data. Configuration, pipeline stage selection, long
digests and paths passed dark/light checks at 1280px and 390px widths. Mobile
pipeline overflow remained inside its own scroll container. The harness
intercepted all API calls; no fixture was added to the running installation.

The same isolated harness checked rollback retries using the actual application
route. After a lost response and an application refresh from r2 to r3, retry
retained the original key and expected revision. After a definitive HTTP 409,
another attempt used a new key and the refreshed revision. Both successes
navigated to the returned deployment. Temporary servers were stopped afterward.

The applied configuration now uses lightweight TOML syntax highlighting and its
Deploy changes action uses a red accent. Both were visually checked in Ink and
Paper using the actual components in a labelled static fixture. Copy and Export
still use the canonical source text. Highlighting scans at most 65,536 UTF-16
units and emits at most 512 colored spans; the complete remaining text stays
visible without highlighting. It adds no runtime dependency.

Live GitHub/GitLab workflow execution, remote build cancellation and artifact
deployment were not exercised. Provider, backup, TLS, licensing and terminal
operations require their own integration acceptance; rendering their controls
does not establish that those external systems are configured or working.

## Resource measurements

The following measurements cover all emitted client assets, including lazy
routes. They are not initial page transfer sizes. Gzip was calculated separately
per file at level 9; actual HTTP compression depends on deployment configuration.

| Asset | Files | Raw bytes | Gzip bytes |
| --- | ---: | ---: | ---: |
| JavaScript | 84 | 1,237,861 | 388,677 |
| CSS | 2 | 125,378 | 23,285 |
| Fonts | 2 | 236,032 | 109,697 |

The main JavaScript entry is 369,561 bytes. The 331,178-byte xterm chunk loads only
after Connect. The portable UI source archive is 135,846 bytes.

One production preview RSS sample was 65,920 KiB after browser QA. This is a
point-in-time observation, not a peak or capacity guarantee. Node retains its
192 MiB old-space cap, which does not cap total process memory.

Existing bounds remain: 25 applications per page, 24 metric samples, at most
1,000 matched log entries, live logs limited to 1,000 lines/256 KiB, 500 terminal
scrollback lines and a 16 KiB input queue. Route splitting, on-demand panels,
bounded history/query caches and hidden-tab polling suspension remain in place.
See [the dashboard README](README.md#authentication-and-limits) for the complete
limits and refresh intervals. Long-running browser memory and large-cluster
load tests were not performed in this migration.
