# Service volume cleanup UI review

Independent review completed on 2026-09-24 against the [dashboard checklist](ui-ux-checklist.md) and contributor rules. Scope: `DeleteServiceDialog`, its volume-removal review payload, and the volume-cleanup status/retry section on deployment detail.

## Result

Passed after resolving cleanup-note layout and retry-feedback findings. The reviewer made only UI changes to deployment detail; backend reclamation and production cleanup were outside this review.

## Coverage

- [x] Self-hosted and product-edition styles, Dark and Paper themes, 320px mobile and 1280px desktop. Forty base rendered cases cover initial deletion review and cleanup reclaiming, deleted, retained and error states.
- [x] Twenty-four additional captures cover confirmed deletion review, rejected final deletion and failed cleanup retry. Eight mobile edge captures cover default volume retention and services whose volumes are all shared.
- [x] Permanent volume deletion is off by default. Keyboard Space and touch can select it before planning. It is disabled while planning and after the review has been accepted, so the final action cannot silently change the reviewed choice.
- [x] The final destructive action explicitly says “Delete service and volumes” when opted in. Otherwise it says “Delete service.” The review identifies eligible volumes, warns of permanent data loss and states that shared volumes and backups remain.
- [x] Synthetic planned requests remove the service and its unused private named-volume definition while preserving the shared named-volume definition and its remaining service mount. The default request does not include `delete_service_volumes`. A service with shared volumes only has no permanent-volume checkbox.
- [x] Failed planning preserves the selected option and allows retry. Failed final deletion preserves the accepted review and its disabled option. Busy controls prevent further interaction. Escape closes the dialog and restores the invoking button's focus.
- [x] Cleanup status distinguishes reclaiming, retained and deleted observations. A successful retry response retains the reclaiming status until a subsequent backend observation says deletion finished; it does not claim immediate quota release.
- [x] Failed cleanup retry reports a visible error without opening the deployment-cancellation dialog. Retry uses a busy label and remains usable after failure. The retry action is absent for a principal without deployment-write permission.
- [x] Shared page insets remain 16px on mobile and 24px on desktop. The cleanup note and its action fit the viewport. Both themes preserve readable warnings and visible focus. The existing deployment pipeline remains an intentionally scrollable mobile region; its offscreen children do not cause document overflow.
- [x] Screenshots were captured after expected content, fonts and transitions settled, then inspected at full resolution and in contact sheets. Final document width equals viewport width in every base case.

## Findings resolved during review

The cleanup section originally reused a flex-row note with each paragraph and action as a sibling. At 320px this squeezed claim names into a vertical letter column and moved the retry button outside the document. A bounded stacked inner container now keeps all status content and the action inside the note.

Cleanup retry originally put its error into cancellation-dialog state, leaving failed retries invisible while that dialog was closed. A separate cleanup error now renders with the cleanup section. The retry button also respects deployment-write capability and identifies the requesting state.

## Evidence and limits

Ignored evidence is under `work/service-volume-review/`: `results.json`, `interactions.json`, `edges.mjs`, screenshots and contact sheets. The local fixture imports the actual components and deployment route, supplies explicitly synthetic API responses, and blocks nonlocal API calls. UI request inspection confirms the proposed payload, not server-side authorization or actual data removal.

This review does not establish Kubernetes deletion, shared-volume protection in the backend, quota reconciliation, backup recoverability, real deployments or production health. Those need the implementation's separate tests and runtime checks. Unrelated dashboard routes were not audited again. No production API was accessed; the fixture server and browser sessions were stopped when the review finished.

## Standing checklist

The checklist below is the reusable gate. Scoped results above record this pass; unrelated entries are not asserted as newly tested.


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

