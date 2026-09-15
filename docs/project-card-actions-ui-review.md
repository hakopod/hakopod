# Card actions, Git entry points and runtime settings review

Independent UI review completed on 2026-09-15. The reviewed changes pass after the findings below were resolved.

## Coverage

The isolated fixture renders the actual dashboard route/component source and Hatch styles. It is visibly marked as a fixture, uses artificial API responses, and reports unobserved workload health. It does not connect to a database, Kubernetes cluster, Git provider or production account.

| Area | Coverage | Result |
| --- | --- | --- |
| Project cards | Shared/personal projects; administrator, project administrator and viewer; rename and delete actions | Pass |
| Application cards | Empty/nonempty applications; menu rename, conflict, save, cancel and Escape; permissions and delete-dialog regression | Pass |
| Git entry points | New source build, TOML repository import, new application, import review and existing build editor | Pass |
| Runtime settings | New/existing application configuration and new/linked source builds; commands, arguments and plain environment variables | Pass |

All four combinations of dark/Paper themes and 1440 × 900 desktop/390 × 900 touch viewports were exercised. The final passing runs contain 236 assertions: 72 card, 64 Git entry-point, 56 runtime and 44 final regression/geometry checks. Rendered screenshots were inspected individually and in contact sheets. No unexpected page exception occurred; 409/422 responses were deliberately injected to check error behavior.

## Verified behavior

- Project rename sits immediately left of delete with an exact 8px gap. Both controls measure 36 × 36px and share the same vertical position; the action group ends at the card heading's right edge. Personal projects have no delete action. Project administrators can rename their project; viewers have no mutation controls.
- Application cards have one ellipsis control and no standalone rename pencil. Authorized users can rename empty and populated applications through the menu. Cancel, Escape and successful save restore focus to the correct ellipsis. Conflicts preserve entered names and keep the dialog open. Card links do not intercept actions. Delete still opens its own confirmation.
- Git paths point to `/builds/new` and `/applications/import`, expose the active page through `aria-current`, and preserve red active text through hover and keyboard focus with no selected background. New-application choices reflow into readable rows on mobile. Framework detection and the Cloud Native Buildpacks option remain discoverable. Method navigation is absent during import/deployment review and in the existing-build editor.
- Quoted command arguments and multiline environment values survive form → TOML → form conversion. Invalid commands remain editable; rejected reviews and saves retain values. Review payloads contain the parsed command/arguments and environment map. Duplicate variable names and secret-like names produce visible errors without losing input.
- Existing application settings seed the editor correctly. A stale revision stops review and keeps edits. Linked source builds preserve command, arguments and environment by omitting them from the request until replacement is explicitly selected; explicit replacements send the entered map.
- Runtime variable inputs and textareas now both start at 44px high. Desktop name/value labels and fields align; mobile fields stack within the page. Value fields retain vertical resizing. Add/remove variable controls work without submitting the surrounding form.
- Page/control bounds fit the layout and visual viewport. Page insets are 24px desktop and 16px mobile. Cards, dialogs, menus and runtime fields show no decorative self-hosted brackets; keyboard focus remains visible.

## Findings resolved

1. Git method navigation initially used a filled primary button, then a class that did not apply active color. It now uses the shared navigation red and transparent background, including hover/focus.
2. The mobile new-application choice row squeezed “Build from Git” into three lines. A responsive grid and single Git icon now keep each choice readable.
3. Runtime value textareas inherited a 112px minimum, leaving name/value rows uneven. They now use aligned compact 44px fields with vertical resizing.
4. Modern browser HTML-pattern warnings exposed an unescaped hyphen in application/service name patterns. The touched form patterns were corrected; final checks produced no pattern warning.

## Scope limits and evidence

This review verifies rendered UI and request construction against controlled responses. It does not claim real rename persistence, Git cloning, framework detection output, builds, deployments, Kubernetes readiness or backend authorization. Existing linked-build state is supplied directly to the real BuildForm component. Backend persistence and cluster acceptance belong to the separate implementation checks. Global authenticated shell navigation and unrelated routes were not re-reviewed.

Local evidence is in `/tmp/hakopod-card-actions-review/`: `results.json`, `git-results.json`, `runtime-results.json`, `final-results.json`, scripts and screenshots. `final-*-env-fields.png` records the final compact textarea styling; earlier runtime screenshots predate that cosmetic correction. The Vite fixture was stopped after review; no fixture implementation is part of the product change.

## Standing checklist

The following checklist is copied from the shared UI review guide. Blank entries are the standing gate for future reviews, not additional claims beyond the scoped results above.

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

