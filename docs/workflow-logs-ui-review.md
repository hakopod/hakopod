# Managed Actions workflow logs UI review — 2026-09-29

The independent reviewer signs off on the workflow activity UI after source inspection, VM browser interaction checks, measured element bounds and critical screenshot review. All findings in the changed UI were resolved. This is a scoped UI approval; GitHub runner execution, backend authorization and deployment acceptance require their separate evidence.

## Method and scope

The isolated fixture imports the actual application route, `ServiceDetail`, `ManagedActionsStatus`, `ManagedActionsWorkflows`, shared controls and dashboard styles. It runs on the Cloud VM in `/srv/hakopod-backup-scratch/workflow-logs-20260929/ui-review`, using loopback port 14539 and existing Node/Playwright/Chromium dependencies. The fixture visibly says “UI fixture · artificial data”, intercepts every API request and blocks external network requests. No production API, account, credential or workload was read or changed.

Self-hosted and Cloud edition attributes and the shared edition flag were rendered. The fixture shell uses the existing global-header, page-content and footer classes with a minimal artificial header. Full authenticated navigation, private Cloud workspace routing, live entitlements, GitHub API behavior, log completeness and runner lifecycle are outside this visual sign-off. The existing API response contracts were represented by synthetic records.

All compilation and browser execution took place on the VM. Local work was limited to source preparation, reading results and inspecting copied screenshots. The original service-resize harness was only read. Review helpers and evidence are owned by this task. The browser processes and owned loopback fixture server were stopped after review; `server-stop.json` records port 14539 closed.

## Completed coverage

The main suite recorded 78 screenshots and six additional interaction/paging results. A focused follow-up recorded eight screenshots plus six keyboard-outline and content-bound measurements. Two additional captures verify run-attempt reload and runner filtering, for 88 screenshots in total. There were no final document-overflow, clipped-control, console or font-loading findings. The four initial loading/error-selector screenshots in the main suite are superseded by the `final-state-copy` captures.

| Coverage | Viewports and themes | Result |
| --- | --- | --- |
| Actual service Actions route with pool counts, run header, jobs, steps and logs | 1440, 390 and 320; dark and Paper; self-hosted and Cloud | Passed. |
| Step expansion, search/no matches, wrap, timestamps, download, job selection, URL Back and follow stopping on scroll | 390; both themes and editions | Passed. Synthetic HTML remains literal text; ANSI controls are removed. |
| Empty, pool error, history error, log error, waiting, provider permission failure, expired/unavailable logs, stale observations, denied log access, failed job, unconfirmed job and loading | 320; dark and Paper | Passed. Denied log access sends no log request. |
| Long workflow, branch, matrix-job and step names; 3,005-line log window | 320; dark and Paper | Passed. Every loaded window is reachable, including a sentinel in the middle; dropdown Escape restores focus. |
| Missing job selection and recovery | 320; dark and Paper | Passed. No silent fallback to another selected job. |
| Ordinary-service Actions deep link, managed-pool Overview entry and application-level removed-pool cleanup | 1440, 390 and 320; dark and Paper | Passed. Ordinary services display Overview; removed pools preserve cleanup guidance without an activity link. |
| Workflow attempt selection, reload and runner filtering | 320; dark and Paper | Passed. Attempt identity survives reload; choosing a runner clears an incompatible run and shows its assigned job. |
| Desktop job keyboard order/Enter and mobile select Space/End/Enter; pending/error selector text | 1440 and 320; dark and Paper | Passed. |

All ten main contact sheets and the focused contact sheet were inspected. Full-resolution inspection included desktop self-hosted/Cloud, narrow Paper, expanded step output, focused search, long names/log paging and final keyboard/state captures. The two final runner-filter captures were inspected at full resolution.

## Measurements and resolved findings

Workflow content occupies x=24–1416 at 1440 pixels and x=16–304 at 320 pixels. It retains the shared 24px/16px inset without another page wrapper inset. All final captured documents have `scrollWidth === innerWidth`. Native log scrolling contains long unwrapped lines. The job keyboard outline is a visible 2px solid line, using `rgb(232, 112, 95)` in dark and `rgb(184, 66, 47)` in Paper. Self-hosted controls retain the shared no-brackets treatment.

Resolved during this review:

- Unknown pool observations no longer display zero activity during loading or failures.
- History failures do not also display a successful empty-history message. The run selector distinguishes loading, failure and an empty result, and is disabled before useful data is available.
- The search control uses the shared Input. Step disclosures expose stable `aria-controls` targets.
- Earlier/Later/Latest controls make every loaded log window reachable; their group wraps on narrow screens.
- Mobile job selection originally expanded ordinary pages to 394px and long matrix pages to 2,226px. Explicit zero-minimum grid columns and selector bounds now keep both at the viewport width.
- New step/log borders use the shared hairline token. Failure indicators use the existing destructive token.
- Actions deep links on ordinary services normalize to Overview rather than displaying an empty panel.

The UI explicitly limits history and log windows, distinguishes GitHub logs from live runner output, preserves waiting/permission/expired states, and directs users to GitHub for queue status and complete retained logs. It does not claim that unassigned GitHub jobs belong to this pool.

## Evidence and reproducibility

VM directory: `/srv/hakopod-backup-scratch/workflow-logs-20260929/ui-review`.

- `main.tsx`, `vite.config.ts`, `review.mjs`, `focus.mjs`, `filters.mjs`: synthetic fixture and checks.
- `evidence/results.json`: 84 main result records, including 78 screenshots.
- `evidence/findings.json`: empty final main findings list.
- `evidence/focus-results.json`: 14 focused result records, including eight screenshots.
- `evidence/focus-findings.json`: empty focused findings list.
- `evidence/filter-results.json` and `filter-findings.json`: two passing reload/filter captures with no findings.
- `evidence/contact-01.png` through `contact-10.png`, and `focus-contact-01.png`: inspected contact sheets.
- `before-font-fix`, `before-layout-fix`, `fixture-diagnostics`: discarded fixture/setup captures and before-fix evidence; these are not passing verification.

The fixture required the existing browser font configuration and the package font path in Vite's serving allowlist. Initial font-serving and unconfigured-browser attempts were rejected as evidence. An earlier interaction run was interrupted by a source-sync/HMR update; final checks ran after the source stabilized.

Reviewed component hashes before formatting-only changes:

```text
801aeb530d44a2ddef991456322054aa341b129540a1039027237e9331223241  managed-actions-workflows.tsx
0f745ace34a0fec36726c4dcf96bae3adc5f94ea1acaec6c7c136fb5e132632b  service-detail.tsx
186d1c9697f8e10df888a856fad52d37b4a2758d1e62192c1c4b908a5afe47b8  managed-actions-status.tsx
0b2001f56e6212a53a386141ea9a9c2af3f335a3cd5c44f312c769c65639a96d  actions-logs.ts
```

## Standing checklist

The reusable checklist below is copied from `ui-ux-checklist.md`. Its blank boxes are not an additional result log. The scoped coverage and limits above record this pass; unrelated dashboard routes are not newly certified.

### Layout and hierarchy

- [ ] The shared page container supplies 24px horizontal padding at 640px and above, 16px below. Nested pages fill its content box without duplicate insets, centered margins or width caps. Headings, summaries and lists align. Settings fill the area beside their navigation; embedded auth does not double its parent's inset.
- [ ] Page-heading and page-level tab-row bottom dividers reach both viewport edges. Full-width row backgrounds and borders do not shift their labels outside the content inset or cause document overflow. Internal card/inspector dividers stay within their component.
- [ ] Shared layout/spacing uses Tailwind utilities in JSX or `@apply` within semantic CSS classes. Responsive rules use the shared breakpoint; raw CSS remains for component-specific behavior. There are no competing page padding overrides.
- [ ] Page and form-section headings have no decorative icon or visible description. One title carries the context; necessary actions remain aligned. Status and data needed for decisions stay visible; explanatory prose moves to help.
- [ ] Help is short, named, keyboard reachable and available on touch. Focus/hover or activation reveals it; Escape dismisses it; it stays inside the viewport. It is not the sole home for essential warnings or field instructions.
- [ ] Pages have one h1, sensible subordinate heading order and no duplicate headings or breadcrumbs. Nested back navigation uses the global header.
- [ ] Projects is the default home regardless of saved scope. Application lists are URL-scoped to a valid project and environment; invalid or unavailable scopes never fall back silently or display another project’s cached data.
- [ ] Page-heading vertical padding is balanced above and below at every responsive layout.
- [ ] Active main navigation, tabs and Settings/preferences sections use the shared theme-aware red for text and icons, with no selected underline, border or background fill. Hover/focus preserve the selected color and visible keyboard focus; ordinary row dividers remain visible.
- [ ] The active section remains visible inside scrollable navigation after deep links, selection changes, font loading and resizing, without scrolling the document.
- [ ] Desktop navigation is one compact row. Mobile reflow and long names do not cause document overflow or clipped actions.
- [ ] Summaries are inline and compact. Catalog lists have no outer panel border. Cards keep restrained surfaces, readable spacing and meaningful hover/focus states.
- [ ] Application/service grids use at most four normal desktop columns and five wide columns. Sparse grids retain card widths.
- [ ] Action links use shared Button with asChild. Sibling actions have equal height and vertical alignment; labels remain readable and targets usable.
- [ ] Application endpoint text and external-link icon stay on one line; long labels truncate without hiding the destination from assistive technology. Alarm links have visible separation from the preceding content.

### Components and interaction

- [ ] Consume Hatch through public exports. Use shared tokens for color, spacing, state and typography; check dark and Paper themes.
- [ ] All selects use SelectField, including disabled/empty options and accessible labels. Do not add native selects or a competing wrapper.
- [ ] Keyboard focus is visible. Cards preserve native links and independent menu/copy actions. Disabled/loading controls cannot double-submit.
- [ ] Forms with more than four inputs use nested pages. Labels, contextual instructions and validation stay associated with inputs. Textareas can expand where useful.
- [ ] Consequential actions show a review/confirmation, and failed requests preserve entered values. Permission failures are clear.
- [ ] Before screenshots, wait for the route’s expected body or control, lazy imports, query completion and fonts. Reject unexpected loading placeholders, rendered error boundaries and fixture schema errors. A page title or clean console alone is not evidence that the page loaded.
- [ ] Empty, loading, denied, failure and stale-data states remain usable without artificial success. Long IDs, URLs, values and translated-length text wrap or scroll within their component.

### Data, safety and cost

- [ ] Display actual backend observations; separate desired, observed, pending and stale state. Never fabricate metrics, logs or deployment success.
- [ ] Secrets are write-only, roles are enforced by Go, and mutation controls respect access. Review shared-secret impact before replacement.
- [ ] Polling, streaming, caches and buffers are bounded and inactive work stops. Do not add dependencies or runtime services for cosmetic changes.
- [ ] Product copy is concise plain English with no emojis. Avoid repeated context and implementation jargon in routine flows.

