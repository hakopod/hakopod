# Organization runner pools UI review

Independent review completed on 2026-09-25 using the [dashboard checklist](ui-ux-checklist.md) and contributor rules in `AGENTS.md` and `packages/ui/AGENTS.md`.

## Scope and evidence

The review covers Managed Actions creation/configuration, the review step, shared deployment-secret instructions and runner status. Source routing was checked in `templates.$templateId.tsx`; its existing keyed form preserves project, environment, application and service boundaries. Both Cloud and self-hosted editions use the reviewed components. This was an explicit artificial-data fixture using the real components and shared styles, not a production account or a complete dashboard navigation test.

All compilation and browser execution took place on the VM. No local build, package installation, test runtime or server was started. The reviewer used the existing remote Playwright runtime with requests intercepted; non-loopback requests were rejected. No real credential, GitHub runner registration, deployment or customer workload was changed. The fixture server and browser processes were stopped after review.

Remote evidence lives in `/srv/hakopod-backup-scratch/organization-runners/ui-review/`:

- `main.tsx`, `review.mjs`, `extra.mjs`, `build.sh` and `run.sh` describe the explicit fixture and probes.
- `evidence/results.json`: 72 captured checks. Main matrix: Cloud/self-hosted, dark/Paper, 1440/390/320 pixels, new organization form, organization review, repository review, existing repository configuration and organization status. Additional checks cover unavailable capability, Pro/Team gates, sandbox unavailable, permission gates, plan/deployment errors, missing secrets and keyboard scope interaction.
- `evidence/extra-results.json`: 10 follow-up captures after the permission message fix: eight permission states across editions/themes at desktop/mobile, repository credential help and shared organization/repository credential help.
- `evidence/scope-keyboard-viewport.png`: the open scope menu, followed by verified keyboard selection and viewport-bound checks.
- `evidence/contact-01.png` through `contact-09.png`: all initial captures inspected. Full-resolution inspection additionally covered the mobile organization form, desktop dark review, narrow organization status, organization/shared secret instructions, corrected permission state and keyboard menu.

The initial denied screenshots are superseded by the eight `final-*-denied.png` captures. A first probe failed because a broad accessible-label locator also matched the group field's description; it was corrected to select the organization textbox by role. That fixture locator failure was not a product error.

## Findings resolved

1. Organization review initially inherited repository-only token instructions from the shared secret component. The instructions now require organization Self-hosted runners permission for organization pools and repository Administration permission for repository pools. If both reference the same secret, both permissions appear. Random-password generation remains absent for these provider credentials.
2. Users lacking deployment write access initially saw a disabled Review button without a reason. A visible instruction now states that deployment write access is required to create or update pools. This was re-rendered in both editions/themes at desktop and mobile sizes.

## Completed checks

- [x] Organization is the default for new pools. Repository remains an explicit alternative and is not requested for organization pools.
- [x] Existing repository configurations retain their repository scope and values. Existing organization configurations retain organization and runner-group ID.
- [x] Switching scope and returning from review preserves entered values. Organization requests omit repository; repository requests omit organization/group. Blank group ID is omitted to use GitHub's default group.
- [x] Review displays the chosen target and group. Failed plan requests preserve form values; failed deployments preserve the review. Missing credentials prevent deployment.
- [x] Scope uses the shared SelectField. Mouse, touch, keyboard opening/selection and Escape focus return were exercised. The open menu stays inside the narrow viewport; focus is visible.
- [x] Rendered states have one h1, full-width page-heading dividers, the existing shared 24px desktop/16px mobile content alignment, readable labels and wrapping long identifiers. No document overflow or controls outside the viewport appeared in 82 captured checks. Self-hosted captures have no displayed corner brackets; Cloud retains its existing styling.
- [x] Dark and Paper screenshots were inspected critically. Form sections, summaries, error messages and status facts remain readable; mobile controls and actions wrap without overlapping. No unresolved visual findings remain in the affected components.
- [x] Pro/Team, unavailable runtime, denied access and failed capability states block submission. Runtime and permission explanations remain visible. The permission fix was inspected again after rebuilding the fixture.
- [x] Shared component usage, existing bounded queries and secret handling were inspected. No new runtime dependency, polling loop or cosmetic service was introduced.

## Limits

This review approves the affected UI components in explicit fixtures. It does not prove GitHub organization registration, runner-group policy enforcement by GitHub, live token permissions, runner job execution or Kubernetes cleanup. Those require separate backend/integration evidence. Unchanged global navigation, unrelated routes, loading animations and full authentication flows were not re-certified. The standing checklist below is preserved as the general gate, not a claim that unrelated screens were re-tested.

## Standing checklist


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

