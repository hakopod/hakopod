# Git provider setup UI review — 2026-09-14

Independent reviewer: `/root/probe_setup_review`. Review only; no product-source edits.

## Result

Approved for the affected Git connection setup UI. All six reported findings were fixed by the implementation agent and retested. This is UI approval against isolated mocked API responses, not verification of real GitHub App registration, GitLab OAuth authorization, production webhook delivery, or provider-account permissions.

## Coverage and evidence

- Read root and Hatch contributor instructions and the standing Hatch checklist, plus changed setup/editor/callback/shared webhook components and metadata-query code.
- `results.json`: 40 rendered cases, 10 states × dark/Paper × 1440/390 pixels. New GitHub, public-URL unavailable, pending/resume, managed App edit, new/existing GitLab, missing callback URL, App-created callback, installation-complete callback, and cancelled callback. Expected loaded controls and fonts awaited, one h1, no browser page errors or document overflow. Reviewed all contact sheets and full-size representative screenshots.
- `interactions.json`: both themes on a 390px touch context. GitHub request failure preserves draft, invalid provider destination is rejected, saved connection resumes rather than recreating, source-only choice is sent, keyboard Enter submits an intercepted manifest. GitLab uses the configured callback URL, preserves secrets after a mocked save failure, hides secrets in review, and shows the one-time secret after successful mocked save. Saved webhook clipboard now uses configured public_url.
- `managed.json`: both mobile themes; managed App edit sends expected revision and preserves stored credential fields without requesting private keys or leaking secrets.
- `final.json`: eight final link/keyboard cases. Setup links visibly underlined and keyboard focusable; shared SelectField switches providers by keyboard. No overflow.
- `webhook.json`: eight final configured/unavailable webhook cases at both sizes and themes. Configured public HTTPS URL is displayed and copied; missing public_url shows actionable setup guidance and no empty or loopback copy control. Final targeted screenshots inspected.

Full evidence root: `work/ui-migration/git-provider-review/` (ignored local fixture). Representative screenshots:

- `screenshots/github-pending-dark-390.png`
- `screenshots/github-edit-review-light-390.png`
- `screenshots/final-github-links-dark-390.png`
- `screenshots/final-gitlab-links-light-390.png`
- `screenshots/final-webhook-configured-light-1440.png`
- `screenshots/final-webhook-unavailable-dark-390.png`
- `screenshots/gitlab-saved-light-390.png`

## Findings resolved

1. New provider selector appeared before h1 and lacked clear visual grouping: moved under the title to compact labeled Provider section.
2. New GitLab flow repeated callback URL: shown once in registration instructions.
3. Inline Setup links were indistinguishable from prose because the unlayered CSS reset overrode utility underline: visible underline and focus verified after fix.
4. Checkbox labels stacked the checkbox above its text: shared checkbox-label exclusion restores inline alignment in new and existing GitLab forms.
5. Pending GitHub setup displayed two action footers and irrelevant help: consolidated into one footer.
6. Shared webhook copy URLs used browser origin despite configured public dashboard URL: now use metadata public_url; unavailable configuration provides a visible setup action.

## Limits

Real external registration/navigation was intercepted, all records/secrets were explicitly artificial, and no live operator cluster, provider account, VM, API data, or release was modified. Go security/business logic and real OAuth exchanges are outside this UI review. Unrelated route families were not re-reviewed. Fixture process and owned headless browsers were stopped after review.

## Standing checklist

The checklist below is copied as required. Blank boxes are the reusable gate; this review's scoped results are recorded above and do not claim that unrelated application, network, deployment, or authentication behavior was retested.


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

