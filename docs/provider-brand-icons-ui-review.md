# Provider artwork and Settings navigation independent UI review

Independent rendered approval is granted on 14 September 2026 for the provider
artwork and Settings/preferences spacing changes on `fix/provider-brand-icons`,
based on `7507556`. The reported findings are resolved. This is approval of the
reviewed source and rendered UI, not a publication or external-provider execution
claim.

## Final targeted coverage

The final source pass completed **28 records with no errors** and produced
**22 screenshots** using installed Chrome and the disposable real Go API /
PostgreSQL fixture at ports 8216/4316. It waited for loaded provider data, fonts,
and the selected component state before measurements and screenshots.

| Surface | Final viewport/theme coverage | Checks |
| --- | --- | --- |
| Embedded `/settings?tab=secret-providers` | 1440 and 390, Paper and dark | Both provider marks, compact navigation, active section visibility, actual bounds, keyboard focus, mobile section selection |
| Personal preferences, Appearance section | 1440 and 390, Paper and dark | Navigation spacing/targets, active focus, mobile selection, Escape restores Account menu focus |
| Infisical edit and review | 1440 and 390, Paper and dark | Smaller mark in disabled trigger and review value, contrast/proportions, labels and page bounds |
| New provider selector | 320, Paper and dark | Full Vault/OpenBao selected label, open menu with both marks, Infisical selected state, keyboard selection, touch selection, Escape/focus restoration |

Earlier completed provider checks are retained: embedded and standalone lists,
new/edit configuration and review for both provider kinds, and selector
interaction at 1440 in both themes; the 390 Paper run completed lists, both new
forms/reviews, and selector interaction. Those comprise 32 successful records
before the final Infisical size and Settings-spacing refinement. The final
samples above replace their scale/navigation evidence; unchanged form and list
behavior is reused. No full Cartesian matrix is claimed.

## Findings and requested refinements resolved

1. **The combined selected label was truncated on mobile.** At 320px the value
   had 180px available for 255px of single-line content, hiding OpenBao entirely;
   390px still clipped the final word. The provider-scoped flexible trigger now
   wraps the complete name. Final 320px checks in both themes measured a 246px
   trigger, 57px height, and equal 180px client/scroll widths with no hidden text.
2. **Provider menu artwork had excessive separation from its label.** The
   inherited 24px gap plus 8px wrapper margin created 32px of space. The scoped
   menu rule now yields an 8px gap. Both options stay inside the viewport and
   align consistently; open-menu screenshots remained mounted during capture.
3. **Infisical appeared oversized relative to the other marks.** Following the
   user's feedback, its width changed from 42px to 32px. The unmodified 91:43
   artwork is contained at approximately 32 by 15.1 visible pixels within the
   shared 48 by 20 slot. Its shape remains undistorted and readable in Paper and
   dark, including disabled edit triggers and review rows. Vault uses its
   monochrome foreground mask; OpenBao keeps its original colors and contained
   proportions. Actual screenshots confirm that all three marks remain distinct.
4. **Settings and preferences navigation needed tighter spacing.** Buttons and
   group labels now have zero horizontal padding; button vertical padding is
   6px. Both desktop themes measured 36px button height, and both emulated coarse
   pointer mobile cases measured 44px. Workspace mobile navigation has a 16px
   item gap and keeps the selected section fully visible within its own scroller.
   Content retains the shared 24px desktop / 16px mobile page inset.

Keyboard selection, touch selection, and Escape focus restoration passed for
both 320px picker themes. Settings and preferences retain the shared red active
text. Measured active buttons have no border or background fill; the visible
red rectangle in focused screenshots is the keyboard-only outline. Unfocused
selection styling was not replaced with a border or filled state.

## Source and verification limits

Source coverage includes the shared provider icon, provider selection field,
review value, icon provenance/assets, and the scoped rules in `console.css`,
`settings.css`, and `shell.css`. The existing shared SelectField API is preserved;
provider-only wrapping/spacing does not introduce another selector. No new
runtime dependency, polling behavior, or backend operation was added.

The fixture contains explicitly disposable providers at nonresolving
`example.invalid` endpoints and has no Kubernetes client or workers. Review
visited real list/detail data and used temporary new-form entries only through
the review step. It did not create, save, rotate, delete, resolve, or deploy a
provider. Real credentials stayed out of logs and screenshots; authentication
state was kept in browser memory. No production account, provider, workload,
cluster, or browser setting was changed.

Chrome viewport/touch emulation is the browser coverage; physical devices,
Safari/Firefox, all Settings sections, and unrelated dashboard route families
were not rerun. The earlier provider workflow review remains the evidence for
unchanged save/failure/conflict/delete behavior. No-JavaScript dashboard operation
is not claimed. The implementer reported passing build, typecheck, formatting,
dashboard tests and Go tests; this independent approval follows screenshot and
interaction inspection rather than relying on those checks.

One unpaced exploratory run reached the real API's request limiter. The error
screen was identified as `rate_limit`; unfinished cases are excluded. Final
navigation was paced and all 28 records passed without modifying the limiter.
Earlier incomplete exact-label and deferred Radix-focus harness runs are also
excluded. All reviewer browsers were closed; shared fixture services remain
under the implementer's control.

Evidence is in ignored `work/provider-brand-icons-review/`. The `final/`
subdirectory contains `review.mjs`, `results.json`, `review.log`, and the 22 final
screenshots. Parent-directory evidence preserves initial defects and completed
form/interaction coverage. No product source changes were made by the reviewer.

## Standing checklist

The standing checklist is copied below from the dashboard review gate. Blank
boxes are the reusable template, not unresolved findings; the scoped completed
review above records this pass. Unrelated routes and Kubernetes operations were
not retested.

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

