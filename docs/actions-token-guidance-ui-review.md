# Managed Actions credential guidance and icons UI review

Reviewed on 2026-09-29 by an independent reviewer against `34a9b923f0b078b88231a3d7355ae9e01b71b06f` and the subsequent name-pattern and catalog corrections. The evidence manifest records the exact final source hashes. Scope: `ManagedActionsTokenHelp`, its use in the managed runner form and shared `DeploymentSecrets`, and Actions service icons in application cards, service cards and topology.

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

## Scope and review method

The standing boxes above remain the reusable gate. This report records only the affected surfaces and behaviors below; it does not claim a new audit of unrelated dashboard routes.

All browser execution ran on the build VM, using the actual product components, route module, shared styles, fonts and public icon assets. The fixture rendered artificial records inside a minimal shared shell. It intercepted every API request, permitted local assets only, and aborted external requests. It did not open GitHub, save real credentials, call production APIs or change a customer workload.

The matrix used self-hosted and Cloud edition flags, dark and Paper appearance, and 1440px, 390px and 320px viewports. Paper set both the actual `light` class and `data-theme="light"` attribute. Viewport geometry was measured after expected content and fonts loaded. This exercises the shared Cloud UI, not private Cloud authentication, workspace loading or billing.

| Surface | Rendered coverage |
| --- | --- |
| Managed runner form | Organization and repository scopes at all three widths, both editions and themes; collapsed and expanded token guidance |
| Shared deployment secret setup | One credential used by both organization and repository pools at all widths; each scope independently, ordinary password, saved and absent-secret states at 320px |
| Application list | Current runner image, legacy custom image and ordinary Python image carrying Actions config; ordinary Python, PostgreSQL and generic service application |
| Application Services tab | The actual application route with all six service variants and a long legacy pool display name |
| Application topology | Same six variants, including 320px keyboard selection that reveals an offscreen node inside the topology canvas |
| Keyboard and touch | Native disclosure, inline GitHub link, failed/successful token save, review navigation and retained configuration |
| Managed Actions catalog | Actual catalog card and detail sheet at 1440px and 320px in both editions/themes; metadata copied from the updated Go template |

## Completed checks

- Permissions remain visible while the longer token-creation instructions are collapsed. Organization scope lists Self-hosted runners write access; repository scope lists Administration write access; both list Actions read access. A credential shared across both scope types lists all required permissions once.
- The instructions name resource ownership, repository selection, private-repository visibility and organization approval. The link points to GitHub's fine-grained token form, opens a new tab and retains `rel="noreferrer"`.
- Ordinary secrets retain random-secret generation. Saved or absent secrets show no token instructions. Failed intercepted saves retain the entered value; successful saves remove setup controls and show the saved status.
- Enter and Space toggle the native disclosure; touch opens it. Tab reaches the GitHub link. Self-hosted focus uses the shared 2px outline; Cloud uses the shared corner focus treatment. A persistent native disclosure is not a dismissible tooltip, so Escape dismissal is not expected.
- Shared page insets measured 24px on desktop and 16px below 640px. One h1 was present per loaded page. All measured controls stayed inside the document; there was no document overflow. Long pool names remained inside their cards and inspector.
- GitHub icons loaded for every Actions-configured service, including legacy/custom images and an image otherwise mapped to Python. Application and service card wrappers and images measured exactly 22×22px; topology wrappers and images measured 18×18px. Ordinary service icons retained their previous mapping. Decorative icon images have empty alt text; service text supplies the accessible identity.
- The Managed Actions catalog alias uses the GitHub asset. Its card and detail sheet use 28px and 36px icons respectively; the detail requirements include repository Actions read permission.
- Self-hosted screenshots show no decorative brackets. Both palettes retain readable token guidance and icon contrast. Topology's internal horizontal canvas remains scrollable; keyboard focus reveals a selected node without increasing document width.

## Finding from actual submit flow

The review found that the existing application and service name patterns used an unescaped hyphen in a character class. Current Chromium parses HTML `pattern` with the `v` flag, rejected both expressions on submit, and skipped their native pattern checks. The form still reached the review page. The product owner escaped the hyphen in both fields. The targeted rerun checks native pattern rejection, verifies that invalid names produce no plan request, accepts hyphens and digits, and exercises the form-to-review/back flow in both editions and themes. The original finding and final results are retained separately.

## Evidence and limitations

The final evidence contains 188 screenshots: 148 matrix/state/interaction captures, 16 post-fix review captures, 8 focused secret-section images, and 16 catalog card/detail captures. All final finding lists are empty. Twenty-four contact sheets cover the inventory; representative full-resolution images were also inspected for permission text, focus, icon contrast and narrow review layout.

The evidence index includes `evidence/results.json`, `evidence/review-flow-results.json`, `evidence/catalog-results.json`, their matching empty finding lists, and `artifacts-manifest.json` with SHA-256 hashes of exact source and artifacts. `server-stop.json` records the verified fixture PID/cwd, stopped process and closed localhost port 14541.

Representative screenshot names:

- `self-dark-1440-secrets-mixed-expanded.png`
- `self-light-390-list-icons.png`
- `self-dark-390-secrets-mixed-link-focus.png`
- `cloud-light-390-secrets-mixed-link-focus.png`
- `cloud-light-320-form-org-review-secret-detail.png`
- `self-dark-320-templates-catalog-detail.png`
- `cloud-light-320-templates-catalog-detail.png`

Final evidence resides on the VM at `/srv/hakopod-backup-scratch/actions-guidance-20260929/ui-review/`. The fixture entrypoint, Vite configuration, browser scripts and screenshots are reviewer-owned artifacts, outside product source.

The initial selector mismatch and incomplete Paper theme attribute were fixture defects. Their captures are excluded from final acceptance and retained separately under `fixture-diagnostics/` and `before-theme-parity/`. No product implementation was changed by this reviewer.

This pass verifies presentation and intercepted interaction behavior. It does not verify GitHub token issuance, organization approval, GitHub permission enforcement, real secret persistence, runner registration or a production release. Those are separate backend and deployment checks.

## Result

Signed off for the affected shared UI after the name-pattern correction and final catalog check. No unresolved finding remains. The reviewer changed only this report and isolated fixture/evidence files; the owned Vite process is stopped. Product release and production verification remain the delivery owner's responsibility.
