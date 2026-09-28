# Managed Actions temporary workspace UI review

Independent review completed on 2026-09-28 for the temporary workspace field in
`web/src/components/managed-actions-form.tsx`. No product UI findings remain.

## Scope and evidence

The reviewed change adds a whole-number GiB input, sends its value in the plan,
and displays the per-slot amount before deployment. Source coverage included the
catalog template's creation and existing-pool branches, `ManagedActionsForm`,
`FormPage`, `FormSection`, the shared Input and validation wrapper, shared Buttons,
and the Hatch styles and components they consume.

The VM fixture rendered the actual component and shared styles with both
self-hosted and Cloud edition settings. Its header explicitly identified the
records as artificial UI fixtures. It used isolated HTTP responses for
capabilities, planning and rejected deployments; it did not contact production
APIs or create runners. Full Cloud navigation, authentication, live entitlements,
Kubernetes placement and GitHub jobs are outside this UI sign-off.

All building and browser execution ran on the Cloud VM under
`/srv/hakopod-backup-scratch/private-runners-20260928/ui-review`. The review used an
isolated loopback server on port 14449 and existing VM dependencies. No local
package, dashboard or Docker build ran. The fixture browser and loopback server
were stopped after the review; evidence and scripts remain in the task directory.

- `build.log`: successful Vite fixture build.
- `review.mjs` and `evidence/results.json`: 57 passing recorded render and
  interaction cases, including measured element bounds.
- `evidence/`: 77 full-page and component screenshots; `contact-01.png` through
  `contact-10.png` cover the complete screenshot inventory. All contact sheets
  were inspected, followed by full-resolution resource, review, validation and
  focus captures.
- Reviewed form SHA-256, equal in the local checkout and VM source:
  `572bab184b5169ec9b6c0e9b57de14b601476454e3151c56d99c7e7bef6f4e24`.

## Completed checks

| Coverage | Result |
| --- | --- |
| Self-hosted and Cloud; dark and Paper themes; 1440, 390 and 320 pixel widths | Passed creation, review, saved existing-pool and legacy-pool scenarios. |
| Page layout | Form retains a 24px desktop / 16px mobile outer inset. Heading divider reaches both viewport edges. One h1 and no horizontal document overflow. |
| New field layout | 44px input height, with controls inside the viewport. At 320px the input is 246px wide. The per-slot range, job cleanup and separate Docker limit remain readable. |
| New-pool default | Displays 2 GiB. Choosing 8 submits numeric `workspace_size_gib: 8`; review and deployment request retain 8. |
| Existing pools | Saved 12 GiB remains 12. A legacy spec without the field uses 2. Both survive another review without silently changing the size. |
| Validation | Empty, 1, 17 and 2.5 are rejected before a plan request. Both endpoints, 2 and 16, are valid. The entered value is retained and the visible validation message is associated through `aria-describedby` and `aria-invalid`. |
| Review and failures | Review shows the chosen GiB per slot. Back restores the input. Plan and deployment failures preserve it. A stale application revision retains the saved 12 and reports the conflict. |
| Keyboard and touch | Arrow keys step by one; Tab moves from the field to Cancel and Review. Tapping focuses the input. Review receives focus; returning focuses the application name. |
| Focus styling | Self-hosted shows the shared 2px outline and no visible corner brackets. Cloud retains its allowed bracket and background focus treatment in both themes. |
| Busy and denied states | The new input and review action disable during planning. Denied, unlicensed and runtime-unavailable cases keep review disabled. Capability failure remains visible and offers retry. |
| Component use | Uses the public shared Input and Button wrappers. Existing selection controls remain SelectField. No new CSS, dependencies or runtime service. |

The initial browser script expected an outline in both editions. Source and
rendered inspection confirmed that Cloud deliberately uses the existing Hatch
bracket and background treatment. The assertion was corrected to recognize that
allowed styling, and the complete run passed. This was a fixture correction; no
product change was required.

Loading and missing-scope presentation, the full navigation shell, unrelated
route families and screen-reader speech output were not newly exercised. This
review verifies the affected form and its states, not backend enforcement or
successful provisioning. The standing checklist below is retained as a reference;
the table above is the result of this scoped pass.

## Standing checklist

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
