# CLI consent UI review

Independent review completed on 2026-09-24 using the [dashboard checklist](ui-ux-checklist.md) and contributor rules. Scope: `web/src/routes/login.device.tsx`, the return-destination change in `web/src/components/auth-screen.tsx`, and their shared account, selection, error and help components.

## Result

Passed for the affected UI after resolving the destination-summary finding below. This is a synthetic rendered UI review, not proof of live authentication, account creation, provider sign-in, CLI token issuance or deployment.

The original consent page kept showing “Choose below” after a destination was selected. A long destination label also truncated in the mobile picker. The implementation now displays the selected project/environment and a wrapping workspace label in the review summary. Independent screenshots at 320px in both themes confirm that the authorization decision has readable destination context. Refreshing a list that removes the selected destination disables authorization and clears the stale summary.

## Rendered coverage

| Surface | Viewports | Themes | States |
| --- | --- | --- | --- |
| Self-hosted account entry and consent | 320, 390, 1280 | Dark, Paper | Ready, no ready destinations, expired request, signed-out device entry |
| Product-edition account entry and consent | 320, 1280 | Dark, Paper | Same ready, empty, expired and sign-in states with edition styles |
| Both editions, edge cases | 320, 1280 | Dark, Paper | Fixed project scope, loading, missing code, signup |
| Both editions, interactions | 320, 1280 | Dark, Paper | Long selection, open picker, failed approval, denial, successful approval, refreshed-away selection |

The base and edge matrix contains 72 screenshots, plus interaction and help captures. The reviewer inspected contact sheets and full-resolution mobile, desktop, long-value, failure, help and account screenshots after expected content and fonts loaded. Captures wait for theme and menu transitions to finish. Explicit loading cases are distinguished from loaded-content evidence.

- [x] Shared content inset measured at 16px on mobile and 24px on desktop. The title divider reaches the viewport edges; consent uses the available content width.
- [x] No document overflow or clipped interactive controls in the covered widths. Cloud account illustration elements extend beyond their clipped decorative container; these do not widen the document or hide controls.
- [x] Picker is the existing `SelectField`. Keyboard Space, End, Enter and Escape work; Escape restores trigger focus. Focus remains visible without decorative self-hosted brackets.
- [x] Long destination labels wrap in the menu and in the explicit scope summary. The 320px picker popup is bounded at x=12, width=296; its long choice remains readable.
- [x] Touch selection and approval emit a single synthetic approval request. Busy state disables the picker and actions. Approve remains disabled until a current destination is selected.
- [x] Failed approval preserves the selection. Refreshing after access disappears removes the stale selected scope and disables approval. Deny and successful approval render distinct terminal-return messages.
- [x] Empty state provides dashboard setup in a separate tab and refresh, with Cloud compute guidance when applicable. It does not report access granted.
- [x] Heading help opens by keyboard focus or touch; Escape/outside touch dismiss it. At 320px its bounds are x=8, width=304.
- [x] Signed-out device entry stores the return destination. Failed password sign-in retains entered values. The signup page remains usable at mobile widths.
- [x] Shared Button links/actions, status/error components and theme tokens are used. No new visual dependency or runtime service was added.

## Evidence and limits

Ignored local fixtures and results live under `work/cli-review/`: `results.json`, `cloud-results.json`, `edges.json`, `interactions.json`, `cloud-interactions.json`, `help.json`, screenshot folders and contact sheets. The fixture mounts actual route/components against clearly labelled synthetic data and blocks nonlocal API requests. No production account or workload was accessed. Local fixture servers and review browsers were stopped after review.

The review covers the changed account surfaces, not a new audit of unrelated projects, settings, grids or resource navigation. OAuth provider redirects, email verification, real first-workspace creation, server-side scope enforcement and terminal polling need the implementation's separate integration tests. The route fixture does not represent an authenticated full dashboard-shell test.

## Standing checklist

The unchanged checklist below is a reusable gate. The scoped results above describe what this pass verified; unrelated checklist areas are not asserted as newly tested.


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

