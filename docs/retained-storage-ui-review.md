# Retained storage UI review

Independent UI/UX review completed on 2026-09-24 against the [dashboard checklist](ui-ux-checklist.md) and contributor rules.

## Result

Passed for the affected surfaces after fixing mobile long-name overflow. The review covered `RetainedStorage` inside the actual project application-list route, the persistent-data opt-in in `DeleteResource`, and the hosted-compute allocation and release wording.

This is synthetic rendered UI evidence. It does not prove that a real disk was reclaimed, a storage reservation was released, or any production workload was changed.

## Coverage

- [x] Dark and Paper themes at 320px mobile and 1280px desktop; 24 base screenshots covering retained, deleting, reclamation failure, inspection failure, application deletion and hosted compute.
- [x] Additional screenshots cover permanent reclamation review, rejected request, compute release review, and 63-character application/project slugs at 320px in both themes.
- [x] Actual project route has zero active applications while retained data remains visible below the application empty state. It does not require a live application card to offer reclamation.
- [x] Retained and reclaiming states have distinct copy. A successful synthetic 202 leaves the row as “Reclaiming data” with its action disabled, rather than claiming immediate cleanup or released quota. Failed cleanup exposes “Retry reclamation.”
- [x] Permanent data deletion is not preselected in the application-delete dialog. Exact typed confirmation is required. Keyboard Space toggles the labelled checkbox. Closing and reopening resets both confirmation and destructive opt-in.
- [x] Retained-data deletion rejects blank/wrong confirmation in the UI. The review visibly identifies project, environment and application, explains irreversibility and preserved backups, and distinguishes request acceptance from verified disk reclamation.
- [x] Keyboard Enter opens reclamation; Escape closes it and restores trigger focus. Touch activation works. Busy inputs/actions are disabled. A rejected request preserves the typed value and destructive opt-in; retry remains usable.
- [x] Compute release requires the workspace name. Allocation copy distinguishes this workspace's reservation from unreserved shared capacity and states that deleting an application does not release compute. Release guidance includes retained-data reclamation.
- [x] Shared content insets, theme colors, Button/Dialog/Input components and named trash icons are retained. Modal and action controls fit the checked viewports. Screenshot review follows font loading and completed transitions.

## Resolved finding

A 63-character unbroken project slug did not wrap in the retained-data dialog. The retained application's long name also expanded an implicit grid column, growing a 320px document to 525px and causing horizontal scrolling when opening the modal.

The implementation added wrapping to the dialog/scope, a bounded single-column grid, minimum-width resets on retained rows and wrapping for application names. Final independent checks in both themes report document width and scroll width of exactly 320px, with no out-of-viewport modal element bounds. The long-value screenshots were inspected after the fix.

## Evidence and limits

Ignored fixtures, screenshots, bounds and interaction results are under `work/retained-review/`: `results.json`, `interactions.json`, `screenshots/`, contact sheets, and the long-value probes. The fixture imports the actual project route and engine components. Hosted allocation copy is rendered from a local copy of the current component with its import paths adapted to the fixture and the product-edition styles loaded. All API responses are labelled synthetic and nonlocal requests are blocked.

The inspection-error state uses the existing shared error notification and periodic query retry. The review did not exercise real provider/network recovery, authorization, storage deletion, quota reconciliation or backup restoration. Those require separate server tests and runtime verification. Unrelated application catalogs and navigation were not retested as a complete dashboard audit. No production API was accessed; the local fixture server and browsers were stopped after review.

## Standing checklist

The standing checklist below is copied as the reusable review gate. Completed evidence above is scoped to this change; unrelated checklist items are not asserted as newly tested.


Copy this standing checklist into each UI review. The blank boxes are a reusable gate, not the result log; the completed review below records this pass.

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

