# Managed Actions guided setup UI review

Independent review on 2026-09-29 used the [dashboard checklist](ui-ux-checklist.md),
the Hakopod contributor instructions, and the Hatch public component guidance.
This covers the four-step GitHub, Compute, Jobs, and Review flow. The separate
[log groups review](actions-log-groups-ui-review.md) records log rendering and
observed image display.

All rendered data came from an explicitly marked local artificial fixture. The
fixture loads the real route modules, form components, and dashboard shell;
browser requests outside its local origin are blocked. It did not contact or
change a production API, GitHub account, or Kubernetes workload.

## Coverage

| Route or behavior | Rendered coverage | Result |
| --- | --- | --- |
| `/templates/managed-actions`: create through all four steps | Dark and Paper; 1440, 390, 320px | Passed |
| Existing pool through template edit and service configuration | Dark and Paper; 1440, 390, 320px | Passed |
| Multiple-pool configuration chooser | Dark and Paper; 1440 and 390px | Passed |
| Node lookup failure, unavailable saved node, and long workflow labels | Dark and Paper; 1440 and 390px; additional Paper 320px labels | Passed |
| No eligible nodes, loading nodes, unavailable runtime, and license denial | Dark and Paper; 1440 and 390px | Passed |
| Initial permission denial and permission revoked during review | Both themes for initial denial; Dark 390px for revocation | Passed |
| Architecture mismatch and failed credential save | Dark and Paper; 390px | Passed |
| Invalid collapsed custom field and application revision drift | Dark 390px and Paper 390px respectively | Passed |

Five final scripts produced 60 passing case records with no findings: 6 complete
flows, 20 saved-value and error-state cases, 26 availability cases, 6 final layout
cases, and 2 final mismatch/credential cases. These include overlapping flows and
states; they are not 60 distinct routes.

Chrome ran at the stated viewport widths, with touch enabled on mobile and
reduced motion enabled. Expected content, fonts, and state transitions settled
before accepted captures. All measured cases had no document overflow or browser
page errors. Full-resolution representative screenshots were critically inspected
after corrections, including the latest desktop Compute page, mobile GitHub and
320px Compute pages, custom resource fields, Jobs, Review, denial, multiple pools,
long labels, failed token save, and stale revision states.

## Scoped checklist

The standing checklist applies to these changed routes and their shared setup
components. Unrelated route families, project-home behavior, application endpoint
links, and global navigation policies are not represented as newly retested.

### Layout and hierarchy

- [x] Page headings and summaries align with the shared 24px desktop and 16px mobile inset. No nested page inset or width cap was added.
- [x] The page heading and setup-step divider reach both viewport edges. Actual element bounds were measured at 1440, 390, and 320px.
- [x] Layout and spacing use Tailwind utilities and shared theme tokens. Both dark and Paper borders, text, and status facts remain readable.
- [x] The desktop step navigation remains compact. Mobile steps and form controls fit within the viewport without clipping.
- [x] Active steps use the shared red text with transparent backgrounds and no selected border. Future disabled steps do not inherit filled button surfaces.
- [x] Every measured loaded or denied page has one h1. Necessary warnings and field instructions remain visible; optional explanation uses named help or disclosure controls.
- [x] Help works through focus, keyboard activation, Escape, and touch. Mobile help and node options stay within the viewport.
- [x] Multiple-pool cards keep compact summaries and a maximum of four desktop columns, with shared Button links and no outer list panel.
- [x] No self-hosted decorative corner brackets are visible. Keyboard focus remains a simple solid outline.

### Components and interaction

- [x] Setup uses Hatch public Card and Badge exports and the existing shared Button, Input, and SelectField components. No native selects or competing selector implementation were introduced.
- [x] The form is a dedicated nested page with four guided steps and a review before deployment. Back navigation preserves the draft.
- [x] Step and disclosure targets are at least 44px high. Measured focus outlines are solid 2px. Enter and Space operate disclosures, and keyboard selection and Escape work in the node control.
- [x] Clicking the current Review step preserves the displayed plan. Busy controls prevent duplicate transitions and deployment submissions.
- [x] Resource presets coexist with custom reservations and limits. Choosing custom reveals the controls; an invalid field inside a collapsed disclosure opens it and receives focus.
- [x] Existing runner group, credential name, node, architecture, resources, concurrency, lifetime, workspace, and labels survive the edit flow without normalization to preset values.
- [x] Node loading, lookup failure, missing saved nodes, and incompatible architecture preserve the selected node. Visible warnings explain the state; an incompatible pinned node blocks ordinary progression until explicitly changed.
- [x] Retry nodes stays on Compute and does not submit the form. A successful retry restores the eligible choices.
- [x] Empty, loading, denied, failed, and stale states remain usable and truthful. License/runtime denial blocks progression; permission revocation removes deployment controls.
- [x] Long label chips wrap within the page. The exact JSON-array `runs-on` example scrolls within its own component.
- [x] Failed token save preserves the entered artificial secret, keeps it masked, and blocks deployment. Failed deployment preserves the review and permits a retry.

### Data, safety, and cost

- [x] Review displays the planned pool values. Browser assertions verify that deployment sends the exact planned spec and original revision, with the same idempotency key retained across a failed deployment retry.
- [x] A background application refetch does not silently advance the draft revision. A conflicting plan is rejected while the entered draft remains visible.
- [x] The selected node and architecture reach the plan together. The fixture verifies this request contract, not actual Kubernetes eligibility or placement.
- [x] Creation and review do not fabricate successful deployment. The fixture deliberately returns deployment and credential failures to verify truthful recovery.
- [x] Caching guidance requires workflow configuration and does not imply that creating a runner pool automatically enables a cache. Clean per-job workspaces remain explicit.
- [x] Source inspection confirmed permission-gated node queries, cancellation signals, existing query limits, and a bounded label-chip display. This UI change adds no runtime service or cosmetic dependency.
- [x] Product copy uses plain English. Credential, placement, resource, and revision information needed for a decision remains visible.

## Findings resolved during review

- Clicking the current Review step cleared its plan and left an empty view. The
  current-step handler is now a no-op, with a browser regression check.
- Disabled future steps inherited filled button styling; the desktop step bar
  stretched unnecessarily and its divider stopped at the page inset. The final
  compact, transparent navigation and full-width divider passed bounds and
  screenshot checks in both themes.
- Initial raw borders were too strong. Shared hairline tokens now keep cards and
  disclosures readable without dominating the form.
- Review previously said it was ready while a required credential was missing.
  The heading now says "Review runner pool" and the missing-secret state remains
  visible with deployment disabled.
- Custom resources could remain concealed after selecting the custom profile.
  The custom controls now open, including on validation failure.
- Node retry could submit its containing form. It now has an explicit button type
  and the corrected interaction stays on Compute.
- Long workflow labels could overflow. Chip text now wraps, with a 320px rendered
  regression case.
- Disclosure padding sat outside the summary hit target. The summary now owns
  its padding and minimum height; keyboard and element-bound checks passed.
- Permission-denied routes had no page heading. They now retain the FormPage
  heading, including when permission is revoked during Review.
- The multiple-pool chooser used plural "job slots" for one slot. The final
  singular copy was rendered and inspected after correction.

## Evidence and limits

Ignored local evidence is under
`work/ui-migration/managed-actions-setup-review/`: `review.mjs`, `results.json`,
`states.mjs`, `states-results.json`, `availability.mjs`,
`availability-results.json`, `layout-final.mjs`, `layout-final-results.json`,
`final-functional.mjs`, `final-functional-results.json`, and `screenshots/`.
Early `failure-*` and `probe-*` captures are not final acceptance evidence. The
latest `final-*` captures cover the disclosure and final layout corrections.
The review browser contexts were closed and the local fixture server was stopped
after testing.

The independent reviewer signs off the affected UI and the behaviors listed
above, with no remaining blocking UI findings. This browser fixture review does
not verify backend authorization, real GitHub credential permissions, Kubernetes
node eligibility, cross-architecture execution, cache performance, or enterprise
fleet capacity. Those require the separate backend and real-cluster qualification
evidence. No claim of arbitrary production scale follows from this UI sign-off.
