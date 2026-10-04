# Mathesar template UI review

Reviewed on 4 October 2026 against [the Hatch dashboard checklist](ui-ux-checklist.md).

This pass covers the catalog list, Mathesar inspector, configuration page, bundled and existing PostgreSQL choices, deployment review, secret editing, and their failure and permission states. The actual dashboard route modules, shared shell, form, selectors and styles were rendered. Catalog metadata came from `spec.Templates()` and review specifications from `spec.PlanTemplate()` in this checkout.

Identity, project, saved-secret metadata and failure responses were explicitly labeled development fixtures. No live account, application, cluster, secret or deployment was changed. This is UI evidence; it does not establish runtime readiness, connectivity, migrations or successful deployment.

## Findings resolved

- Catalog category selection used green text, a border and a filled surface. It now uses the shared theme-aware red for the selected label and count, with visible keyboard focus.
- The catalog had an outer panel border and artificial filler cells. It now uses separate cards without an outer border. Sparse results keep their column width; desktop grids retain the four-column and five-column caps. Responsive rules use Tailwind `@variant`.
- Catalog toolbar dividers stopped at the content inset. They now reach the viewport edges while controls retain the shared 24px inset, or 16px below 640px.
- Browser-generated `token64` secrets were only 44 characters and failed the server's 64-character requirement. The generator now uses 48 random bytes, producing 64 Base64 characters, matching the server. The shared regression suite covers all generated secret formats.
- The longer existing-database form could focus Database port and Shared media storage class behind the sticky action footer. The template form now enables the existing `FormPage` focus-reveal behavior. Tab traversal measured all 12 controls in each of eight theme/width cases without footer overlap.

Conditional configuration fields use the centralized `SelectField` for declarative choices. Only active fields are required or sent for review. Switching database modes keeps hidden external connection drafts, and failed review requests preserve the entered values. The TLS default is **Verify server identity**; its visible help explains the less strict alternatives. The existing-database plan contains the application and proxy, with no bundled database service.

## Rendered coverage

| Route or state | Dark and Paper, 1440px and 390px | Additional coverage |
| --- | --- | --- |
| Catalog search and Mathesar card | Loaded metadata and logo, count, alignment, active filters | Full catalog at 320px, 390px, 1440px and 1920px; one/four/five column bounds and no filler cells |
| Mathesar inspector | Open, requirements, secrets, logo, action, dismissal and focus return | Keyboard opening and Escape dismissal; viewport bounds |
| Bundled PostgreSQL form | Required fields, labels, storage instructions, HTTPS URL and review gating | 320px and 1920px edge layouts |
| Existing PostgreSQL form | Conditional fields, TLS selector, mode toggle and retained values | Touch selection, long TLS option wrapping at 320px, visible field help |
| Review and secret editor | Both Go-generated plans, missing-secret gating, generated signing key, retained save-error draft | External canonical TOML excludes the database; bundled submission omits inactive connection fields |
| Failure and permissions | Review failure, secret-save failure, deployment failure, denied access, empty search and catalog error | Every retained-error state uses explicitly artificial responses |
| Focus and overlays | Named help, keyboard open/close, touch help, select popups | 96 focused-control measurements at 320px, 390px, 1440px and 1920px across both themes; viewport screenshots avoid full-page fixed-overlay distortion |

The review waited for expected route controls, query results, fonts and finite animations before accepting captures. Geometry checks covered actual elements, not only document overflow. No browser exceptions occurred. Full-resolution screenshots and a representative contact sheet were inspected critically; the narrower fields and menu text remained readable, the logo retained contrast in Paper, and focus remained visible.

## Verification and evidence

- `node scripts/test-ui.mjs` passed all 211 shared UI tests, including conditional draft filtering and generated credential formats.
- `pnpm --dir web exec tsc --noEmit` passed.
- The main browser matrix passed 20 grouped checks. Eight supplemental theme/width cases checked the full catalog, focus bounds and TLS popup, including touch selection on narrow screens.
- Ignored local evidence: `work/mathesar-ui-review/results.json`, `supplemental-results.json`, `review.mjs`, `supplemental.mjs`, `generate.go`, and `evidence/`. Final reruns use the current Go-generated plans and the updated TLS label.
- Representative screenshots: `dark-1440-catalog.png`, `light-1440-inspector.png`, `dark-1440-configure-external.png`, `light-390-configure-external.png`, `light-320-tls-menu-viewport.png`, `dark-1440-focus-media-storage.png`, and `light-1440-review-external.png` in that evidence directory.

The template implementation and UI review were delegated separately. Once the reviewer fixed the issues above, the coordinating agent independently inspected the catalog, inspector, bundled/external forms and deployment review. It then inspected the final 320px Paper TLS menu and 1440px dark focused-storage viewport captures: the menu stayed within the viewport, selection remained readable, and the focused field remained clear above the sticky footer. The second reviewer found no remaining visual issues in the covered states.

A later Xem review found touching actions in the shared secret editor. Its two action rows now use wrapping flex layout with an 8px gap, and generated-value help occupies its own row. Three focused secret unit tests passed. The final screenshot pass ran in an isolated VM scratch directory: Mathesar and Xem at 1440px, 390px and 320px in both themes passed all 12 contexts, producing 18 secret-editor captures. Real Tab navigation verified the visible focus outline, controls remained in bounds and clear of the footer, and failed saves retained their draft values. The coordinating agent independently inspected four representative screenshots and confirmed readable spacing, wrapping and focus with no clipped or overlapping controls.

Final spacing evidence is in ignored `work/xem-ui-review/remote-final/`, including `secret-layout-results.json`, `mathesar-light-390-secret-key-layout-final.png` and the Xem RSA/storage examples. Builds, dependencies and browser execution for this final pass stayed on the VM, capped to two CPUs and 2 GiB memory; local files were only used to retrieve and inspect its evidence. Fixture and browser processes stopped after the pass.

The final VM pass also exercised Mathesar's derived `shared_storage` requirement with a visibly marked development fixture representing paid hosted compute and 50 GiB of ordinary storage. All four Dark/Paper cases at 1440px and 390px showed “Requires your own server” and “Shared storage (ReadWriteMany)” before any configuration form or plan request. Choose compute remained available, keyboard focus was visible, and keyboard/touch return navigation reached the catalog. Desktop and mobile screenshots were critically inspected with no clipped text or controls. Evidence is `hosted-gate-results.json` and `hosted-shared-storage-*.png` in the same final evidence directory. This checks the shared capability gate in the public shell; it does not represent a full Cloud shell review or a live hosted allocation.

## Limits

Actual backend authorization, external PostgreSQL connectivity and TLS negotiation, first administrator setup, database migrations, ReadWriteMany storage, media persistence, restarts and backups are outside this browser fixture. Loading, stale-query recovery, the full Cloud shell, unrelated route families and existing-application service additions were not newly tested. Successful deployment was deliberately not simulated. The full repository build, Go tests and cluster acceptance status are recorded separately by the implementation task.

## Standing checklist

The checklist below is copied from the shared review gate. Its unchecked boxes are the reusable repository-wide checklist, not a claim that unrelated routes were retested in this scoped pass. The completed coverage and limitations above record this review.

### Layout and hierarchy

- [ ] The shared page container supplies 24px horizontal padding at 640px and above, 16px below. Nested pages fill its content box without duplicate insets, centered margins or width caps. Headings, summaries and lists align. Settings fill the area beside their navigation; embedded auth does not double its parent's inset.
- [ ] Page-heading and page-level tab-row bottom dividers reach both viewport edges. Full-width row backgrounds and borders do not shift their labels outside the content inset or cause document overflow. Internal card/inspector dividers stay within their component.
- [ ] Shared layout/spacing uses Tailwind utilities in JSX or `@apply` within semantic CSS classes. Responsive rules use the shared breakpoint; raw CSS remains for component-specific behavior. There are no competing page padding overrides.
- [ ] Side-by-side settings share heading, control and help rows. Measure matching labels and control top/bottom edges on the Y axis, including wrapped help and validation states. Do not pair a control with an internal heading against a sibling with an external label. Check alignment visually as well as checking overflow; use shared grid tracks where content can wrap.
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
