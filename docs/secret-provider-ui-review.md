# Secret provider independent UI review

The independent reviewer approved the provider dashboard after rendered inspection and interaction checks on 14 September 2026. All reported visual and keyboard findings were fixed and retested. This approval covers the UI described below, not a production secret-provider connection or Kubernetes resolution.

## Coverage

The review used the real local Go API and PostgreSQL fixture at ports 8215/4315, with no Kubernetes client or workers. Providers pointed at nonresolving example.invalid addresses. Browser checks used installed Chrome, completed font loading, screenshots and element/text bounds.

- Embedded Settings list, standalone list, Vault/OpenBao and Infisical configuration, review, and edit pages: 1440 and 390 pixels in Paper and dark themes; additional 320-pixel dark coverage.
- Service Secrets and Environment tabs: 1440 and 390 pixels in both themes plus 320 dark. Reference editor: 390 both themes and 320 dark.
- Picker, long names, confirmation, missing scopes, stale revisions, missing environments, failed saves, and keyboard step transitions. Touch-sized controls and mobile picker/dialog interactions were checked.
- Headings and content use the shared 24px desktop / 16px mobile inset. Active Settings and service navigation stay visible in their own horizontal scrollers; offscreen inactive tabs did not increase document width.

## Resolved findings

1. Provider picker labels were centered/right aligned inconsistently beside their icons. A flex wrapper now aligns both labels to the same left edge. Rendered and keyboard selection retests passed at 1440/390 in both themes and 320 dark.
2. A valid 40-character unbroken provider name painted over list actions and expanded the edit document to 473px on a 390px viewport. List and heading wrapping now keep names and actions usable at 390 and 320.
3. The same name overflowed the delete dialog title, then its confirmation label at 320px. Both now wrap. Text-range measurements and screenshots confirm all title and confirmation text fits; the saved-status name also wraps at 320.
4. Returning from Review to Configure using the keyboard left focus on BODY and the mobile page scrolled midway through the form. Both step changes now focus the progress heading and bring it into view. Retests reported the named Configure heading at both 1440 and 390. Initial form load is unaffected.

## Workflow evidence

A disposable Vault provider was created, updated and deleted through the real API. Missing scopes were rejected. Review concealed entered credentials. A simulated 503 save preserved the token and selected scope. Stored credentials were absent from edit fields until replacement was explicitly selected, and absent from public GET responses.

An independent concurrent API update caused a real 409. Save stayed disabled until Compare latest access and explicit adoption of the new revision while retaining the draft. A subsequent scope change and credential rotation saved revision 4. Typed-name deletion stayed disabled until the complete name matched, and deletion succeeded. Escape from a keyboard-opened delete dialog restored focus to its opener.

A simulated project response omitted an existing environment. Its grant remained checked and visibly marked No longer available. The UI explains that no individually selected environments allows all environments, including future ones.

The service UI showed the external provider/path/key reference without a native Set value action. Native metadata correctly reported Kubernetes storage unavailable and disabled replacement. No native-storage roundtrip or external provider resolution is claimed.

## Limits and evidence

Only Vault/OpenBao and Infisical are implemented in this slice. Other providers from the reference image are not represented as supported. HTTP failure injection and removed-environment responses were browser simulations; create/update/conflict/delete used the disposable real API. No production account, cluster, provider or workload was changed.

Evidence is in ignored `work/provider-independent-review/`: screenshot matrix, `results.json`, `workflow.json`, `back-focus.json`, `final-checks.json`, `remaining-final.json`, and `dialog-final.json`. Early screenshots documenting defects are retained beside corrected final images. Large-matrix readiness interruptions were rerun as isolated missing cases; unfinished runs are not counted as successful coverage. The picker harness waits for Radix's deferred keyboard focus before confirming the selection. The 503 from native secret metadata is an expected fixture limitation.

## Standing checklist

The reusable checklist below is copied from the dashboard gate. Its blank boxes are not the result log; the scoped completed review above records this pass. Unrelated route families and Kubernetes operations were not retested by this review.

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

