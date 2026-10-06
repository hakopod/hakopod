# Template TOML editing UI review

Review date: 6 October 2026. Independent review complete for template creation
and adding a template to an existing application. No unresolved finding remains
in the reviewed scope. This is isolated browser evidence, not deployment or
production acceptance.

## Coverage and outcome

All **18 final-source cases passed**, producing 78 screenshots. The twelve core
cases cover new and existing applications in dark and Paper themes at 1484,
390 and 320 pixels. Six mobile cases cover denied access, stale response and an
application revision change while the review request is held. Each run waited
for real route content, editor imports, query completion and fonts. Product
source fingerprints stayed unchanged throughout execution.

The runner uses the actual template route/form, TOML editor, secret form and
dashboard shell. Go generates Outpost catalog metadata, canonical TOML, edited
specs, complete application diffs and required-secret lists with PlanTemplate,
AddServices, Parse, Diff and LocalSecretNames. Identity, project, saved-secret
metadata and API responses are explicitly artificial. Every request is
intercepted, external requests are blocked, secret bodies are redacted from the
ledger and every deployment submission fails.

| Area | Verified result |
| --- | --- |
| Layout | 24px/16px shared inset, one page heading, balanced heading padding, editor/control bounds, no self-hosted decorative brackets, readable desktop/mobile reflow in both themes |
| Editing | Keyboard activation opens the editor; touch activation works on mobile; actual editor input changes the draft; no deployment action is available while editing |
| Validation | Invalid draft survives review failure with syntax marker and visible error toast; generated and reviewed canonical TOML have zero Monaco markers |
| Review | Successful review accepts the exact edited spec; custom-config warning is shown; required refs match the edited spec; removed bundled password refs disappear |
| Existing application | Application name remains fixed; review carries loaded scope/revision; final diff page exposes the removal of previous-cache rather than silently merging it back |
| Secrets | Newly referenced custom secret uses generic create-only scoped POST; multiline textarea value survives failure; successful save clears the entry and enables deployment; existing-application saved refs cannot be overwritten from this flow |
| Submission | Held review/deploy controls cannot double-submit; deployment body equals the successfully reviewed canonical TOML, configuration and expected revision |
| Recovery | Failed deploy retains the review; reopening editor retains reviewed TOML; discarding changes restores it; stale responses and revision changes cannot replace the review or deploy |
| Access | Viewer sees deployment-access denial without editor controls |

## Resolved finding

The previous static editor schema falsely marked Go-generated canonical TOML
invalid at line 1. Missing fields included service container_daemon and binding
external_database, external_database_revision and ssl_mode. The schema is now
generated from the reachable authoritative OpenAPI Spec schemas. The final
matrix explicitly requires no diagnostics for initial canonical and reviewed
edited configurations; malformed TOML still reports the actual syntax location.
Earlier smoke captures containing false schema markers are diagnostic evidence
only and are not accepted final screenshots.

## Evidence and independent inspection

The maintained harness is `web/review/template-edits/`; its Go fixture export is
reproducible on the VM and the generated JSON is ignored. Final local evidence:

- `.local/template-edit-review/evidence/results.json`: 18 passing cases, exact
  request assertions, geometry, marker evidence and source hashes.
- `.local/template-edit-review/evidence/screenshots/`: 78 final captures.
- `contact-1.png` and `contact-2.png` in that evidence directory: reviewed contact
  sheets covering all editor/validation and stale/denied states.

Full-resolution screenshot inspection included existing-application editors at
1484 and 390 pixels in both themes; mobile invalid drafts in both themes; the
existing dark desktop review, including the paginated removal row; and the
new-application review/secret layout. Controls remain within the viewport,
long image digests wrap inside the editor, mobile footer actions remain usable,
errors remain visible and the review preserves secret references without showing
secret values. The coordinating reviewer additionally inspected the final dark
390px editor and dark desktop existing-application review, reporting no new
findings. The coordinating reviewer also inspected Paper mobile editor and invalid-draft
captures and reported no additional findings.

Representative final files include:

- `edit-review-failure-existing-dark-1484-editor.png`
- `edit-review-failure-existing-dark-1484-review.png`
- `edit-review-failure-existing-dark-390-editor.png`
- `edit-review-failure-new-dark-390-invalid-preserved.png`
- `edit-review-failure-existing-light-1484-editor.png`
- `edit-review-failure-existing-light-390-editor.png`
- `edit-review-failure-new-light-390-invalid-preserved.png`

| Product source | SHA-256 |
| --- | --- |
| `web/src/components/template-form.tsx` | `e0503ab9af24d7d5332390d8b468045f7cfdf0dc6474aab233151c3bb18b76dd` |
| `web/src/components/template-secret-field.tsx` | `99a7463466c6a2e0e9cebde42c37ab9576ec47dbcb26c09ed0b4501afd88db2d` |
| `web/src/routes/templates.$templateId.tsx` | `7a147f37b578d7668e06f0489efb711316009a3e9d1e091205b806d4fb5ccfa4` |
| `web/src/lib/template-review.ts` | `45544b07f487c6b752141a19a649bf6ea4bf327973de9fa51bdc04163e4094bf` |
| `web/src/components/toml-editor.tsx` | `004f00310ba026045a765d7fd3c693206db7878e0751f58442dc54e55b18de40` |
| `web/src/lib/editor-schema.json` | `3f845b96efca503708b70abe6b75bade7a79ac201d10ef9d039fcb0deff6efdb` |
| `web/src/lib/toml-language.ts` | `72c1b03e09285a1a5ecfcbd22feaaca2d6dd5131c8b5089fbf248d7b3093e3c9` |

All fixture generation, browser execution and compilation occurred on the
reserved VM; local work was source/docs and screenshot inspection. Owned
servers stopped on runner exit. Final read-only verification found all principal
review units inactive and no listeners on ports 4198, 4199 or 4200. The review does not verify real authorization,
secret persistence, deployment submission, workload startup or production
availability. Physical-device and screen-reader tests were not performed. The
standing global checklist remains below; unrelated route families are not
represented as newly tested.

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
