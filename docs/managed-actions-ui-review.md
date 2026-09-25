# Managed Actions UI review — 2026-09-25

The independent reviewer signs off on the Managed Actions UI changes after source inspection, VM browser interaction checks, element measurements and critical screenshot inspection. The defects found in the new feature were resolved. This is a scoped feature review, not a claim that every historical dashboard style matches the standing checklist.

## Method and limits

All compilation and browser execution ran on the VM in `/srv/hakopod-backup-scratch/managed-actions-build/ui-review`, with existing Playwright 1.56.1 and Chromium 141. Builds used a disposable Vite harness importing actual product components/routes and styles. The browser used explicit artificial API records, intercepted all API requests and blocked external network requests. The harness visibly identified itself as a UI fixture. Production accounts, workloads and credentials were not used or changed. There were no local builds or browser installations. The owned VM fixture server was stopped after review; evidence files were retained.

The actual catalog route and its transition to `/templates/managed-actions`, the application detail route and service detail route were rendered. The global header/footer were a minimal fixture shell using the existing page-container classes; full authenticated navigation and Cloud workspace routing were not end-to-end tested here. Cloud-specific form branches were exercised by setting the edition flag in the fixture. Backend enforcement, GitHub workflow execution and deletion cleanup require their separate API/runtime acceptance evidence.

## Coverage and result

93 recorded screenshot cases passed their expected-content, no-page-error and document-bounds checks. An earlier failed long-value capture is retained as before-fix evidence and is not counted as passing.

| Coverage | Viewports and themes | Result |
| --- | --- | --- |
| Catalog card, Pro badge, requirement sheet; new form, review, missing secret, saved secret and failed deployment; service cleanup display | 1440, 390 and 320 pixels, dark and Paper | 36 cases passed |
| Missing scope, missing runner, Pro inactive, runtime unavailable, member/operator guidance, Cloud entitlement/runtime guidance, capability error, existing 15-minute timeout and architecture edit | 390 pixels, dark and Paper | 18 cases passed |
| Registration waiting, empty, stale and error | 320 pixels, dark | 4 cases passed |
| Actual application Services route with removed pool, empty pool inventory and query failure; actual service Overview with observed runners | 1440, 390 and 320 pixels, dark and Paper | 24 cases passed |
| Keyboard/touch help and selects, maximum-length review values, failed-plan persistence and explicit loading | 320 pixels, dark and Paper | 8 captures passed, with associated interaction assertions |
| Final resource paragraph spacing; licensed Cloud review with three slots and the intended resource fields in the plan request | 320 pixels both themes; 390 pixels Paper for Cloud | 3 cases passed |

Keyboard checks covered catalog activation and navigation, help focus/Space/Escape, select Home/Enter, review focus and return-to-configuration focus. Touch checks covered help and architecture selection. The select check waits for Radix to settle focus after opening; the earlier immediate Home/Enter assertion was a fixture timing failure, not a product finding.

The review shows the selected architecture, profile, concurrent slots, repository and credential reference. Missing provider credentials disable deployment; random-password generation is not offered for the GitHub token. Failed plan/deployment requests retain entered configuration. Changing an existing ARM64 pool to Automatic removes the architecture field from the submitted plan. An existing 15-minute timeout remains selectable. The Cloud fixture restricts the selector to three slots and submits the `large` profile with 500m/2500m CPU and 1Gi/4Gi memory request/limit fields.

## Measured bounds and visual findings

At 1440 pixels, the form and application content begin at x=24 and occupy 1392 pixels. At 320 pixels, they begin at x=16 and occupy 288 pixels. Form page-heading dividers extend from x=0 to the viewport width. The application cleanup component remains below its existing heading and tabs. At 320 pixels, long runner IDs, repository names and cleanup messages wrap within their component. All 93 passing captures have `scrollWidth === innerWidth`.

Select controls are 48 pixels high and ordinary action buttons are 40 pixels high. Self-hosted captures have no visible decorative corner brackets; Cloud retains its existing edition treatment. Visible focus is preserved. Both palettes remain readable, including disabled actions and request errors.

Resolved findings:

- The form is keyed to resource and workspace scope so a reviewed draft cannot silently move to a different application/project.
- Clearing architecture now clears the submitted value; nonstandard legal timeout values are retained.
- Missing scope/runner states are explicit and retain the page heading; current permission and capability checks guard submission.
- Runtime setup guidance distinguishes operators from members and Cloud users.
- The three selectors now have visible labels as well as accessible names.
- Removed pools display removal pending, hide configuration/new-workflow guidance and retain cleanup messages on the application Services page after service removal.
- Application-level cleanup status is mounted below the title/navigation; its error state includes Retry.
- Maximum-length review fields previously expanded the document to 1342 pixels at a 320-pixel viewport. `min-w-0` and `wrap-anywhere` now keep the review at 320 pixels in both themes.
- Resource paragraph sentence spacing was restored and visually checked.

An existing shared catalog deviation was noted: category chips use filled/bordered selection rather than the standing red-text-only navigation treatment. It predates Managed Actions and was not changed or represented as newly compliant by this pass.

## Evidence

VM evidence directory: `/srv/hakopod-backup-scratch/managed-actions-build/ui-review/evidence`.

Local review copies: `/Users/theboringhumane/Projects/hakopod-cloud/.local/actions-ui-review/evidence`.

- `results.json`: 58 form/catalog/component cases.
- `application-results.json`: 24 actual application/service route cases.
- `interaction-results.json`: 8 focused cases.
- `final-results.json`: 3 final probes.
- `contact-01.png` through `contact-12.png`: contact sheets. All coverage groups were inspected; representative full-resolution screenshots were also examined.
- `final-form-320-light.png`, `final-cloud-review-390-light.png`, `long-review-320-dark.png`, `long-review-320-light.png`, `app-cleanup-320-light.png`, `app-cleanup-1440-dark.png`, `actual-service-320-light.png`, and `help-keyboard-320-dark.png`: representative detailed evidence.
- `long-overflow-before.png` and `long-overflow-before.json`: resolved failure, not passing evidence.

Final product component hashes:

```text
c2748e3ce5d026b46a55d3522763f8de82638bbc18ca185aaf4f1c3b77b11aea  managed-actions-form.tsx
e1cb227eede72adaa5ed5c7284f231e655b53aeb701fe5595d947343de061416  managed-actions-status.tsx
```

## Standing checklist

The reusable checklist below is copied from `ui-ux-checklist.md`. Its blank boxes are not an additional test-result log. The scoped coverage, resolved findings and explicit limits above record this pass; unrelated route families and the existing catalog category style are not newly certified.

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

