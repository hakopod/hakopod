# SQL query dashboard review

Independent review completed on 2026-10-08 using the standing [UI checklist](ui-ux-checklist.md). Scope: the new SQL query route, database detail SQL action, and their shared shell, FormPage, SelectField, help and result-table behavior.

The review used the actual product route modules through the explicitly labelled DEVELOPMENT ONLY fixture in `web/review/agent-query`. Vite ran on the VM with bounded resources and was forwarded to local port 14199. No local build ran. All API responses were artificial and intercepted; this evidence does not qualify SQL execution, Kubernetes state, authorization enforcement or production deployment.

## Checked behavior

- Source inspection covered both changed route modules and shared layout, scope, controls, help and query refresh behavior.
- Loaded form, write review, result and database detail screenshots were inspected in dark and Paper themes at 1484 × 1000 desktop and 390 × 844 mobile. Font loading and expected route controls were checked before judging captures. The early detail captures with an incomplete connections fixture were replaced after the fixture gained that response.
- Actual heading and page bounds showed the shared 24px desktop and 16px mobile inset, full-width heading divider and no duplicate page inset or width cap. Mode and maximum-row controls aligned, measured 48px high, and matched widths on desktop. Mobile execute/edit actions measured 40px high and wrapped without clipping. Detail navigation siblings aligned at 32px high. No document overflow appeared in the measured cases.
- Keyboard mode selection, Tab focus on controls and result scroller, review focus, Escape help dismissal and visible focus outlines worked. Mobile pointer activation opened help inside the viewport. This exercised touch-style activation; physical touchscreen and software-keyboard behavior were not tested.
- SQL and parameter values survived cancel, failed read requests, unknown API write outcomes and interrupted write connections. Unknown outcomes removed the execute confirmation and required a new review. The error received focus and remained visible.
- Results preserved SQL null and the exact text of a 25-digit integer. Long text stayed within the bounded result scroller. Truncation warnings, empty result copy, permission denial, unsupported engines and explicit loading state were inspected. These additional state checks were representative, not a complete four-way theme/viewport matrix for every error state.
- The scoped SQL link on database detail navigated to the loaded database's project/environment. The write-only artificial credential opened in the permitted write mode after the fix.
- The artificial submitted request contained `read_only: false`, `max_rows: 100`, `max_bytes: 262144`, and `expected_revision: 4`, matching the reviewed target revision. The fixture request ledger had no blocked requests for this query execution.

## Resolved findings

The implementation now labels prior evidence as “Last query outcome” and provides an “Executed statement” disclosure, so editing a new statement does not misrepresent the old result. Row counts use singular copy correctly. Unknown write outcomes and network interruptions clear the old write confirmation. A write-only credential selects its permitted mode rather than presenting an unexplained disabled read action.

## Evidence and limits

Local screenshots are in `work/ui-review/agent-query/`: `desktop-dark-form.jpg`, `desktop-paper-form.jpg`, `desktop-dark-result.jpg`, `mobile-dark-form.jpg`, `mobile-dark-write-review.jpg`, `mobile-dark-result.jpg`, `mobile-paper-result.jpg`, the four `*-detail.jpg` files, `mobile-paper-unknown.jpg`, `mobile-paper-executed-statement.jpg`, `mobile-paper-denied.jpg`, `mobile-paper-unsupported.jpg`, `desktop-dark-failed.jpg`, `desktop-dark-network-unknown.jpg`, `desktop-dark-empty.jpg` and `desktop-dark-loading.jpg`. Additional initial dark/Paper form, review, help and truncated-result screenshots are in the browser tool transcript. `desktop-dark-write-only-before-fix.jpg` is diagnostic evidence only.

No unresolved finding remains in the reviewed UI scope. Live database queries, backend role changes, stale-revision rejection, held-request double-submission, physical touch, narrow 320px layouts and other database tabs were not newly qualified by this visual pass. The revision guard and request lock were source-inspected; runtime acceptance belongs to the backend/cluster verification record.
