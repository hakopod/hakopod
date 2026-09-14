# Git provider entry UI review — 2026-09-14

Independent reviewer: `/root/probe_setup_review`. Reviewed against root/Hatch contributor instructions and the shared UI checklist.

## Result

Provider entry and redirect checks pass. Default GitHub/GitLab cards open the matching App setup without token inputs; named connections retain edit/resume behavior. This pass uses real dashboard components and nested route modules with isolated artificial API records.

## Coverage

- Dark and Paper at 1440 and 390 pixels. Real Settings → Git connection hierarchy and Settings → Integrations → provider hierarchy preserve their original search validators.
- `entry.json`: eight provider-card navigation checks (desktop keyboard Enter, mobile touch), twelve named-card regression checks, sixteen redirects from old integration URLs and exact `github-default`/`gitlab-default` IDs, and four invalid-provider fallback checks. Expected loaded content, one h1 and document bounds checked; no page errors.
- Source/import setup links reviewed in source. No token/private-key input in the new App paths. Real provider registration and live API authorization were not exercised.
- Final card layout and keyboard-focus checks cover both themes and widths, including configured/unconfigured provider copy. Screenshots inspected for hierarchy, readable copy, alignment, boundaries and focus.

## Findings resolved

1. Direct legacy-ID redirects stalled rendering. Replaced the query-dependent Navigate with an effect using stable dependencies; all direct defaults then redirected correctly.
2. Invalid provider search could survive router search merging and render GitLab. Explicit validator fallback and component guard now select GitHub.
3. Mobile named-card statuses expanded the icon column, misaligning titles against provider cards. Both trailing labels now use the shared integration-state placement.

## Evidence and limits

Local ignored evidence: `work/ui-migration/git-provider-review/entry.mjs`, `entry.json`, `entry-layout.mjs`, `entry-layout.json`, and `screenshots/entry-*.png`. Final cards and focus screenshots use `entry-final-{cards|focus}-{dark|light}-{1440|390}.png`; setup examples use `entry-{github|gitlab}-new-{theme}-{width}.png`.

No real provider credentials, browser session, operator cluster, or application data changed. The isolated fixture and owned headless browsers were stopped. This review does not repeat the earlier callback/save coverage recorded in [Git App setup review](git-app-setup-ui-review.md).

## Standing checklist

Copied reusable gate below; the scoped results above distinguish this pass from unrelated dashboard behavior.


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

