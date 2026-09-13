# Dashboard UI review checklist

These rules apply to every dashboard route, shared layout and reusable component. User requirements override the Hatch examples where they differ. Every UI change needs an independent UI/UX reviewer, source coverage, rendered desktop and mobile checks in both themes, and a recorded result. Do not sign off from source inspection alone.

## Sources

- [Hakopod contributor rules](../AGENTS.md).
- [Hatch contributor rules](../packages/ui/AGENTS.md) and [public package guidance](../packages/ui/README.md).
- [Hatch tokens](../packages/ui/packages/ui/src/styles/tokens.css), [Button](../packages/ui/packages/ui/src/components/button.tsx), [Card](../packages/ui/packages/ui/src/components/card.tsx), and [Tooltip](../packages/ui/packages/ui/src/components/tooltip.tsx).
- User direction: simple and compact; no outer horizontal page gutters or width caps; headings contain the title and necessary actions only. Descriptions belong in accessible help. Decorative heading icons are omitted. Internal input/card padding remains necessary for readability and touch access.

## Required checks

Copy this standing checklist into each UI review. The blank boxes are a reusable gate, not the result log; the completed review below records this pass.

### Layout and hierarchy

- [ ] Root content and every nested page fill their available width with zero outer horizontal padding, margin or width cap. Settings fill the area beside their navigation.
- [ ] Page and form-section headings have no decorative icon or visible description. One title carries the context; necessary actions remain aligned. Status and data needed for decisions stay visible; explanatory prose moves to help.
- [ ] Help is short, named, keyboard reachable and available on touch. Focus/hover or activation reveals it; Escape dismisses it; it stays inside the viewport. It is not the sole home for essential warnings or field instructions.
- [ ] Pages have one h1, sensible subordinate heading order and no duplicate headings or breadcrumbs. Nested back navigation uses the global header.
- [ ] Projects is the default home regardless of saved scope. Application lists are URL-scoped to a valid project and environment; invalid or unavailable scopes never fall back silently or display another project’s cached data.
- [ ] Page-heading vertical padding is balanced above and below at every responsive layout.
- [ ] Active tabs use accent text, icons and underline only. Active, hover and focus states do not add a background fill; keyboard focus stays visible.
- [ ] The active section remains visible inside scrollable navigation after deep links, selection changes, font loading and resizing, without scrolling the document.
- [ ] Desktop navigation is one compact row. Mobile reflow and long names do not cause document overflow or clipped actions.
- [ ] Summaries are inline and compact. Catalog lists have no outer panel border. Cards keep restrained surfaces, readable spacing and meaningful hover/focus states.
- [ ] Application/service grids use at most four normal desktop columns and five wide columns. Sparse grids retain card widths.
- [ ] Action links use shared Button with asChild. Sibling actions have equal height and vertical alignment; labels remain readable and targets usable.

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

## Coverage for 2026-09-13

The current pass covers global page gutters and compact headers. The broader checklist remains the standing review gate; unrelated behaviors are not represented as newly retested.

| Route family | Route source coverage | Layout/header | Rendered evidence |
| --- | --- | --- | --- |
| Applications | index; new; import; application detail; configure; domains; environment; source; service tabs | Shell, PageHeader, FormPage, custom detail | Passed: 01–07, 21, 57–59 |
| Catalog | templates; template detail | Shell, PageHeader, FormPage | Passed: 42–43 |
| Builds | builds; new; detail; edit | Shell, PageHeader, FormPage, custom detail | Passed: 16–19 |
| Deployments | deployment detail | Shell, custom detail | Passed: 20 |
| Networks | networks; new; detail; connect | Shell, PageHeader, FormPage | Passed: 33–36 |
| Infrastructure | infrastructure tabs; registry new/edit; node terminal | Shell, PageHeader, FormPage | Passed: 22–25, 51–53, 65 |
| Backups | backup tabs; job detail; run; destination new/edit; schedule new/edit; artifact restore | Shell, PageHeader, FormPage | Passed: 08–15, 54–56 |
| Settings | all settings tabs; profile; integrations; provider; host access | Settings layout, FormPage | Passed: 37–41, 44–50 |
| Account | signed-out login/setup; signup; forgot; reset; verify; invite; device; onboarding | Auth layout, PageHeader | Passed: 26–32, 60–61 |
| Shared states | root not-found; loading; error; unavailable integration states | Shell and shared states | Passed: 62–64; unavailable issuers/enrollment in 53, 65 |
| Non-visual routes | api.$; session | HTTP only | No page layout |

## Completed review

The independent UI/UX reviewer signed off on the global layout and heading changes after source inspection, screenshots and element measurements. The inventory contains all 43 visual route modules plus 22 tab, account and shared-state variants.

- [x] All 65 cases rendered at 1484 and 390 pixels in dark and Paper themes: 260 cases passed.
- [x] Fourteen representative routes also passed at 320 and 2560 pixels in both themes: 56 cases, including long application/domain names and edited backup schedules after fonts loaded.
- [x] Outer page wrappers have zero horizontal padding/margins and no width cap. Their bounds fill the available width; settings content fills its navigation track. No document overflow was found.
- [x] Page and form-section descriptions use named help controls. Decorative page, section and repeated aside icons are removed; functional controls and operational state remain visible.
- [x] Ten help interaction checks passed across both themes: focus, aria-describedby, Escape, Enter, Space, mouse hover/exit, touch toggle and outside dismissal. Mobile tooltip content stays within the viewport.
- [x] Header actions keep equal heights. Long titles wrap without hiding controls. Full-resolution domains, networks, settings, application, backup and help screenshots were inspected alongside contact sheets covering the route inventory.
- [x] Loaded-content checks require each lazy settings/infrastructure tab’s expected heading, no pending loading stack, no unexpected error panel or rendered error boundary, and completed font loading. Explicit loading/error examples are recorded separately.
- [x] No production account, workload or API was changed by the review. The fixture and browser processes were stopped afterward.

The review found and resolved overflowing Hatch corner brackets, mobile tooltip width and second-tap behavior, a backup form’s font-dependent minimum-content overflow, oversized empty states, a blank conditional footer, leftover application metadata icons and repeated aside icons. A single synthetic pointer jump does not exercise Radix’s hover grace area correctly; realistic stepped pointer movement was used for hover dismissal.

Screenshot inspection also caught lazy-tab placeholders and incomplete fixture records that geometry assertions missed. Those early captures were discarded as evidence. The final run exercised loaded content with complete artificial records.

Local evidence is under `work/ui-migration/global-review/`: `routes.json`, `results.json`, `edge-results.json`, `help-results.json`, the review scripts and full-page screenshots. Files use case IDs from the matrix, theme and viewport width. These ignored fixtures support UI review; they do not claim new end-to-end verification of backup, deployment or authentication operations.

## Project navigation review

The next pass is recorded in [the project navigation checklist](project-navigation-checklist.md). The completed global-layout review above remains historical evidence.
