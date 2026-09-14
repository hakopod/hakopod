# Lifecycle dashboard review — 2026-09-14

Independent UI/UX review of deployment jobs, configuration files, connection bindings, named HTTP endpoints, preflight warnings, and public build values. Read the repository and Hatch contributor instructions, public package guidance, tokens, Button, Card, and Tooltip sources before reviewing.

## Result

The affected UI is approved within the fixture coverage below. All findings made during this pass were resolved before final screenshot inspection. This is a dashboard presentation and serialization review, not proof of Kubernetes behavior or an end-to-end deployment.

- Jobs display Completed, Running, or Failed. Completed jobs contribute success to application health; stale observations still withhold current success. Job failures retain pod/log inspection without a replica-readiness claim. Recorded deployment results distinguish completed jobs from running replicas.
- Job configuration keeps unsupported listener/replica inputs disabled and directs timeout/retry editing to TOML. The job Restart action is absent. Existing compact forms remain in place.
- Service Networking displays the observed named HTTP endpoints, keeps long destinations accessible while truncating their text, and excludes non-HTTP URLs. Exposure labels recognize named HTTP listeners.
- Generic TOML export and shallow service updates already preserve `job`, `files`, `bindings`, and `http`. Regression tests cover empty literal files, file modes, secret references, binding passwords, and named domains. Environment and certificate editors clone the complete current spec before editing their own fields. Removal intentionally leaves dependencies/binding references for backend validation.
- The existing build form now reads and submits `build_args` using a small public-values textarea. Empty values, embedded equals signs, and value whitespace survive; duplicate/malformed lines produce a visible error rather than silently losing values. Help states that values are public, baked into the image, and require review/reinstallation of the workflow. Backend validation owns bounds and secret rejection.
- Existing deployment, import, and template review pages render backend warnings. The deployment review was exercised with an explicit fixture preflight warning.

## Rendered coverage

Owned isolated Vite server on `127.0.0.1:4198`, production route/components, injected local fixture API, and a fresh headless Playwright browser profile. The user's browser/profile was not used. Every page has the visible banner “LOCAL QA FIXTURE — artificial data; no live API; external requests blocked.” No production state, credentials, Kubernetes resources, or provider workflow was changed.

| Route family | Cases | Themes and widths |
| --- | --- | --- |
| Application lists/detail | URL-scoped project application list; application services and configuration | Dark and Paper, 390 and 1484 px |
| Services | Job completed, running, failed, stale; named endpoint Networking; environment editor | Dark and Paper, 390 and 1484 px |
| Deployment configuration | Job form review; TOML editor; failed deployment keeps entered image, lifecycle fields, and review warning | Dark and Paper, 390 and 1484 px |
| Deployment history | Recorded service results with completed job | Dark and Paper, 390 and 1484 px |
| Builds | New form, edit form, failed save retaining public values, build detail | Dark and Paper, 390 and 1484 px |

There are 60 distinct route/state/theme/viewport cases: 52 in `results.json`, plus eight supplemental list/build cases. The supplemental run also repeats the four failed-job cases after the final notice-copy change, giving 64 successful captures in total. All final cases have one main h1, no document overflow, no unexpected loading placeholders, error boundaries, missing fixture endpoints, or page errors. The runner waits for expected route content, lazy/query completion, and fonts before measuring or capturing.

Keyboard checks cover endpoint focus, visible focus indication, TOML focus, named help activation with Enter and dismissal with Escape. Mobile touch checks open and close named help and verify the tooltip stays within the viewport. Form checks exercise actual control changes, payload preservation, preflight review, failed submission, return to configuration, and retained build values after save failure. Backend writes are intercepted fixtures.

Real element bounds: at 390 px the main content has 16 px insets, service headings span x=16..374, and the long named endpoint occupies x=151..349 with a 19.5 px height. At 1484 px content uses 24 px insets, service headings span x=24..1460, and the same endpoint occupies x=209..716.875. Service headings retain equal 12 px top/bottom padding. Document widths are exactly 390/1484. Bounds were measured after interactions, so vertical coordinates can be negative when the page is scrolled.

Inspected all four contact sheets covering the 60 distinct cases, plus full-page mobile/desktop screenshots of job completion/failure, named endpoints, recorded deployment results, job review, and the build-values form. The final screenshots show aligned headers/content, readable state labels, preserved action targets, theme-aware active navigation, and confined long strings. The review caught the named-endpoint exposure label and job-failure replica wording; both were corrected and recaptured. Early captures with outdated selectors, absent fixture Git connections, or incomplete plan resource profiles were discarded. An interrupted preliminary supplemental process wrote a late error into its old shared log; the authoritative final `supplement-results.json` contains twelve passing cases and no page errors.

## Verification and limits

- `pnpm test`: 29 server tests and 61 UI tests passed.
- `pnpm typecheck`, `pnpm build`, and `git diff --check` passed.
- Evidence: `work/ui-migration/lifecycle-review/` contains the isolated fixture/server, review scripts, result JSON, test/build logs, four contact sheets, and full-page screenshots. These local evidence files are ignored by Git.
- Job runtime, secret-file injection, connection credential construction, HTTP routing/TLS, preflight accuracy, and real Git provider workflows require backend/cluster acceptance tests; this review does not claim them.
- Advanced lifecycle fields remain editable via application TOML. Dedicated file/binding/job forms and named-endpoint certificate inspection are outside this initial UI. Existing domain/certificate forms still manage their established primary-listener model.
- Rendered scope is the affected route families above. Unrelated Settings, authentication, infrastructure, catalog templates, terminal streaming, and role-denied states were source-reviewed only where shared code applied, not comprehensively rerun. Source review confirmed import/template warnings; those routes were not newly captured.
- 320 px and intermediate breakpoints, a physical touch device, screen readers, and live API failures were not tested in this pass.

## Standing checklist

The reusable checklist below is copied from the project gate. Its blank boxes do not claim unrelated global behaviors were rerun; the completed scope and limits above are the result log.


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

