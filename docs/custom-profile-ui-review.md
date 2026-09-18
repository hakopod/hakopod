# Custom service profile label UI review

Reviewed independently on 18 September 2026 against base `13bdbbd` and the
custom-profile changes in `web/src/lib/service-resources.ts`,
`web/src/routes/applications.$applicationId.tsx`,
`web/src/components/service-detail.tsx`, and
`web/src/components/application-topology.tsx`.

**Result: approved. No actionable finding in the changed labels or their layout.**
Services with explicit nonempty CPU or memory overrides display `Custom` in
service cards, `Custom profile` in the Runtime panel header, and `Custom` in the
topology inspector. The base size remains available for inherited resource fields.

## Verified cases

Real route and component sources were copied into an ignored isolated Vite
harness and rendered through TanStack Router, the real dashboard shell, React
Query and the normal Hatch components. API responses were intercepted fixtures;
the page is named `Profile label fixture` and the harness includes an isolated
fixture notice. Unobserved runtime and unavailable metrics were retained. No live
API or VM was contacted, and final request logs contain GET requests only.

Each case below was checked in all three changed views, in Paper and dark themes
at 1440 × 950, 390 × 950 and 320 × 950: 126 label assertions across six viewport
and theme combinations.

| Fixture service | Resource configuration | Expected summary |
| --- | --- | --- |
| `api` | All four CPU/memory overrides; base size `small` | Custom |
| `worker` | CPU request only; base size `medium` | Custom |
| `setup` | Deployment job with memory limit only; base size `medium` | Custom |
| `empty` | Empty resource object; base size `medium` | medium |
| `blank` | Four empty resource strings; base size `medium` | medium |
| `default` | Resources omitted; size `medium` | medium |
| `fallback` | Resources and size omitted | small |

The service-card link opened the correct service through Enter on desktop and
touch on mobile. The card focus outline remained visible. Every topology node
was selected using keyboard focus plus Enter on desktop and touch on mobile;
its pressed state, inspector heading and profile updated together, including
transitions between custom and default profiles. The selected desktop node had
a visible keyboard outline. The existing topology canvas scrolls inside its
mobile container; its inspector and changed labels stayed within the viewport.

Actual bounds were recorded for card facts, service Runtime chips, inspector
facts and headings. There was one main heading per route and no document
horizontal overflow in any case. No measured changed label extended outside the
viewport. Screenshots show readable labels without clipping or overlap, compact
cards capped at four columns on desktop, and 24px desktop / 16px mobile content
insets. Both desktop card grids and representative mobile cards, Runtime panels,
job panels and topology states were inspected at full resolution.

A focused read-only form check confirmed that the full-override API still shows
`Small` and the CPU-only worker still shows `Medium` in the Size control, while
their explicit CPU request values remain present. It did not submit a plan or
change resource settings. This is preservation of the base-profile display,
not a repeat of the resource-form acceptance matrix.

## Evidence and limits

Ignored local evidence is in `web/work/custom-profile-review/`:

- `prepare.py`, `serve.mjs`, `fixtures.mjs` and `review.mjs` reproduce the harness.
- `results.json` and `review.log` record all six passing scenario groups,
  measurements, expected labels and requests. The final run has zero browser
  errors, unknown fixture requests, errored fonts or captured loading stacks.
- `source-sha256.txt` identifies the four reviewed source files.
- 68 final screenshots include `cards-light-1440.png`, `cards-dark-1440.png`,
  `card-focus-light-390.png`, `detail-api-dark-390.png`,
  `detail-setup-dark-320.png`, `topology-setup-dark-1440.png`,
  `topology-fallback-dark-320.png` and `base-size-worker-light-390.png`.

An exploratory pass encountered a Vite hot-reload / duplicate React-root error.
Its results are excluded. The complete final matrix was repeated with HMR and
file watching disabled and a separate Vite cache; it passed cleanly. Earlier
probe captures and `initial-results-discarded.json` are diagnostic material only.

Review used isolated headless installed Chrome and emulated touch. It does not
claim Safari, Firefox, physical-device or screen-reader coverage, live cluster
behavior, deployment, or newly verified unrelated forms, authentication,
permissions, failure states and motion. The change adds no polling or animation;
existing runtime behavior was not expanded. The implementer separately reported
passing dashboard typecheck, tests and build after rebasing. The reviewer changed
only this record and ignored evidence, with no commit or push.

## Standing checklist

The standing checklist is copied below as required by the dashboard review
instructions. These reusable blank gates are not a claim that unrelated behavior
was re-audited; the scoped completed result and limits are recorded above.


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

