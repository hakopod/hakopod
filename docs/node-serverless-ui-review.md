# Node placement and serverless UI review — 2026-09-20

Independent reviewer: notification_ui_review agent. Scoped UI review passed after the fixes below. Followed AGENTS.md, Hatch contributor/public component guidance and the standing checklist. The implementation agent authorized UI corrections and requested the Add service function entry during this review.

## Rendered coverage

Actual ServiceExecutionFields, FunctionEditor, DeploymentForm, ServiceDetail and Add service route components were rendered with self-hosted dashboard CSS in an explicitly labeled local fixture. All API calls were intercepted; non-localhost network requests were blocked. No deployment was submitted, no function source was executed, and no real node, gateway or application was changed.

Dark and Paper themes were covered at 1440px desktop, 390px mobile and 320px narrow mobile. Final evidence contains 114 screenshots, six contact sheets, full-resolution editor crops, `results.json` and `add-results.json` in ignored `web/work/node-serverless-review/`. Expected loaded controls, API results and fonts were awaited before successful-state captures. Loading/error captures are explicitly separate cases. Contact sheets and full-resolution key fields, review summaries, route and editor captures were inspected.

- Node picker: eligible architecture options, disabled unavailable option, keyboard opening, selected node, loading, failed discovery, retry and missing saved node. The saved value remains intact; an unverified value is labeled “saved selection” during loading/failure and “unavailable” only after a successful response omits it.
- Serverless controls: unavailable gateway disables starters/enable control; always-warm selection disables idle timeout; sleep and warm review summaries remain readable. Numeric settings retain defaults through callbacks. Concurrency, startup wait and request limit are included in the review summary.
- Both JavaScript and Python starters produce editable source and correct runtime/mount path. Existing node choice survives starter selection. Edited source reaches the mocked plan. A mocked review conflict preserves source, and retry reaches review. No final deploy action was invoked.
- Actual service-detail overview displays configured node placement and serverless policy, alongside explicitly unavailable synthetic runtime data. The review does not represent configuration as observed live replicas.
- Add service now has a dedicated HTTP-function entry with required unique name and JavaScript/Python choices. It opens the existing DeploymentForm directly without requiring a dummy image. Empty names are rejected, existing names disable submission, and both languages reach editable source. Availability failure/retry, disabled gateway and denied deployment permission are rendered. Denied users see no starter controls.
- Resource scope was tested against an intentionally different saved scope: placement lookup uses `resource-project/resource-env` from the loaded application, not the fixture’s `fixture-project/fixture-env` preference.
- Actual bounds showed no document overflow in the core fields, review, service detail or Add service flows at any tested width. Source editor height is 288px in both themes/all widths. Shared page insets and alignment were visually checked. Keyboard selection and native input validation work; mobile starter actions were activated by touch without forced clicks. Focus remains visible in the shared controls/editor.

## Changes and resolved findings

1. Fixed generated-type callback errors by spreading serverless defaults before optional saved settings.
2. Stopped labeling a saved node unavailable before discovery has completed successfully.
3. Corrected review replica ranges and always-warm/idle wording; included startup/request limits. Always-warm helper copy now states that one container remains running.
4. Shared textarea CSS overrode the intended editor height. Applied the Tailwind important minimum-height utility and verified the resulting 288px bounds.
5. Availability failures disable new starter actions even if an earlier success is cached.
6. Added directly discoverable, scope-aware JavaScript/Python function starters to the existing Add service route, retaining the configuration and review step.

Production UI files edited by this reviewer: `web/src/components/service-execution-fields.tsx`, `web/src/components/deploy-dialog.tsx` and `web/src/routes/applications.$applicationId.services.new.tsx`. Service-detail and starter-library additions were inspected and exercised but not edited by this reviewer. `tsc --noEmit` and `git diff --check` passed after the final UI changes.

No unresolved findings remain in the covered states. This is client behavior and visual verification using synthetic responses, not live node scheduling, gateway activation, image execution, backend authorization or deployment acceptance. The implementation agent owns those checks and the dashboard build. Full shared-shell navigation and unrelated route families were not re-reviewed in this pass. Preview/browser processes were stopped after the review. No commit or push was made.

## Standing checklist

The following is the reusable global checklist. Coverage above records this scoped review; blank boxes do not imply unrelated routes were retested.

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
