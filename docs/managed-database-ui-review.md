# Managed database cockpit review

Date: 29 September 2026. Review status: independent sign-off for the bounded
PostgreSQL/Redis cockpit and topology changes described below.

This record covers the PostgreSQL and Redis catalog, six database detail tabs,
application-aware topology, resource monitoring and placement controls. It does
not claim support for the planned engines, public endpoints or additional TLS
guarantees in the [platform expansion plan](managed-database-platform-plan.md).

## Data and behavior

Applications appear from saved bindings, the last successful deployment and the
latest attempted deployment. These are configuration records, not detected live
sessions. Manually entered connection credentials are not discoverable. Failed
rollouts retain their historical binding evidence. The API enforces database
scope, excludes application-scoped keys, returns no credential/configuration
bodies and caps responses at 250 binding records with a truncation flag.

The graph separates applications, private endpoint routes and observed database
members. Search and pages of 15 applications keep labels readable. All observed
members in the selected shard remain available. Selection, keyboard focus and
Locate selected reveal the chosen node inside the scrollable canvas.

The 15-application, primary-plus-six-replica screenshot is explicitly artificial
development data. It tests UI density with six replicas, now supported by
admission. The screenshots themselves do not demonstrate provisioning or live
application traffic; separate development-cluster tests provide runtime evidence. A separate fixture covers pagination across 35 applications;
layout tests also retain 48 members.

## Rendered coverage

The VM-built fixture uses the actual dashboard routes and shared shell. Every
fixture page has an artificial-data banner. The independent reviewer inspects
saved screenshots and source; browser interaction and bounds are captured by the
implementing agent because the reviewer's browser connection is unavailable.

| Area | Evidence and result |
| --- | --- |
| Detail tabs | Overview, Monitoring, Connections & security, Backups, Activity and Settings rendered in both themes at measured 1483px and 389px: 24 loaded cases without document overflow. |
| Dense topology | 15 applications, two private endpoints and seven database members retained with separate labels; selection, inspector and application binding routes inspected. |
| Keyboard help | Tab opens the named topology help with an associated description; Escape dismisses and Space reopens it within the measured 389px viewport. |
| Narrow interaction | Pagination, replica selection, filtering after replica selection and locating the already-selected application checked at measured 358px. Selected-node bounds stay inside the canvas. |
| Redis | Standalone and cluster views; shard selection, unknown-role rendering and stale slot coverage inspected. |
| Monitoring | Current CPU/memory samples, bounded session history, two desktop columns and mobile reflow; unavailable/stale samples remain explicit. |
| Catalog | Loaded PostgreSQL/Redis icons; stale readiness says last known. |
| Failure states | Missing observed endpoint, unavailable connection records, credential request failure, Activity and Backups request failures. Unavailable counts do not become zero. |
| Nested pages | New and cluster placement, connect, and resize rejection in both themes on desktop/mobile. Rejected resize preserves the edited 500m CPU value. Recovery covers the ineligible-target guard; import covers missing eligible S3 destination. |

Original screenshots and JSON measurements are retained under
`work/database-cockpit-review/evidence/`; `final-evidence-index.md` identifies
accepted replacements and historical captures. Initial screenshots with unloaded
icons, old monitoring layouts and historical records marked `loaded: false` are
not final evidence. Earlier filenames labeled 320px actually measured 358px;
320px and wide-display screenshot review are unverified. The private Cloud
dashboard overlay, successful recovery/import submission flows, a comprehensive
loading/empty/viewer/degraded/long-value matrix and real cross-provider
availability are not covered by this UI review. The implementing agent checked
keyboard help; the independent reviewer did not inspect that additional screenshot.

## Verification

- Full `go test ./...` passed on the VM with disposable PostgreSQL, including
  connection scope/history/bounds and resize-review placement identity tests.
- Dashboard typecheck and production build passed on the VM. All 49 server tests
  and 98 UI tests passed.
- Named `k3d-hakopod-dev` development-cluster acceptance passed PostgreSQL placement
  across two development-labeled scheduling zones, complete real member CPU and
  memory samples, and recovery after deleting the primary while retaining a test
  row. PostgreSQL standalone and Redis standalone/six-member cluster acceptance
  passed earlier in this work.
- The two scheduling zones are labels on nodes hosted by one VM. They prove
  scheduling separation, not availability across physical cloud zones.
- No commit, push, pull request, production deployment or public endpoint was
  created by this review.

## Expanded engine review, 29 September

Independent reviewers subsequently inspected the ClickHouse and Oracle preview
using the real dashboard components with clearly marked artificial data. Their
records are retained locally in
`work/database-enterprise/clickhouse-oracle-ui-review.md` and
`work/database-enterprise/database-interaction-review.md`.

The supplied screenshots cover both themes and desktop/mobile layouts for
overview, monitoring, connections/security and backups; populated backup records;
pending/stale security and viewer states; member storage inspection; immutable
capacity; and selected creation/review steps. The dense ClickHouse fixture has
15 applications, two shards with six copies each, and three Keepers. The Oracle
fixture has 15 applications and one Free instance. Edition copy distinguishes
proprietary Free from unavailable customer-licensed Enterprise/Data Guard.

The interaction follow-up verified matching and empty searches, shard and member
selection, Locate selected after horizontal panning, and real Tab navigation that
reveals an offscreen application inside the canvas. It also verified keyboard
help and Escape dismissal, readable mobile help, and rejected connection forms
in both themes. Rejection preserves the entered values and review, focuses the
visible alert, and lets the next Tab reach the retry action.

No new actionable UI defect remained in those accepted captures. This is not
overall UI or production sign-off. Touch dispatch was unavailable in the browser
tool; physical/emulated touch and internal vertical canvas scrolling remain
unverified. Not every creation step was rendered in every theme/viewport.
Compositor padding and repeated fragments in responsive full-page captures are
excluded from product geometry. One mobile focus record had a one-pixel body
width discrepancy at 67 percent browser zoom, so it does not establish clean
document overflow. The local reports retain the exact accepted records and
exclusions. Native runtime acceptance is recorded separately in the engine guides.

## Release review, 2 October 2026

An independent reviewer rendered final dashboard source `c0bf711` on the
isolated Regular development VM. All records were marked as artificial fixture
data. This review did not contact a production API, expose credentials or
qualify a database runtime.

The database review recorded 110 cases: 54 dark-theme renders, 54 Paper-theme
renders, a keyboard-focus check and an emulated-touch check. The matrix covered
320, 390 and 1440 pixel widths for the populated list, unavailable scope, all
six shipped creation guides, and dense PostgreSQL, Redis, MySQL, MongoDB,
ClickHouse and Oracle Database Free details. All six detail tabs rendered at
390 and 1440 pixels in both themes. The dense PostgreSQL fixture contained 15
application bindings, one primary and six replicas. Every case had one page
heading, no document overflow and no browser console or page error. The skip
link received a visible two-pixel outline, and touch selection changed the
guided engine.

The managed-platform review recorded 60 more renders. It covered blocked,
ready, rejected, expired, unavailable-catalog, empty, missing-input, viewer and
application-key creation states in both themes at 390 and 1440 pixels. Platform
lists and ready, pending and failed details were checked at 320, 390 and 1440
pixels, with missing scope at 390 pixels. All cases loaded without document
overflow or browser errors.

The reviewer inspected full-page captures, including corrected Paper-theme
captures after rejecting an initial reviewer URL that left the fixture in dark
mode. Desktop and mobile catalog cards remained aligned, selected tabs stayed
visible in the horizontal mobile tab row, topology labels remained readable in
the horizontal canvas, and 320-pixel guided forms retained usable controls and
summaries. No actionable UI defect remained. Evidence is retained on the review
VM under `ui-release-c0bf711/reviewer/database-review/` and
`ui-release-c0bf711/reviewer/review/` within the owned scratch directory.

## Vitess release review, 5 October 2026

An independent reviewer checked the Vitess dashboard at source
`b2472bdbc19013dbe8a749921cf92b7b72f5f44a`. All records were explicitly marked as
artificial fixtures. The review covered 54 route cases in dark and Paper themes
at 1440, 390 and 320 pixels: standalone and clustered details, monitoring,
connections and security, backups, recovery, dense topology and all five creation
steps. Four separate checks covered keyboard focus, mobile tab scrolling, touch
selection, recovery eligibility and corrected singular tablet wording.

The dense fixture contained eight shards, five replicas per shard, 48 tablets,
15 applications, two vtgate gateways and three etcd members. The graph showed
the selected shard while the inventory retained all 48 tablets. The creation
form displayed only the native backup destination in the selected project and
environment. A failed request preserved the name, topology, destination revision
and table-routing entry. Recovery displayed only the compatible verified archive.

The reviewer inspected full-resolution screenshots after checking real element
bounds. Controls remained inside the viewport, keyboard focus stayed visible,
and the mobile tab row and topology canvas scrolled within their own containers.
Every route retained the shared 24-pixel desktop or 16-pixel mobile inset, one
page heading and no document-level horizontal overflow or browser error. No
actionable visual finding remained after the singular tablet copy was fixed.
The same VM source passed 65 server/proxy tests, 218 UI tests, the production
build and TypeScript checks.

A later capacity correction at `815949a341f6588b70630ab36701b1dab329d70f` passed
24 additional rendered cases covering allocation, deployment review and details
for both layouts and themes at 1440 and 320 pixels. The standalone total was
5.1 CPU cores and 9.656 GiB; the two-shard, one-replica cluster total was 8.85 CPU
cores and 17.248 GiB. A final copy correction at
`255479be4954af74fb34871f42c0508a6f7071b5` passed eight screenshot cases for one
and two table-routing entries at those widths in both themes.

The release candidate `6e7028e02c52ffb1c982bd421c5a78d249d93d7d` retains those
rendered dashboard bytes. Its later web changes affect generated API metadata
and the server proxy, not the rendered components. The retained evidence is in
`work/database-enterprise/vitess-ui-review-v1/`, including the reviewer notes,
measured results, interaction results, screenshots and SHA-256 lists for the
main, capacity and copy reviews. VM preview processes were stopped after export.
These reviews establish the stated UI coverage; native database acceptance and
Cloud or production availability require separate evidence.
