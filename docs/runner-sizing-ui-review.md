# Managed Actions runner sizing UI review

## Completed review: 2026-09-25

Independent review of `ManagedActionsForm` and `runner-resources.ts` passed after the final capacity-error change. Source inspection covered the shared form used by catalog creation and existing-service configuration, its resource summary helper, public Hatch inputs and selects, the shared form/page layout, and the shared error component. This is a targeted UI review, not a new review of unrelated dashboard routes.

### Coverage and evidence

The actual components were compiled on the remote VM into a clearly marked UI fixture with artificial API responses. All requests outside loopback were blocked. No production API, account, runner pool, GitHub token, workload or volume was changed.

- Cloud and self-hosted editions, dark and Paper themes, at 1440, 390 and 320 pixels: 12 combinations.
- New runner resources and review, inherited existing resources, partial overrides, and full custom overrides were exercised in every combination. Inherited fields remain omitted in submitted specifications; partial overrides do not become full overrides.
- Custom inputs `250m`, `2`, `1Gi`, `4Gi` and three slots display a 0.75-core / 3072-MiB reservation total. Both `0.25` and `250m` CPU syntax submit correctly. Invalid syntax prevents a plan request.
- Back navigation and rejected plans preserve values. Review receives focus; returning focuses the application input. Resource inputs have associated labels and instructions, visible focus, logical Tab order and usable touch focus.
- Runtime unavailable, missing resource profiles, license unavailable, permission denied and capability lookup failures retain readable guidance and block review. No invented fallback resource profiles are shown.
- The full capacity message was checked in both editions and themes at 390 and 320 pixels. Requested resources, available resources, exact CPU shortage and next action are visible in an expanding inline alert above Review. No duplicate toast is created. The alert has no internal vertical clipping or horizontal overflow.
- The main matrix records 47 captured states; additional measurements cover all 12 combinations, and eight final inline-capacity-error cases. Two theme variants exercise heading-help keyboard focus, Escape dismissal and touch opening. These counts represent UI fixtures and assertions, not real deployment or scheduling tests.

### Layout and visual findings

Measured page padding is 24px on desktop and 16px on mobile. Form content begins at that inset, without a second page gutter. Heading dividers reach both viewport edges. All four new resource inputs are 44px high. There is one h1, no clipped controls or document overflow, and no self-hosted decorative brackets.

Rendered screenshots were inspected beyond geometry assertions: eight contact sheets covered the initial form/review/unavailable matrix, and full-resolution resource-region, mobile review, keyboard-focus, heading-help and final capacity-error captures were inspected. Desktop fields form an aligned two-column grid; mobile fields stack with labels and limits kept beside their inputs. Pool totals and the CPU-sharing warning remain visible. Primary actions retain the configured accent.

Two findings were resolved before sign-off:

1. Materializing inherited resource defaults on submission would lose inheritance. The implementation now submits only existing or edited overrides; fixture assertions verify both omitted and partial configurations.
2. A long capacity error in the shared toast required internal scrolling at 320px. Capacity errors now appear inline in the form and expand naturally. Final captures show the complete recommendation without internal scrolling; unrelated errors keep the existing toast behavior.

The review script initially used an ambiguous label locator for the review region; it was corrected to the focusable wrapper. A separate check initially observed smooth scrolling before it settled. Those harness failures were not application failures; the old toast-scroll experiment is superseded by the final inline-alert checks.

### Evidence location and limits

Remote evidence is retained under `/srv/hakopod-backup-scratch/runner-sizing/ui-review/evidence`: `results.json`, `extra-results.json`, `capacity-results.json`, full-page screenshots, `*-resources-region.png` and final `*-capacity-error-detail.png` captures. The fixture source and `review.mjs`, `extra.mjs`, and `capacity.mjs` sit one directory above. Earlier toast-scroll captures are historical and do not represent the final source.

The server binds only to remote loopback port 14439. Build and browser work were bounded to one CPU and 1500MiB; the static server used 128MiB. No local build, browser process or development server was started. Temporary fixture/browser processes were stopped after the review.

This verifies rendered component behavior against mocked API contracts. Backend resource arithmetic, live scheduling, production rollout, actual runner registration and GitHub job execution require separate evidence. No live capacity values were inferred from the fixture. Unchanged navigation, application grids, endpoint rendering, backup pages and authentication routes were outside this targeted pass; the standing checklist below remains the reference for their own reviews.

## Standing review checklist


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

