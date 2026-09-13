# Page inset and navigation review

Independent UI/UX review for 2026-09-13. Signed off after source inspection, rendered screenshots, element measurements and keyboard/touch checks. No open findings remain in this layout scope.

This pass covers the shared 24px page inset, reduced to 16px below 640px; full-width page dividers; red navigation selection; application endpoint alignment; and spacing above alarm links. It follows the [Hatch review checklist](ui-ux-checklist.md) and the [contributor rules](../AGENTS.md).

## Coverage and evidence

The isolated browser fixture imports the real dashboard routes, components, styles and Hatch package. Every record is marked artificial. Requests outside the fixture origin are blocked; no production account, application or API was changed.

| Surface | Coverage |
| --- | --- |
| Projects and applications | Project overview, scoped lists, invalid/empty scopes, application/service tabs, configuration, domains, variables, source import and backend certificates |
| Catalog, builds and deployments | Catalog and template form, build list/new/edit/detail, deployment inspector |
| Networks and infrastructure | Network list/new/detail/connect, node terminal, registries, TLS, enrollment and HAProxy tabs |
| Backups | All list tabs, job detail, run/restore, destination and schedule forms |
| Settings and account | Every settings section, integrations, profile, host access, preferences dialog, login/setup/signup/recovery/invitation/device/onboarding |
| Alarms and shared states | Inbox/settings, viewer restrictions, loading/error/not-found and disconnected management API |

The inventory contains all 47 visual route modules plus 37 meaningful tab, role and shared-state variants. HTTP-only routes have no visual layout.

- The shared-layout candidate passed 336 renders: 84 cases at 1484px and 390px, in dark and Paper themes.
- It passed 198 boundary renders: 19 cases at 320, 639, 640, 1280 and 2560px in both themes, plus Settings at 1023 and 1024px. These include long names/endpoints and an invitation rendered inside the dashboard.
- The last two CSS changes wrap narrow form-mode controls and constrain the shared field-stack grid and its children. A separate 24-case targeted pass covers application creation/configuration, the domains form and its empty state, and network creation at 320, 390 and 1484px in both themes. Input, select, form-child and note bounds are checked for internal clipping. Earlier full-matrix captures are not represented as recaptures of these last selector changes.
- All 32 keyboard/touch checks passed, followed by 20 targeted mode/error checks on the last CSS changes and 10 help checks. They cover menu and select bounds, focus restoration, form-value retention during resizing, preference sections, active application/service tabs after deep links and resizing, local scrolling, custom blue accent, and form-mode selection. Help covers keyboard, hover, Escape, Enter/Space and touch dismissal.

Each capture waits for expected route content, lazy imports, query completion and fonts. Unexpected loading stacks, error boundaries, missing fixture responses and document overflow fail the run. Contact sheets cover the route inventory; full-size review includes applications, domains, service networking, Settings, authentication, forms, navigation and interaction states.

## Findings resolved

1. A long application name plus endpoint pushed the heading to 352px on a 320px screen. Constraining the heading content now keeps it inside x16–304; the 12px external-link icon stays beside truncated text. The full destination remains in the accessible label and title.
2. Form-mode controls retained their old selected fill. They now use the shared red text/icon state and a keyboard-only focus outline. A later screenshot caught the Git repository option clipped inside the narrow form; the shared control now wraps its buttons.
3. Mobile Settings and the direct-main Backups tab row missed the full-width divider rule. Their borders now reach both viewport edges while content keeps the shared inset. Desktop Settings and the preferences dialog keep their internal layout.
4. A service Secrets deep link left its selected tab offscreen at x430–509 on a 320px screen. Shared tab rows now reveal their active item locally after mounting, selection, resizing and font readiness, without scrolling the document.

5. The narrow application form had a 242px field-stack with a 303.64px implicit grid track, hiding the right edges of fields and help text. The shared grid now uses a bounded column and minimum-width resets. Full-height screenshots and child bounds confirm that fields, select arrows, notes, footer and error messages remain inside the form. The error test also retains the TOML draft.

The measured main content bounds are x24–1460 at 1484px, x16–374 at 390px and x24–616 at 640px. Nested page wrappers add no horizontal inset. Embedded authentication adds none. Page-row borders span the viewport; card and inspector borders stay local. Alarm links have 16px top spacing. Active text/icons use Hatch red in both themes even when the dashboard accent is blue; keyboard focus remains visible.

## Checklist applied

Checked items cover rendered layout, interaction and relevant source inspection. Unchecked items are unchanged platform behaviors outside this review; this is not a new end-to-end claim for authentication, authorization or deployment operations. The one-heading check applies to page routes; intentional shared loading/error/not-found examples retain their existing state headings.

### Layout and hierarchy

- [x] The shared page container supplies 24px horizontal padding at 640px and above, 16px below. Nested pages fill its content box without duplicate insets, centered margins or width caps. Headings, summaries and lists align. Settings fill the area beside their navigation; embedded auth does not double its parent's inset.
- [x] Page-heading and page-level tab-row bottom dividers reach both viewport edges. Full-width row backgrounds and borders do not shift their labels outside the content inset or cause document overflow. Internal card/inspector dividers stay within their component.
- [x] Shared layout/spacing uses Tailwind utilities in JSX or `@apply` within semantic CSS classes. Responsive rules use the shared breakpoint; raw CSS remains for component-specific behavior. There are no competing page padding overrides.
- [x] Page and form-section headings have no decorative icon or visible description. One title carries the context; necessary actions remain aligned. Status and data needed for decisions stay visible; explanatory prose moves to help.
- [x] Help is short, named, keyboard reachable and available on touch. Focus/hover or activation reveals it; Escape dismisses it; it stays inside the viewport. It is not the sole home for essential warnings or field instructions.
- [x] Pages have one h1, sensible subordinate heading order and no duplicate headings or breadcrumbs. Nested back navigation uses the global header.
- [ ] Projects is the default home regardless of saved scope. Application lists are URL-scoped to a valid project and environment; invalid or unavailable scopes never fall back silently or display another project’s cached data. **Unchanged; not retested end to end in this layout pass.**
- [x] Page-heading vertical padding is balanced above and below at every responsive layout.
- [x] Active main navigation, tabs and Settings/preferences sections use the shared theme-aware red for text and icons, with no selected underline, border or background fill. Hover/focus preserve the selected color and visible keyboard focus; ordinary row dividers remain visible.
- [x] The active section remains visible inside scrollable navigation after deep links, selection changes, font loading and resizing, without scrolling the document.
- [x] Desktop navigation is one compact row. Mobile reflow and long names do not cause document overflow or clipped actions.
- [x] Summaries are inline and compact. Catalog lists have no outer panel border. Cards keep restrained surfaces, readable spacing and meaningful hover/focus states.
- [x] Application/service grids use at most four normal desktop columns and five wide columns. Sparse grids retain card widths.
- [x] Action links use shared Button with asChild. Sibling actions have equal height and vertical alignment; labels remain readable and targets usable.
- [x] Application endpoint text and external-link icon stay on one line; long labels truncate without hiding the destination from assistive technology. Alarm links have visible separation from the preceding content.

### Components and interaction

- [x] Consume Hatch through public exports. Use shared tokens for color, spacing, state and typography; check dark and Paper themes.
- [x] All selects use SelectField, including disabled/empty options and accessible labels. Do not add native selects or a competing wrapper.
- [x] Keyboard focus is visible. Cards preserve native links and independent menu/copy actions. Disabled/loading controls cannot double-submit.
- [x] Forms with more than four inputs use nested pages. Labels, contextual instructions and validation stay associated with inputs. Textareas can expand where useful.
- [ ] Consequential actions show a review/confirmation, and failed requests preserve entered values. Permission failures are clear. **Unchanged; not retested end to end in this layout pass.**
- [x] Before screenshots, wait for the route’s expected body or control, lazy imports, query completion and fonts. Reject unexpected loading placeholders, rendered error boundaries and fixture schema errors. A page title or clean console alone is not evidence that the page loaded.
- [x] Empty, loading, denied, failure and stale-data states remain usable without artificial success. Long IDs, URLs, values and translated-length text wrap or scroll within their component.

### Data, safety and cost

- [ ] Display actual backend observations; separate desired, observed, pending and stale state. Never fabricate metrics, logs or deployment success. **Unchanged; not retested end to end in this layout pass.**
- [ ] Secrets are write-only, roles are enforced by Go, and mutation controls respect access. Review shared-secret impact before replacement. **Unchanged; not retested end to end in this layout pass.**
- [x] Polling, streaming, caches and buffers are bounded and inactive work stops. Do not add dependencies or runtime services for cosmetic changes.
- [x] Product copy is concise plain English with no emojis. Avoid repeated context and implementation jargon in routine flows.

## Verification limits and cleanup

This is a browser layout and interaction review, not a new Kubernetes, cloud, certificate, backup, email or authentication acceptance test. Existing API data flows and authorization were not changed by these styling fixes. The tab-reveal helper uses a bounded observer per mounted navigation and disconnects it on cleanup. No runtime dependency was added.

Local evidence is in `work/ui-migration/page-inset-review/`: route/case inventories, `results.json`, `edge-results.json`, `targeted-results.json`, `interaction-results.json`, `mode-results.json`, `help-results.json`, scripts and screenshots. `preliminary-*` and `before-review-fixes-*` preserve superseded passes; `failure-*` screenshots preserve the two narrow-layout failures. Only the final results and targeted recaptures support this sign-off.

The isolated fixture on port 4195 and all review browser processes were stopped. The real preview on port 4173 was left running.
