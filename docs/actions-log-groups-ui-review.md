# Managed Actions log groups UI review

Independent final review on 2026-09-29 used the [dashboard checklist](ui-ux-checklist.md),
the Hakopod contributor instructions, and the Hatch component guidance. This review
supersedes the earlier narrow sign-off. All rendered records are explicitly marked
local artificial fixtures; no production account, workload, or API was used.

## Coverage

| Route or behavior | Rendered coverage | Result |
| --- | --- | --- |
| Managed Actions full job and selected step output | Dark and Paper; 1440, 390, 320px | Passed |
| Service Overview runtime image | Dark and Paper; 1440, 390, 320px; one, zero, and mixed observed images | Passed |
| Application Topology image inspector | Dark and Paper; 1440, 390, 320px; one, zero, and mixed observed images | Passed |
| Initial loading, GitHub credential denial, and role revocation | Dark and Paper; 1440 and 390px | Passed |
| Empty output and request failure | Dark; 390px | Passed |
| Live polling, source transition, paging, search, and download | Browser interaction checks using artificial records | Passed |

The final rendered matrix contains 22 route/viewport/theme cases, with additional
state variants and functional cases. Expected content and fonts were loaded before
screenshots. All cases had one page heading where measured, no page errors, and no
document overflow. Full-resolution representative screenshots were critically
inspected after the final corrections, including mobile diagnostics, timestamps,
both themes, empty and mixed images, and denied/loading/error states.

## Scoped checklist

The standing checklist applies to the affected routes and shared components in
this change. Unrelated route families, forms, navigation policies, and mutation
flows are not represented as newly verified.

### Layout and hierarchy

- [x] Shared page insets and full-width heading/tab dividers remain aligned; no nested page inset or width cap was introduced.
- [x] Layout and spacing use Tailwind utilities and shared tokens. Dark and Paper text, borders, and active navigation remain readable.
- [x] Desktop controls remain compact. Mobile controls reflow without clipping. Configure pool is completely inside the viewport at 320 and 390px.
- [x] Log rows have distinct borders. Unwrapped output scrolls inside its log viewport; wrapped long output does not enlarge the document.
- [x] Page headings and necessary actions retain the existing hierarchy. No decorative headings, new explanatory paragraphs, or corner brackets were added.
- [x] Managed image fields wrap long digest-pinned references. Mixed images show both observed values. Empty observations say "No runner image observed" and do not expose a stale scalar or saved image.

### Components and interaction

- [x] Group controls expose `aria-expanded`, have a visible solid 2px keyboard focus outline, and respond to Enter and Space.
- [x] Touch expansion and collapse work at 320 and 390px. Group controls meet the measured 40px minimum height and stay within their viewport.
- [x] Full job and selected step logs use the same renderer. Failed jobs and steps expose diagnostics regardless of their wording.
- [x] Routine completed groups start collapsed. Unfinished groups start open; arrival of a closing delimiter preserves the reader's expansion choice.
- [x] New diagnostic lines reopen previously collapsed groups, including a later `npm ERR!` after an earlier warning. Unchanged polling preserves manual collapse.
- [x] Search includes the retained window, reveals matching children with ancestor context, and restores manual expansion after clearing the search.
- [x] Time displays timestamps on group headings and ordinary rows. Its default hidden state preserves mobile text width.
- [x] Loading, empty, no-match, credential-denied, request-failure, and role-denied states remain usable and truthful. Retry controls are absent without `logs:read`.
- [x] After role revocation, previously rendered artificial log output disappears and a wait longer than the live polling interval produces no further log request.

### Data, safety, and cost

- [x] Managed image display consumes only `observed.images`; the old saved revision and historical scalar are not used as a fallback. Both affected image consumers were rendered.
- [x] The actual five-second timer fetched appended artificial runner output without query invalidation. Switching from runner to GitHub source reset expansion state.
- [x] A 2,400-line group retained its complete heading/count and ancestor context across paging and search. No page rendered more than 1,000 row elements.
- [x] An independent 40-level nesting case paged every one of its 6,480 parsed rows across seven pages, without omission or missing ancestor headers. Parsing kept ancestry bounded at 32 levels and each page at 1,000 rows.
- [x] Download preserved the entire loaded raw window, including group delimiters. A 64 KiB line remained usable without document overflow.
- [x] Source inspection confirmed bounded expansion state, existing retained log limits, cancellation signals, and disabled background polling. This does not establish production fleet capacity.

## Findings resolved during independent review

- Expansion previously tracked only a boolean attention flag. After a warning, a
  reader could collapse a live group and miss a later failure. The latest diagnostic
  line now identifies newly arrived attention; the corrected browser regression
  also verifies that an unchanged poll does not reopen the group.
- Log retry actions remained available after `logs:read` was revoked. Both the
  request-error retry and the log-state retry now require that permission. The
  corrected rendered cases verify absent controls, removed log output, and no
  further live log requests after revocation.
- The earlier application screenshots used `tab=overview`, which displayed an
  application shell without the image consumer. They were rejected as image
  evidence. The final matrix visits `tab=topology` and waits for the loaded image
  inspector before assertions and screenshots.

The independent reviewer signs off the affected UI and the functional behaviors
listed above. Both product findings were resolved and their corrected states were
rerun and visually inspected before this sign-off.

## Evidence and limits

Ignored local evidence is in `work/ui-migration/managed-actions-review/`:
`independent.mjs`, `independent-results.json`, `states.mjs`,
`states-results.json`, `functional.mjs`, `functional-results.json`, and
`screenshots/independent-*.png`. The earlier `overview.mjs` application captures
are not accepted evidence. Fixtures block unhandled and external requests.

The final independent matrix reports no findings; all four additional state cases
and the existing functional interaction checks pass. The review browser contexts
were closed and the local fixture server was stopped after testing. This pass does not verify real GitHub credentials,
backend authorization, Kubernetes image observation, cross-architecture builds,
enterprise load, or unrelated route families. Those require separate backend,
cluster, and capacity evidence; this UI sign-off is not a production-scale claim.
