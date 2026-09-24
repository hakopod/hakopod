# Volume resize UI review

Independent review completed on 2026-09-24 against the [dashboard checklist](ui-ux-checklist.md), engine contributor instructions, and Hatch public component guidance.

## Result and scope

Passed after resolving mobile clipping, operation-summary focus, stale deletion confirmation, and unavailable maintenance-status findings. The reviewer changed only `web/src/components/volume-resize.tsx`; the service-storage and application-services integrations were inspected and retained. Backend authorization, filesystem migration, Kubernetes behavior and production deployment require their separate validation.

The rendered fixture imports the actual shared resize components and the actual `ServiceStorage` implementation (exposed only by a local Vite transform). Application-services placement was inspected in the route source; its `VolumeResizeOperations` section was rendered in the shared page container. Both self-hosted styles and Cloud edition styles were exercised. This is component and integration-placement evidence, not a full authenticated Cloud-shell or live BYOD test.

## Coverage

- [x] 104 base rendered states: both editions, Dark and Paper themes, 320px mobile and 1280px desktop, at 717px height. Cases cover draft, reviewed target, queued, copy failure, switch failure, retained original, unhealthy and unverified retention, cancelling, cancelled, reclaiming original, completed, and service-storage integration.
- [x] Every base case measures document, dialog, text, input and action bounds, waits for expected content/fonts/transitions, and records page errors. No content crossed the viewport and no page errors remained. Full-resolution captures and all nine contact sheets were inspected. Cloud's existing decorative brackets extend three pixels beyond the dialog border while remaining within the viewport; self-hosted dialogs have no decorative brackets.
- [x] Eight interaction sets cover both editions/themes/widths: review failure preserves target; stale application revision blocks submission; closing and reopening retains operation discovery; reload returns the current operation; cancellation advances to restoring services; post-switch repair exposes retain-both, without Cancel resize; accepted deletion remains reclaiming until completed is observed.
- [x] Destructive confirmation is off by default, names the original claim and reclaimed GiB, and states that backups remain. Keyboard Space and tapping the associated label work. Permission, revision, health and maintenance-query changes clear or disable prior consent. Failed action requests preserve the user's still-valid choice.
- [x] Forty-eight additional permission cases cover developer, viewer, matching project admin, other-project admin, explicit management capability with write, and management capability without write, across both editions/themes/widths. The selected project is deliberately stale. Only matching application-management plus deployment-write access exposes resize mutations; read-only users can inspect existing maintenance.
- [x] Eight edge sets cover invalid, unchanged and fractional sizes; failed maintenance loading; long names; scrolled confirmation; denied actions; and shared mount deduplication. One private service volume plus two mounts of one named volume produces two resize controls. Temporary mounts have none. Twenty-four supplemental edge screenshots and eight keyboard-focus screenshots were inspected.
- [x] The input/review uses the shared Input, Dialog and Button; the consequential start action uses the accent, permanent deletion uses destructive styling and a named trash icon. All body layout is bounded Tailwind grid layout using the shared dialog scrolling area. No extra page inset or width cap was added.
- [x] Essential warnings remain in the dialog body. The review shows old and target GiB, affected services, extra temporary allocation, and combined old/new quota consumption. It explicitly says usage is checked after services stop, and that insufficient fit blocks switching and asks original services to resume. It makes no promise of a consistent online database measurement or guaranteed fit.
- [x] Pre-switch cancellation states that original services are restored and only staging is removed. Post-switch recovery warns that new writes exist on the new volume and keeping both ends maintenance without rolling back data. Missing copy verification and unhealthy application state visibly block original deletion.
- [x] Tab focus remains in the dialog; Escape closes it. Focus is visible through the self-hosted outline or Cloud focus brackets. Opening an existing operation focuses its summary so mobile users see context before reaching destructive confirmation. Long content scrolls independently of the action footer.

## Findings resolved

The initial implicit grid track grew to roughly 364px inside a 320px viewport. This clipped long service names and made the input extend beyond the dialog despite a clean document-width check. A bounded single-column grid, anywhere wrapping and the shared scrollable dialog body resolve it. The retain-both action can wrap on narrow screens.

Radix initially focused the first interactive deletion checkbox on opening a retained operation, scrolling past its identity and status. Existing operations now start at a focusable summary; the checkbox remains available by keyboard and touch without being the first thing presented.

Maintenance-query failures originally made the overview disappear and left fresh resize actions available. Errors now remain visible and uncertain status blocks new or destructive mutations. Confirmation is reset when the reviewed application revision, capability, health, operation phase or maintenance request changes. The start/review action now uses the configured accent, and retries identify cancellation or storage cleanup when applicable.

## Evidence and limits

Local evidence is in ignored `work/resize-review/`: `results.json`, `interactions.json`, `edges.json`, `focus.json`, scripts, screenshots and contact sheets. All API responses are explicitly synthetic and nonlocal requests are blocked. No dependencies were installed. The fixture reused existing dashboard and browser dependencies. No production API, workload, credential or customer data was accessed. Source was formatted with the dashboard's Prettier.

This pass does not prove live owner revalidation, source/PVC identity binding, quota reservations, filesystem-copy integrity, service readiness, real cancellation, or storage reclamation. It verifies the UI's presentation and request flow; the implementation's backend tests and real development-cluster acceptance must establish those properties. Full application-route navigation and unrelated pages were not re-audited. The local fixture server was stopped after review.

## Standing checklist

The reusable gate follows. Scoped results above record this pass; unrelated entries are not asserted as newly tested.

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

