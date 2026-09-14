# Shared template catalog UI review — 2026-09-14

Independent reviewer: `/root/probe_setup_review`. Reviewed the dashboard and website catalog against their contributor instructions and the Hatch UI checklist. Approved for the covered UI after the findings below were resolved.

## Coverage

The shared catalog contained 71 entries: 35 deployment presets and 36 migration guides. Review used actual current components and catalog metadata with an explicitly marked, isolated dashboard API fixture. No live API, provider account or cluster was changed.

- Dashboard: 32 rendered cases (catalog, PostgreSQL, Open WebUI, vLLM, n8n, Cloudflare DDNS, PocketBase and Openclaw × dark/Paper × 1440/390). Four n8n detail-sheet cases and four final Openclaw guide-sheet cases cover keyboard/touch opening, bounds, Escape and focus return. Loaded content and fonts were awaited; pages had one h1 and no document overflow or browser page errors.
- New configuration fields: defaults, required empty/whitespace guards, exact accessible labels and described-by help, values in plan requests, keyboard activation and preserved input after a simulated validation failure. Existing PostgreSQL review/back flow still retains its fields and sends an empty values map; existing Open WebUI and vLLM forms render correctly.
- Website: 20 rendered cases (catalog, PostgreSQL, n8n, Cloudflare DDNS and Openclaw × both themes × 1440/390). Category and search filters, empty results, counts, keyboard card navigation and mobile touch filters/card navigation passed. Six default-Paper no-JavaScript cases cover catalog, n8n and Openclaw at both widths: all entries and content remain readable while unavailable filtering controls remain hidden.
- Final logo galleries inspect all 70 image-backed entries in each consumer and theme. Every image decoded with positive natural dimensions. Catalog placeholders remain for the one entry without a supplied logo. Image galleries are an isolated review arrangement of actual rendered icon components, not proposed page layouts.
- All nine contact sheets and full-size representative screenshots were inspected, including final guide wording, mobile configuration fields and themed logo plates. Main visual matrices use reduced motion. No new animation implementation was introduced; this pass does not repeat the site's broader animation audit.

## Findings resolved

1. Required config values could bypass native required validation because Review is a button outside a form. Empty/whitespace required fields now disable review.
2. Config field descriptions were part of the label's accessible name. Labels now target the input and help is associated through aria-describedby.
3. Website secrets guidance incorrectly required setup before review. It now describes configuration during deployment review.
4. New website presets repeated identical requirements under a second heading. Duplicate notes were removed and empty sections omitted.
5. Migration guides presented alternative credentials as universally required. Both website and dashboard detail sheets now label candidate credential references and explain alternatives.
6. Some white logos disappeared on Paper, and dashboard's legacy filter inverted catalog brand colors in dark mode. Both consumers now use shared background metadata, retain the original colors, and provide contrasting plates; Valkey also uses the shared mark.

## Evidence and limits

Ignored local evidence is under `work/ui-migration/catalog-shared-review/`: `dashboard.json`, `compat.json`, `guide.json`, `website.json`, `final-read.json`, `logos.json`, `dashboard-logos.json`, their scripts and `screenshots/`.

Representative evidence: `dashboard-pocketbase-light-390.png`, `dashboard-final-guide-light-390.png`, `dashboard-final-guide-dark-1440.png`, `site-final-guide-reading-light-390.png`, `site-final-ddns-reading-dark-390.png`, `site-nojs-openclaw-390.png`, and `*-logo-gallery-{dark,light}.png`.

The website was served from its local Astro source; its development toolbar was excluded from main-heading checks and hidden for final reading screenshots. The dashboard used artificial API responses, including a previously generated PostgreSQL plan fixture. This review does not prove new templates start, become ready, persist data or support a real production cutover. Runtime acceptance and build/test results belong to the implementation report. Safari, Firefox and physical devices were not tested. Owned fixture servers and headless browsers were stopped after review.

## Standing checklist

Copied reusable gate below. The coverage above is the result log; unrelated dashboard behavior is not represented as newly retested.


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

