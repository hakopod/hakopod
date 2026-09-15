# Compose import UI review

Independent review completed on 2026-09-15. The Compose importer passes after the two findings below were fixed. Reviewed source is based on `faa0015` plus the deployment-method layout and generated-editor focus corrections.

## Coverage and results

The real ComposeImport and DeploymentForm components were rendered with the dashboard/Hatch styles in an explicitly labelled isolated fixture. New application, existing application and service-specific configuration states were covered. Conversion and deployment-plan responses were controlled fixtures; no production API, Git provider, database or cluster was contacted.

- 80 functional and bounds assertions passed across dark/Paper themes at 1440 × 900 and 390 × 900, including keyboard and touch operation.
- 24 additional checks passed for method layouts at 320, 640, 1024 and 1280 pixels, upload boundaries, read failures and acknowledgment invalidation.
- The actual new/configure route definitions were also mounted for three deep-link checks: `mode=compose` opens the importer for new and existing applications; adding `service=existing` switches to service editing and hides Compose.
- Final screenshots were inspected for method rows, pasted YAML, conversion errors, generated configuration, warning/acknowledgment controls, ordinary deployment review and stale application errors. No unexpected page exceptions or horizontal document/control overflow occurred.

## Verified interactions

- All five creation methods remain readable. Full creation uses five equal columns at the desktop breakpoint; smaller viewports reflow without squeezing labels. Existing application and service-specific forms retain their smaller set of methods. Active mode color and visible focus follow the existing shared controls.
- YAML can be pasted or loaded from a file. A file of exactly 262,144 bytes is accepted; one byte over the limit is rejected with a visible message while preserving the previous YAML. A simulated file-read failure preserves the paste and explains the fallback.
- Interpolation values are sent explicitly. Duplicate keys are rejected locally without a request or input loss. Unsupported-option and existing-service-collision responses remain visible while preserving YAML and variable input.
- Generated TOML is read-only until transferred to the editor. Conversion warnings are visible, and “Edit generated TOML” remains disabled until acknowledgment. Editing the source discards the generated result; reconversion resets acknowledgment. The ordinary deployment-review action stays disabled while the Compose importer is active.
- Download emits `config.toml` with exactly the generated contents. It does not deploy anything.
- “Edit generated TOML” now focuses the editable TOML textarea. Conversion warnings remain visible afterward. Edited TOML converted to the image form retains commands, quoted arguments and environment-variable drafts; the normal reviewed-deployment screen remains the next step.
- Existing application name is fixed in the importer, and conversion requests include its ID and revision. A stale revision blocks review and retains the generated TOML. Service-specific configuration excludes Compose.

## Findings resolved

1. The fifth creation method originally occupied a lone second desktop row because the grid remained at four columns. The grid now uses the displayed method count, and the 1280/1440-pixel rows were rechecked.
2. Transferring generated TOML originally left keyboard focus on the document body when the button unmounted. A post-render focus step now moves it to the editable TOML field in all four theme/viewport combinations.

## Verification limits

The mocked converter responses test UI behavior and request construction, not the correctness of YAML conversion, merging, backend authorization or Kubernetes reconciliation. The stale/collision checks here verify that the interface handles those responses safely. Actual converter/API tests and live architecture-specific CI acceptance are separate implementation checks. No live cluster success is claimed by this report.

Evidence is retained at `/tmp/hakopod-compose-ui-review/`: `results.json`, `edge-results.json`, scripts and final screenshots. The isolated fixture server was stopped after review.

## Standing checklist

The checklist below is copied from the shared UI guide. Blank entries remain the general gate for future reviews; the scoped results above define what was verified in this pass.

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

