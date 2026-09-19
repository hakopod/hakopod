# Requests UI review — 2026-09-20

Independent UI reviewer: notification_ui_review agent. Scoped review passed after the findings below were fixed and retested. Followed AGENTS.md, Hatch contributor/public component guidance and the standing checklist.

## Coverage and evidence

Rendered the actual Requests component, routing diagram, shared Workspace shell, navigation sheet and command palette with dashboard CSS and self-hosted theme attributes. The local fixture supplies synthetic identity, projects, requests and routing records. All API requests are intercepted and every non-localhost network request is blocked. No live application, ingress or external endpoint was contacted by this review.

- Dark and Paper themes: global requests, service requests/routing, custom filters, scoped filters and request details at 1440, 390 and 320 pixels. Empty routing/log, denied API response and loading states rendered at 390 pixels in both themes.
- Actual shared shell and added Requests navigation at 1440, 1280, 1024 and 390 pixels in both themes. Desktop navigation fits one header row at 1280; mobile navigation sheet and Requests command-palette result remain reachable. Active Requests navigation uses theme red without selected fill or underline. Command palette opens with the keyboard shortcut and Escape dismisses it.
- Final evidence comprises 56 screenshots, three contact sheets, `results.json` and `shell-results.json` under ignored `web/work/requests-review/`. Contact sheets and full-resolution desktop, mobile, details, routing, empty/error and shell/navigation/palette captures were inspected. Loaded captures wait for expected request controls/data and fonts; dialog captures wait for animations. Loading and denied captures are explicit test cases, not successful-data evidence.
- Real bounds: document width equals viewport width in every measured global, service and shell case. Heading inset is 24px desktop and 16px mobile. Long paths and IPv6 values wrap within their components; request tables scroll horizontally within their container. Inspector dialogs remain inside the viewport and scroll their body. Keyboard focus uses the visible theme-aware outline.
- Pagination Older → Newer, historical-page paused indication, Pause updates, host/path search, inspector open/Escape close and service-host filtering were exercised. Host filtering works by Enter on desktop and actual touch taps at 390/320 in both themes after the overlay fix.
- Initial project/environment/application/service filters remain in the request URL. Clear service filter removes environment/application/service while retaining project. Custom HTTP 404, 1800-second and TRACE values remain visible and remain in the query rather than silently resetting.

Source coverage includes `routes/requests.tsx`, `components/requests.tsx`, shared shell and command palette, plus service-detail Requests tab placement and `logs:read` permission gate. The fixture mounts service-mode Requests directly; the complete service-detail parent and route-loader transition were not exercised end to end. No clipboard write was requested. This UI review does not independently verify backend authorization, storage, retention, collector gaps, Kubernetes traffic capture or actual ingress routing; the implementation agent records those tests separately. All records here are explicitly artificial.

## Findings resolved

1. Missing asynchronous project options caused initial filters to widen silently to all projects. Preserve the selected project while its options load or remain unavailable.
2. Exact status/custom time/uncommon method deep links lacked options and displayed defaults. Preserve these values with explicit options.
3. The raw search input lacked shared input styling. Use the shared Input component.
4. Inspector content touched the dialog edges. Add internal body padding while retaining bounded scrolling.
5. Error text and muted surfaces used absent Tailwind tokens. Use the configured error-text and surface tokens; 503 is visibly distinct from 201 in both themes.
6. Routing arrows pointed sideways in the stacked layout, and unbroken IPv6 text exceeded narrow cards. Rotate arrows below the desktop breakpoint and wrap address text.
7. Rotating stretched arrow grid cells created tall invisible hit boxes over host buttons. Shrink and center the decorative arrow elements and disable their pointer events. Touch filtering was then retested successfully without forced clicks.

No unresolved findings remain in the covered states. Fixture browser contexts and the localhost preview were stopped after review. Production source fixes were made by the implementation agent; this reviewer added only the review record and ignored fixture/evidence files.

## Standing checklist

The following is the reusable global gate copied from the standing checklist. The coverage above records this scoped pass; blank boxes do not imply that unrelated route families were retested.

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
