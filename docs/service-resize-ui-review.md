# Service resize review crash: independent UI review

Reviewed on 2026-09-26 using the [dashboard checklist](ui-ux-checklist.md).
Approved within the fixture coverage below. No customer application, secret,
deployment, or Kubernetes resource was changed.

## Reproduction and scope

The unchanged shared `DeploymentSecrets` component reproduced
`Cannot read properties of undefined (reading 'organization')` with an ordinary
service and an empty missing-secret list. The failure occurred during review
rendering, before a deployment request. Requiring a current missing-secret name
and an actual Actions configuration prevents ordinary services from entering
the runner-credential permission calculation.

The reviewer used a separate source copy on the VM. Source inspection covered
the shared component and its callers in `DeploymentForm`, repository import,
connected-source review, built-image review, and Managed Actions review.
Rendered route-level coverage was the real `DeploymentForm` in form and TOML
modes; other callers were source-reviewed and exercised through the shared
component, not replayed as complete routes.

## Executed coverage

- 156 scenario combinations across self-hosted/Cloud edition styling, dark/Paper
  themes, and 1440, 390, and 320 pixel widths produced 180 screenshot and element
  bounds records. Expected loaded controls and fonts were awaited; no page or
  console errors, clipped controls, or document overflow remained.
- Shared secret setup covered no requirements, omitted `missing_secrets`, all
  saved, and the last missing reference becoming saved. Mixed ordinary services
  with repository, organization, and combined runner credentials retained the
  correct permission guidance and excluded random credential generation.
- Real service configuration forms lowered CPU request from 500m to 250m, CPU
  limit from 2 to 1, memory request from 1Gi to 512Mi, and memory limit from 2Gi to
  1Gi. Both editors reached review with the exact lower resources and the four
  before/after changes visible. Review sent no deployment request.
- Missing references blocked confirmation. Saving the last reference enabled
  confirmation without crashing. Fixture deployment requests carried the exact
  reviewed resources. A simulated rejected deployment preserved the review and
  edited form/TOML values when returning to configuration.
- Keyboard Enter saved a secret and moved focus to the completion heading. CPU
  input tab order was preserved. Mobile review actions worked by touch. Four
  additional narrow keyboard/touch cases checked visible focus, input-to-checkbox
  tab order, and the ordinary-secret generation disclosure.

## Applicable checklist result

- [x] Shared page insets remain 24px on desktop and 16px on mobile; review content
  and actions stay within the viewport. No layout code changed.
- [x] One page heading, readable summaries and resource changes, and usable
  confirmation/back controls remain visible in both themes.
- [x] Shared input/button/select components and edition styling remain intact.
  Self-hosted focus uses its outline; Cloud focus uses its existing input border,
  background and brackets. Rendered focus screenshots were inspected.
- [x] Empty/all-saved states render without an error boundary. Failed confirmation
  preserves the draft. Consequential changes still require explicit confirmation.
- [x] Secret values remain local to the input and the write-only secret request;
  final-secret completion removes the value field. Runner tokens still require
  provider-supplied values and the appropriate repository/organization permissions.
- [x] No new dependency, polling, runtime service, layout primitive or product copy
  was introduced by the fix.
- [x] Representative contact sheets covering every edition/theme/width and
  full-resolution narrow form/TOML, desktop review, and focused-input screenshots
  were independently inspected. No new visual defect was found.

## Evidence and limits

VM evidence is under
`/srv/hakopod-backup-scratch/service-resize-ui-review`: the isolated source,
`baseline.mjs`, `review.mjs`, `focus.mjs`, `evidence/results.json`,
`evidence/focus-results.json`, full screenshots and 23 contact sheets. The first
fixture omitted resource profiles; that incomplete fixture was corrected before
the final successful matrix and is not acceptance evidence.

All APIs and credentials in this review were artificial. Submitted deployment
checks prove fixture API payload acceptance and draft preservation, **not live
runtime resizing**. Cloud coverage uses the shared source and edition styling;
it does not verify production gateway/authentication or hosted capacity policy.
Kubernetes reconciliation, real capacity checks, Go authorization, full composed
builds and production rollout require separate verification. Unrelated navigation,
loading, denied-access and account states were not newly retested. The VM fixture
server and browser processes were stopped after review; nothing was built or
served on the user's local machine.
