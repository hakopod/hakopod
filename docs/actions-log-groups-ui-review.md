# Managed Actions log groups UI review

Independent review on 2026-09-29 used the [dashboard checklist](ui-ux-checklist.md).
The review uses explicitly marked artificial local records; it does not establish
production runner, Docker, GitHub, or Kubernetes behavior.

## Coverage

- Managed Actions full job output and selected step output at 1440, 390 and 320
  pixels in dark and Paper themes; wrapped text, optional timestamps and long lines.
- Service overview and application overview at 1440 and 390 pixels in both themes.
  The fixture deliberately retains an old saved service image and supplies a new
  observed runner image. The runtime field displays the observed image.
- Group expansion with Enter and Space; touch expansion and collapse on mobile;
  collapsed routine output, expanded error output, and searching text inside a
  collapsed group. The controls expose their expanded state.
- Real document and group bounds, completed font loading, loaded expected content,
  and browser errors. All fourteen route/viewport/theme cases have no page errors
  or document overflow. The six log cases have a visible solid 2px focus outline.
- Source inspection covers the shared log renderer used for full jobs and steps,
  the grouping parser, and the service and topology observed-image consumers.
  Ordinary application Logs already uses bordered rows and is not changed here.

## Visual review and findings

Full-resolution representative screenshots were inspected for desktop and mobile,
both themes, full output, selected steps and the observed image field. Bordered
rows make log boundaries clear. Routine groups collapse while error output stays
visible. Hiding timestamps by default and omitting timestamps in group headings
preserves useful mobile text width; timestamps remain available through Time.

The review led to singular line counts, compact mobile number columns and explicit
focus styling. An initial fixture omitted the self-hosted edition attribute;
that fixture incorrectly showed Cloud focus brackets. Final screenshots use the
same self-hosted attribute as the application root and show simple outlines.

The service overview screenshot exposed clipped pool actions on mobile. The
header now wraps, and the final screenshot and element bounds confirm Configure
pool is fully visible (390px viewport: x=41px, width=134px). Log wrapping now
prefers word boundaries while retaining support for long unbroken output.

The independent reviewer signs off the affected UI within the coverage below.
The final fourteen-case matrix was rerun after these corrections and all checks
passed; representative corrected screenshots were inspected again.

## Evidence and limits

Ignored evidence is in `work/ui-migration/managed-actions-review/`: `main.tsx`,
`server.mjs`, `review.mjs`, `overview.mjs`, `results.json`,
`overview-results.json`, and `screenshots/`. The fixture blocks API requests it
does not explicitly handle. Logs include labelled artificial failures and long
output; no live account or workload was used.

This pass does not reverify unrelated route families, permissions, live polling,
large-window pagination, download fidelity, or real API image observation. Backend
regression tests and cluster acceptance are separate evidence. The observed-image
fixture tests display behavior, not correctness of the API observation itself.
