# Hakopod Edge UI review

Independent UI review approved on 2026-09-30 for the Settings overview,
Infrastructure alias and nested Edge editor. Review followed the
[dashboard checklist](ui-ux-checklist.md), including source inspection, rendered
screenshots, keyboard and touch interaction, and actual element bounds.

## Evidence

- [Passing interface job](https://github.com/hakopod/hakopod/actions/runs/36737533582/job/109963057128)
  in run `36737533582`; artifact `edge-ui-review` (`11107428995`).
- Branch source: `1e206cf9542e28e394cc6927e9243c34e3a060ee`.
- Actual PR merge checkout recorded by the browser runner:
  `2b8a38cd4012bb9ba22ef840e4a91cdf380be84d`.
- Report generated at `2026-09-30T15:34:34.206Z`: **36/36 cases passed**, with
  **90 screenshots** and no document overflow.
- Harness: [web/review/edge](../web/review/edge). The artifact contains
  `results.json`, `screenshots/` and `diagnostics/`. The locally inspected copy is
  in the ignored `.local/edge-review-fourth-run/` directory.

The browser ran the real dashboard components in GitHub Actions. Every API
response was an explicitly labelled synthetic fixture, and unexpected API or
external requests were blocked. No live installation was connected.

## Coverage

Every row was checked in dark and Paper themes.

| Route or state | Viewports | Checks |
| --- | --- | --- |
| `/settings?tab=edge` | 1484×1044, 390×844, 320×844 | Saved policy, active navigation, help, keyboard navigation to the editor |
| `/infrastructure?tab=proxy` | Same three sizes | Alias, active tab, shared overview and layout |
| `/settings/edge` | Same three sizes | Editing, trusted proxy selects, rule reordering/add/remove, review, failed save, conflict comparison, pending submission and queued result |
| Editor access denied and Cloud managed | 1484×1044, 390×844 | Page title, concise access guidance, no mutation form or policy request |
| Empty, queued, drift, error and loading overview | 390×844 | Correct state, visible guidance, disabled actions where required |

## Checklist result

- [x] Shared 24px desktop and 16px mobile insets; no duplicate wrapper padding,
  width caps, clipped controls or horizontal document overflow. Page dividers
  span the viewport and heading padding is balanced.
- [x] One page title, subordinate headings, compact summaries and actions;
  explanatory help remains accessible. No decorative heading icons or visible
  self-hosted corner brackets. Active navigation uses the theme's red without a
  selected background or border.
- [x] Public Hatch components, shared action buttons and `SelectField` controls;
  readable labels, field instructions and both-theme contrast. Help opens and
  dismisses with keyboard or emulated touch and stays within the viewport.
- [x] Native Tab traversal reaches all **19 editable controls** and the submit
  action in every theme/viewport combination: **114 full-control bounds and
  interior hit checks**, all visible with a **2px solid focus outline**.
- [x] The consequential change is reviewed on a dedicated nested page. Review
  does not submit; it shows current and proposed policy and controller values.
  Rule order, replacement, trusted-proxy consequences and reload scope are clear.
- [x] Failed saves preserve entered values. A revision conflict requires a fresh
  comparison and review. Pending submissions cannot be duplicated. A synthetic
  202 remains queued and does not change the observed policy or invent success.
- [x] Loaded-content, fonts, browser errors and fixture-contract checks complete
  before normal captures. The explicit loading case waits for the held HAProxy
  request, not an earlier account-loading screen.
- [x] Source review confirms bounded policy inputs and polling, revision checks,
  and no added service or cosmetic dependency. This editor has no secret input.

Rendered review included both route families, narrow layouts, trusted-proxy
fields, current/proposed values, failed saves and conflicts, queued/drift/error
states, access guidance, help, and focused textareas. Full-page captures can
reposition sticky elements; viewport captures and hit checks were used to judge
actual focused-control visibility.

## Findings resolved

The nested editor originally lost its page title during access denial and used a
decorative empty-state box. Its page wrapper now remains present through access,
loading and error states, with concise Edge-specific denial guidance.

Native focus scrolling could leave a country field behind the desktop action
bar or show only part of a mobile textarea. The editor now opts into the shared
`FormPage` focus-visibility helper. It measures viewport, scroll ancestor and
sticky element bounds after focus, then scrolls a fitting control into view with
outline clearance. Oversized controls retain native caret scrolling. Default
forms do not enable this behavior. The final field checks and viewport images
confirm the correction in both themes at all three widths.

## Limits

This is UI approval using Chromium and emulated touch with reduced motion.
Physical mobile keyboards, other browser engines, maximum-size policies and
unrelated application/project/service routes were not exercised. API fixtures
do not verify Go authorization, durable operations, Kubernetes reconciliation,
HAProxy traffic enforcement or production deployment; those require their own
test and runtime evidence.
