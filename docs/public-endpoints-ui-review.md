# Public endpoint lists UI review

Independent review completed on 18 September 2026 against base `04a16db` plus
the reviewed endpoint collector, shared endpoint component, application route
and service-detail changes. Source hashes are retained with the evidence.

**Result: approved after correcting the endpoint dialog body spacing.**

## Scope and coverage

The real application route, dashboard shell, service networking panel and shared
dialog were rendered at `http://127.0.0.1:4262/` using isolated Playwright/Chrome
contexts. Fixtures are labelled `Endpoint review fixture`. API calls were
intercepted, and external endpoint destinations opened a local intercepted
fixture response. No live account, API, VM or external endpoint was changed.

Six complete scenario groups covered Paper and dark themes at 1440 × 900,
390 × 900 and 320 × 900. They checked the application header's two-endpoint preview,
the service networking preview's three rows, both endpoint dialogs, the worker's
empty endpoint state and the new CI guide link in the Deployments tab. Each
route kept one main heading and no horizontal document overflow. Actual header,
network-panel, dialog, link and button bounds were recorded; changed content
stayed within the viewport.

The nine-entry fixture contains custom mappings and generated/named public
addresses from multiple services. A duplicate generated URL is shown once for
its service. The service dialog correctly filters to six API endpoints, while
an entry present only in the verification response is excluded. Long hostnames
truncate with their external-link icon on the same line, and the complete URL
remains in the title and accessible label. Active custom mappings say
`Configured`; verified-but-inactive and unverified mappings say `Needs setup`;
a missing matching status says `Status unavailable`. The dialog explains that
configured routing does not establish DNS, TLS or application health.

Four additional mobile scenario groups checked loading and failed domain-status
queries in both themes. All five configured custom domains correctly showed
`Checking status` or `Status unavailable`, without hiding their addresses. Two
320px many-domain cases displayed 39 entries in the bounded scroll area. Search
found an exact long hostname, a named tenant and a service-scoped result;
unmatched search showed the visible empty-result message, and clearing it
restored the list and count.

Keyboard Enter and touch opened the dialogs. Escape closed them and restored
focus to the relevant trigger after the close lifecycle completed. Search focus
remained visible. Copy controls were checked against an isolated clipboard stub
and requested the complete URL. Four further interaction cases at 1440px and
320px in both themes verified scrolling to later entries by keyboard or touch
and opening the full long URL with Enter or a tap. Touch advanced the internal
list by 328/330px; keyboard navigation reached its 1601px end. External-page
responses were intercepted and identified as fixtures.

## Resolved finding and screenshots

The initial endpoint dialog placed its search control, count and rows flush
against its edges. The implementer added the shared `dialog-body` inset and
spacing. Fresh screenshots at every width/theme show aligned body content and
a clear focus outline inside the dialog. The final matrix passed with zero
browser errors or unknown fixture API requests. Earlier incomplete fixture
responses and immediate-before-focus-restoration assertions were corrected in
the harness; their initial results and failure captures are excluded.

Full-resolution inspection covered both desktop header/network layouts,
390/320px dialogs, mobile service rows, status states, the 39-entry list and the
CI link. There are 26 final screenshots under ignored `web/work/endpoint-review/`,
including `header-light-1440.png`, `header-dark-320.png`,
`dialog-light-390.png`, `dialog-dark-320.png`, `network-light-1440.png`,
`network-dark-390.png`, `many-light-320.png` and `ci-link-dark-320.png`.
`results.json`, `interaction.json`, their scripts/logs and `source-sha256.txt`
record the exact run. The source was imported directly; existing dependencies
and the previous Vite cache were reused to minimize disk use.

Review used isolated headless installed Chrome and emulated touch. It does not
claim Safari/Firefox, physical-device, screen-reader, live DNS/TLS or backend
acceptance. Unrelated forms, account flows and cluster behavior were not
re-audited. The implementer reported passing typecheck, build and 83 UI tests.
The website CI guide is reviewed separately in the website's `docs/ui-review.md`.
The reviewer changed only review records and ignored evidence, with no commit
or push.

## Log wrapping default — 18 September 2026 addendum

Independent approval also covers the two initial-state changes in
`web/src/components/logs.tsx` and `web/src/components/live-logs.tsx`. Application
and service Logs routes were freshly rendered in Paper and dark at 1440 × 900
and 390 × 900, covering Query and Live tail in every combination: 16 mode checks.
All began with Wrap checked. A clearly labelled long synthetic line rendered
with `white-space: pre-wrap` and `overflow-wrap: anywhere`, spanning multiple
lines inside the panel. Space on desktop and touch on mobile unchecked Wrap;
the text returned to a single unwrapped line. Document width remained within
the viewport in both states. No browser errors or missing fixture responses
occurred.

Sixteen screenshots were captured and representative desktop/mobile Query and
Live tail views were inspected at full resolution. Evidence is in the same
ignored folder: `log-wrap.mjs`, `log-wrap.json`, `log-wrap.log` and
`log-wrap-{application,service}-{query,live}-{light,dark}-{1440,390}.png`.
The fixture supplies intercepted read-only log-query responses and recent-log
text; no live logging backend or persistent preference was exercised. Existing
toggle behavior remains available. This addendum does not repeat the broader
log filtering, streaming, retention or installation-log acceptance matrix.

## Isolated MySQL public-endpoint candidate — 1 October 2026

An independent reviewer approved the isolated candidate source for the database
public-endpoint route, generated dashboard/SDK types, browser API proxy and
machine API proxy. The review verified that capability data is scoped to the
loaded database, uses the generated route descriptor, and fails closed for new
publication when the capability request fails. Endpoint inventory, a retained
publication review, operation tracking and revocation remain available in that
state. The MySQL capability describes MySQL Router routes but remains unavailable
until native Router TLS, routing and revocation qualification completes.

Rendered evidence is in ignored
`work/database-enterprise/mysql-public-endpoint-final-review/`. Synthetic,
intercepted fixtures covered the ready PostgreSQL route, retained-review plus
failed capability refresh, MySQL-unavailable controls, revocation review opened
with Enter and visible region focus, and a rejected revocation with the actual
API error envelope. The changed MySQL-unavailable controls were inspected in
Paper and dark at CSS widths 1484px and 320px. The 320px disabled select may
truncate its long option, so the final v5 capture also shows the wrapping
`MySQL Router · Read and write` summary beneath it. All accepted bounds report
document widths within their viewport.

The malformed `paper-320-mysql-revocation-rejected-v4` capture is excluded
because its fixture returned `{ message }` instead of the API error envelope
and rendered a TypeError. The v5-final replacements use the proper error
envelope and show the rejection beside the still-open revocation review. No
actionable source, layout or copy finding remains.

The source owner reports v5 dashboard typecheck and production build passed.
The v4 run passed 60 server/proxy tests and 200 UI tests; the v5 SDK suite
passed 46 tests. The v5 display-only summary did not repeat the UI test run.
This is dashboard and proxy-surface validation with synthetic fixtures, not
native MySQL qualification, public reachability, a production API claim, or
publication approval. Native touch remains unverified.


## ClickHouse public-endpoint presentation candidate — 1 October 2026

An independent final UI review approves the ClickHouse public-endpoint presentation candidate. The accepted evidence is synthetic and intercepted only, under `work/database-enterprise/clickhouse-public-endpoint-final-review/`; it does not establish ClickHouse native/HTTPS capability, public reachability, backend acceptance, deployment, or production behavior.

The reviewer inspected the dark and Paper unavailable states at CSS widths 1484px and 320px, each at top and bottom scroll positions. The unavailable message is clear, the controls and review action are disabled, text remains readable, and all recorded viewport widths remain within their documents (`1477px` of `1484px` on desktop, `313px` of `320px` on the unavailable and review mobiles). The Paper 320px protocol menu exposes both readable choices, marks the selected native route, and has the expected list semantics in the supplied accessibility capture.

The corrected Paper HTTPS review v2 final was inspected at 1484px and 320px at top and bottom scroll positions. The inventory and review content retain a clear hierarchy; the confirmation region is visibly focused, address and hostname wrap inside the mobile layout, the warning remains visible, and Back, Edit controls, and Publish endpoint retain distinct action hierarchy. No actionable layout, clipping, contrast, focus, semantics, or copy finding remains in this scoped evidence.

Excluded evidence: `paper-320-clickhouse-https-review-v1-{0,1}` and `paper-1484-clickhouse-https-review-v1-{0,1}`. Those captures correctly show stale-review rejection because the initial fixture did not put its reserved endpoint into inventory; they are not evidence for the completed review.

Coverage is limited to the listed synthetic desktop/mobile captures, theme rendering, recorded element bounds, and the supplied menu/focus accessibility evidence. Native touch remains unverified, as do physical-device assistive technology, other browsers, live APIs, and all cluster or public-network behavior. No application code was changed for this review.

## Oracle TCPS presentation — 1 October 2026

The independent review passed for the Oracle protocol label, unavailable state
and shared review layout. Fourteen final screenshots cover dark and Paper at
CSS widths 1484px and 320px: unavailable and reviewed states, settled route
menus and mobile keyboard help. The actual theme and element bounds are
recorded with the captures. Only
`work/database-enterprise/oracle-public-endpoint-final-review/v2-final/` is
accepted; earlier theme and animation captures are excluded.

The unavailable reason stays visible beside disabled publication controls.
The review identifies Oracle TCPS and shows the single-member restart warning
before publication. Hostnames and controls remain inside the mobile layout.
Space, Enter and Escape were checked for menus and help. The independent
reviewer inspected the final screenshots and recorded bounds without direct
access to the capture browser.

This is synthetic presentation evidence. No public endpoint was published or
revoked, and the Oracle availability gate remains disabled. Native touch,
physical assistive technology, other browsers and native Oracle public-network
behavior remain unverified. The integrated dashboard typecheck, 60 server
tests, 200 UI tests and production build passed on the VM; SDK generated checks
and all 46 SDK tests also passed.

## Standing checklist

The required reusable checklist follows. Its blank boxes are not a claim that
unrelated behaviors were retested; the completed scoped result is recorded above.


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
