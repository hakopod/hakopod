# Managed binding options UI review

Review date: 6 October 2026. Independent review complete for the affected UI.
The findings below are resolved. This is isolated browser evidence, not live
cluster or production acceptance.

## Scope and outcome

The real nested database connection route, application Topology and service
Environment tab were rendered in the actual dashboard shell. Dark and Paper
themes were exercised at 1484, 390 and 320 pixels. The shared 24px/16px inset,
field and action bounds, desktop row alignment, one page heading, visible
keyboard focus, named help, emulated touch activation and long references passed.
Representative full-resolution screenshots were inspected after measurements.

The original matrix contained 122 cases: 108 passed, 12 exposed harness issues,
and two exposed the initial saved-secret-list failure described below. A final
36-case run on the corrected source passed all cases and produced 78 captures.
It repeated all prior failures and relevant password/review paths and added four
busy/touch/application-error cases. Two additional recovery runs passed, proving
that retry after a secret-list error restores selection and review eligibility.
The combined index therefore contains **126 unique passing cases across the
baseline and focused final runs**, not a single 126-case final-source run.

| Coverage | Verified behavior |
| --- | --- |
| Connection form | Defaults, custom user/database, saved reference, SSL selection, correct exact accessible field names, aligned controls and no clipping |
| Password workflow | Masked entry survives failed create; successful save clears raw entry; failed plan retains reference; retry avoids another save |
| Scope and validity | Application change clears password/reference; changed database revision and expired review disable submission; links retain loaded scope |
| Guarded engines | Pooled PostgreSQL custom login/database is blocked without discarding values; direct endpoint restores review; Redis indexes and cluster consent, MongoDB consent, MySQL/Vitess TLS guidance, Oracle/ClickHouse fields and legacy TLS warning rendered |
| Submission | Explicit redeployment confirmation, rejected connect preserves draft/review, held secret/connect requests cannot double-submit |
| Recovery | Database and application load failures, empty application list, unready database, denied permission, loading, no saved secrets, initial secret-list error and successful retry |
| Summaries | Application has both bindings; service shows only its own binding; user/database/SSL/reference text wraps at 320px |
| Help/input | Keyboard selection activation, Tab order and outline, named focus help and Escape, actual emulated touch selection and help toggle/dismissal |

## Resolved findings

- Pooled PostgreSQL permits only the managed app login/database. The UI preserves
  custom values, blocks review with direct-endpoint guidance and restores
  eligibility when a direct endpoint is selected.
- TLS help now requires configuring client trust for the mounted database CA and
  hostname verification. It no longer implies arbitrary clients automatically
  trust the CA. Reduced-verification and plaintext warnings remain visible.
- Nested field help had become part of accessible input names. Exact Username,
  database-label and Existing password names now resolve while descriptions
  remain associated.
- An initial secret-list error was suppressed when both the selected reference
  and newly saved reference were empty. Suppression now requires a nonempty
  newly saved matching reference. Error help and Retry are visible, Saved
  password is disabled during failure, and successful retry enables selection.

## Evidence and inspection

Local evidence is under `.local/managed-binding-review/`:

- `evidence/results.json`: original 122-case run and source fingerprint.
- `final/results.json`: all 36 final-source correction/edge cases passed.
- `recovery/results.json`: both explicit secret-list recovery cases passed.
- `reviewed-results.json`: combined case index with source snapshot per case.

The final connection-route hash is shown below; the other three binding files
were unchanged between the baseline and focused reruns. Every run rejects
source changes during execution. The final route change was limited to the
secret-load error predicate and its use in validation.

| Product source | SHA-256 |
| --- | --- |
| `web/src/lib/database-binding.ts` | `127533cb7cfc9503f70e59a1910af0ac119250cddf9babf8187976542a4b4ea3` |
| `web/src/components/database-binding-options.tsx` | `189a6688329b71e9072e7aa5af7dc29eae3bf5cc037943d2d5bb00402a3a88a4` |
| `web/src/components/managed-database-connections.tsx` | `dcbe320cbf350bfec83d86ea273640568adb67e4227c100bfb8f2bc5ae6d3a79` |
| `web/src/routes/databases.$databaseId.connect.tsx` | `adfe1fc108b17149a278e989e4437a80e6fddf3679bef3547b3cbdb828c8716d` |

Accepted full-resolution inspection included these captures:

- `final/screenshots/custom-review-rejection-dark-1484-review.png` and
  `custom-review-rejection-light-390-rejected.png`: review hierarchy, reference
  disclosure, visible failure and retained confirmation.
- `final/screenshots/default-and-keyboard-dark-1484-help.png`,
  `default-and-keyboard-light-320-help.png` and
  `binding-interactions-dark-390-help.png`: visible named keyboard/touch help,
  focus outline and viewport bounds.
- `final/screenshots/application-summary-light-320.png` and
  `evidence/screenshots/service-summary-dark-320.png`: compact summaries, long
  reference wrapping and resource-scoped actions.
- `evidence/screenshots/default-and-keyboard-dark-1484-empty-draft.png` and
  `engine-mysql-light-1484.png`: paired rows, empty/disabled state and driver TLS
  guidance.
- `final/screenshots/secrets-error-light-390.png`: disabled saved-password field,
  visible recovery guidance and correctly styled Retry action.

The coordinating reviewer separately inspected the original desktop default,
320px service summary and Paper MySQL captures and reported no new findings.
The coordinating reviewer also inspected final Paper mobile review/error and
320px help captures and reported no additional findings. Later full-page
screenshots start at scroll position zero; open-help screenshots
use viewport capture. Earlier stitched screenshots that placed the offscreen
fixed skip link into the image were excluded as visual approval evidence. The
original generic geometry check incorrectly treated offscreen topology nodes as
clipped controls; the final runner measures the intentional horizontal scroller
separately while continuing to enforce bounds on connection fields and links.
A keyboard selection timing issue and duplicate inline/toast error locator were
also corrected in the harness without changing product behavior.

All compilation and browser execution used the bounded reserved VM checkout.
The harness marks artificial data, intercepts every fetch, blocks external
network requests and redacts password request bodies. Connect always fails.
Owned fixture servers stopped on runner exit. Final read-only verification found
all four principal review units inactive and no listeners on ports 4198, 4199
or 4200. These checks do not establish actual
authorization, secret persistence, database login, TLS handshakes, deployment or
production availability. Physical-device and screen-reader testing were not
performed. Unrelated global routes remain outside this review.

## Standing review checklist


Copy this standing checklist into each UI review. The blank boxes are a reusable gate, not the result log; the completed review above records this pass.

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
