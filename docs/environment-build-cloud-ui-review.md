# Environment and Cloud build UI review

Independent review completed on 2026-09-16 against the shared dashboard in
`/tmp/hakopod-git-picker` composed with the private Cloud overlay. No open UI
blockers remain in the reviewed changes.

The review used isolated headless Playwright and the real route/components with
explicitly synthetic, intercepted API data. No customer accounts, credentials,
workloads or workflows were accessed. No deployment was submitted, provider
settings changed, or test email sent.

## Rendered coverage

Every main case ran in dark and Paper themes at 1440 and 390 pixels. Screenshots
were taken after required content, lazy modules, queries and fonts loaded.

| Area | Cases and checks |
| --- | --- |
| Application and service environment editors | Eight cases, including initial state, file/paste import, duplicate failure, upload failure, plan failure and review. |
| Container deployment form | Four cases with masked imports, mode conversion and disabled controls during secret upload. |
| Compose interpolation | Four cases with file/paste import, literal substitutions, duplicate rejection, sensitive-value rejection and failed-conversion draft retention. |
| Source-build runtime form | Four cases with file/paste imports, secret upload failure and build-save failure. |
| Shared-image builds | Four cases with worker selection, refresh, failed save, branch help and registry creation/refresh controls. |
| Service Environment and Secrets views | API and worker in eight cases, both tabs: inherited values, empty-string overrides, inherited secret references and other affected services. |
| Log explorer and recent output | API, worker and scheduled-job in twelve cases, each with query, selected pod and recent-tail views. |
| Cloud installation settings | Eight operator cases covering no selected workspace and selected internal workspace, SMTP and OAuth panels; both editors in four additional viewport/theme combinations. Ordinary tenant navigation and direct-route denial also checked. |
| Edge widths | Six cases at 320, 1280 and 1920 pixels, including wrapped mobile logs and entry inspection. |

Application plain variables require explicit `inject_env = true`. Tests cover
both an omitted false value, matching the Go JSON representation, and explicit
enablement during review. With injection disabled, the worker's plain defaults
disappear while independently inherited secret references remain available.

## Interaction and data checks

- File and pasted `.env` imports produce individual rows. Multiline values and
  literal references such as `${NAME}` survive. Duplicates leave existing rows
  and the pasted draft intact.
- Sensitive imports use password inputs. Plan, build and generated TOML bodies
  contain only secret references, never the synthetic secret values.
- Failed secret upload, partial upload and failed plan/build requests preserve
  entered values. Retrying a partial upload reuses the same generated references
  with the exact application, project and environment scope.
- Form → TOML → Compose → form → TOML retains plain and secret drafts and
  produces the same references and TOML. An existing bound secret-name collision
  is rejected before any write.
- Compose interpolation accepts `.env` files and pasted text without evaluating
  references or shell expressions. Sensitive substitutions are rejected with
  directions to use application secrets; no secret upload is attempted.
- All container form inputs, mode changes and review actions are disabled during
  secret uploads. Reuse selections survive service refresh and failed saves.
  Existing command and environment fields stay omitted from the reuse request
  when the user chooses to preserve them.
- Registry creation opens a new tab with `noopener`; refreshing credentials
  retains the source-build draft. The branch instruction is visible.
- Keyboard file selection and paste disclosure, mobile navigation, Escape,
  permission denial and log inspection were exercised. Mobile log wrapping puts
  the message within the viewport instead of requiring horizontal scrolling.
- Operator SMTP/OAuth forms are available without a selected workspace and in
  the internal workspace. An ordinary tenant cannot see those navigation items
  or cause the direct SMTP route to fetch installation settings.

## Layout and screenshot findings

The page inset is 24 pixels on desktop and 16 on mobile. The Cloud header stays
on one desktop row and ends beside search: the gap is 10 pixels at 1280 and
16 pixels at 1920. No page overflow was found in the recorded cases. Long values
wrap or scroll within their component. Active navigation stays red without a
selected underline; mobile navigation retains the selected section.

The reviewer found these issues; each was fixed and retested:

1. Inherited override labels read saved configuration instead of the current
   draft. They now respond when an override is added or removed.
2. Mode conversion allowed edits and competing actions during secret uploads.
   The entire container editor now disables while the operation is pending.
3. Build registry copy contradicted automatic matching-credential behavior.
   The default and help now describe automatic matching and explicit preference.
4. Recent log output inherited the hairline color and was nearly invisible in
   Paper. It now uses the foreground color; both themes were visually checked.
5. The application editor treated an omitted `inject_env` as true although the
   backend omits false. It now starts unchecked until the user opts in.
6. Shared variable help implied Compose substitutions became runtime variables
   and secret references automatically. The Compose variant now explains that
   these values are substitutions only and require explicit secret references.

## Evidence and limits

Ignored local evidence is in `web/work/env-inheritance-review/`, including
`results.json`, `form-results.json`, `build-results.json`, `read-results.json`,
`log-results.json`, `interaction-results.json`, `edge-results.json`,
`injection-results.json`, `partial-results.json`, `reuse-results.json`,
`settings-results.json`, `roundtrip-results.json`, scripts and PNGs.
`compose-results.json` records the interpolation checks.

Setup captures named `initial.png`, `deploy-initial.png`, `build-initial.png`,
`smtp-initial.png` and `logs-initial.png` are not approval evidence. Early fixtures
with incomplete metadata were corrected and the affected matrices rerun.

This is UI and request-shape verification. Actual Kubernetes delivery, registry
authentication, secret persistence, backend authorization and production rollout
require the separate integration checks. No production success is inferred from
fixture results.


## Standing checklist

The blank boxes below are the reusable standing checklist, as in the source.
The completed, scoped results are recorded above; unrelated route families are
not represented as newly retested.

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
