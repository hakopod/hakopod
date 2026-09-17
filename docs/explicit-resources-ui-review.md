# Explicit resource controls: independent UI review

Reviewed September 17, 2026, against the shared dashboard and Cloud overlay. The reviewer worked independently of the engine implementation. The affected UI is approved after the corrections below.

## Scope and results

Source coverage included ResourceFields, shared Input/Button/error mapping, DeploymentForm, effective resource calculation and hosted Free advisory restrictions. Rendered coverage used actual shared new-application, add-service and edit-configuration routes with synthetic API responses in isolated, headless Playwright. No normal Chrome windows, production accounts or clusters were accessed. All API mutations were intercepted; the fixture server refuses unmatched API calls.

| Coverage | Cases | Result |
| --- | ---: | --- |
| OSS/Cloud; light/dark; 1440px/390px; new/add/edit | 24 | Passed |
| Hosted Free import/rejection/reset, multiple service isolation, TOML/form transition; light/dark; 1440px/320px | 12 | Passed |
| Final plural replica summary; light/dark; 1440px/390px | 4 | Passed |

The primary cases verified imported values, keyboard disclosure activation, field editing, blank inherited memory requests, inline API errors, preservation after failure, effective plan values, return to configuration and reset to defaults. Edge cases checked touch taps, visible keyboard focus, the 44px disclosure target, hosted Free disabled inputs and resetting imported overrides to restore the supported review path. Separate service values remained isolated. Generated TOML retained every override; return-to-form used a synthetic parsed spec, not the Go parser.

Each case waited for expected controls and fonts. The primary matrix measured real control bounds, document width and heading count; edge cases measured control bounds and document width. No unexpected JavaScript errors, rendered error boundaries or unmocked requests remained. Resource fields use two desktop columns and one mobile column. Request/limit text, review summaries, disabled states, focus and inline validation were readable in both themes. Full screenshots, field/error crops and contact sheets were inspected critically, beyond overflow assertions.

## Corrections

- Imported overrides on hosted Free were disabled with no form path to remove them. Added **Use size defaults**, including in that restricted state.
- Normalized absent optional errors before calling the string-based field error helper.
- Associated shared instructions with each field using unique `aria-describedby` IDs.
- Added a plain theme-aware keyboard outline and minimum 44px disclosure target.
- The implementation owner corrected “2 replica” to “2 replicas”; four final rendered cases verified it.

## Evidence and limits

Ignored local evidence is under `web/work/explicit-resources-review/`: fixture/server preparation, `review.mjs`, `edge.mjs`, `replicas.mjs`, result JSON, screenshots and contact sheets. Early crops included the existing sticky footer or transient toast because of capture position; final matrix captures align fields below the header and dismiss the toast while retaining its inline error. Initial edge failures were fixture selector ambiguity and pointer-mode focus checks; final checks use an unambiguous selector and actual keyboard traversal.

This review establishes UI behavior and submitted payloads against fixtures. Kubernetes scheduling, resource enforcement, Cloud quota authorization, actual TOML parsing and deployments are covered separately by backend/development-cluster acceptance. Job/CronJob summary branches were source-inspected but not separately rendered. Unrelated global audit findings, denied/loading routes, shell navigation and authorization were not newly verified. No production writes occurred.

## Standing checklist

The reusable checklist below is copied from the shared gate. Blank boxes are not a new global pass; the scoped results above record this review.


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

