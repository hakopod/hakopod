# Deployment notifications UI review — 2026-09-19

Independent reviewer: notification_ui_review agent. Scope: application notification settings route, shared FormPage/sections, controls/help/dialog, and source inspection of the application Deployments entry link. Followed AGENTS.md, Hatch contributor/public component guidance, and the standing checklist below.

## Verified coverage

An explicitly labeled local fixture rendered the actual route component and dashboard CSS with self-hosted document attributes. Only the route parameter and scope context were supplied by the fixture. All API requests were intercepted with synthetic records; no production API, deployment, account, SMTP server or webhook was used. Isolated Playwright Chrome used a temporary profile.

- Dark and Paper themes at 1440px desktop and 390px mobile; additional 320px edge cases. Expected destination section and completed fonts were awaited. Final dialog screenshots waited for animations. Twenty main screenshots and ten edge screenshots were captured; contact sheet and full-resolution form/dialog/state screenshots were inspected.
- Create → review → mocked conflict → preserved name/email → successful save; edit with saved destination left blank → review → save; send-test acknowledgement; delete confirmation, Escape cancellation and confirmed deletion. Mocked responses verify client behavior only.
- Read-only state hides editing/testing/deleting and the form, retaining visible permission guidance and history. SMTP unavailable disables enabled-email review; pausing permits review.
- Shared page inset measured 24px desktop and 16px mobile; page-heading divider spans the width. Main cases have no document overflow. Destination controls are 40px high. Help is focusable and was activated with keyboard and touch; measured focus outline is 2px red in both themes. Delete dialog Escape works.
- Source inspection confirms public Hatch imports, centralized SelectField, password webhook/secret fields, concise visible SMTP/endpoint instructions, review before persistence, disabled busy controls, named trash icon and five-second query polling. Edit fixture confirms saved recipient remains blank. Application entry uses shared Button asChild and the loaded application ID.

## Findings resolved during review

1. Initial empty error state incorrectly rendered an unexpected-error toast. Render RequestError only when an error exists.
2. Mobile destination text collapsed to a few characters per line beside action buttons. Stack destination text and actions below the small breakpoint.
3. Review omitted destination confirmation and claimed notifications were enabled while paused. Show entered email or safe URL hostname, retain unchanged-destination indication, and use paused-specific copy.
4. At 320px the deletion footer pushed Cancel outside the dialog. Allow action wrapping.

All four fixes were re-run and visually verified in both themes. The 320px deletion footer now wraps, keeping both actions entirely inside the dialog. Scoped notification-page review passed with the verification limits below. Local preview and browser processes were stopped after review.

## Evidence and limits

Local ignored evidence: `web/work/notification-review/`: `server.mjs`, `main.tsx`, `review.mjs`, `edge.mjs`, `results.json`, `edge-results.json`, `contact.png`, and theme-width-state PNGs. Fixtures are not shipped as application state.

This pass does not claim live delivery, backend authorization, SMTP/webhook connectivity, retries or Kubernetes verification. Full shared header/navigation and the Deployments entry route were inspected in source, not mounted end to end. Loading/denied-query/server-failure/encryption-unavailable/maximum-target and all non-email channel flows were not independently rendered. Main agent owns backend and build verification. The checklist below is the reusable gate; boxes remain blank for areas outside this scoped pass rather than implying global retesting.

## Standing checklist

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
