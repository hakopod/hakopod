# Service action dialog UI review — 15 September 2026

Independent review of DeleteServiceDialog, ServicePowerDialog and their service-card menu integration. **Approved for the scoped UI changes.** Production implementation was not edited by the reviewer.

## Source and safeguards

- Service actions are shown only with `deployments:write`; the APIs enforce that permission against the requested application scope.
- Delete opens a review without changing the application. Review removes the selected service and its domains, preserves other dependency references for server validation, and displays the server diff and warnings before separate destructive confirmation.
- The dialog freezes the accepted application snapshot. Plan checks its application ID and revision; deployment sends the reviewed expected revision and an idempotency key. Removing the last service is supported for existing applications. Persistent-volume retention matches cluster behavior.
- Stop/Resume explains lost availability or restored replica/autoscaling configuration and sends the expected revision with an idempotency key. Runtime tests and real-cluster acceptance are owned by the implementation task, not this visual review.
- Synchronous guards and disabled pending controls prevent duplicate submission. HTTP 409 disables stale confirmation and gives explicit close-and-review guidance. This resolved the review's initial stale-plan retry finding.
- Shared Hatch Dialog, Button, Icon and DiffTable components are used. Failures are announced with alert roles; pending dialogs block dismissal.

## Rendered evidence and checklist

The disposable `/tmp/hakopod-service-action-ui-fixture` on port 4200 imports the real components and styles. It visibly labels synthetic records and handles all requests locally; no deployment occurs. The reviewer browser could not authenticate, so the parent captured the browser and reported interactions. This reviewer independently inspected the screenshots.

Accepted evidence under `/tmp/hakopod-service-review-shots/`:

- `native-stop-light-desktop.png`, `native-resume-dark-desktop.png`.
- `native-resume-dark-mobile-final.png`.
- `native-delete-light-mobile.png`, `native-delete-review-light-mobile.png`.
- `native-delete-dark-mobile-long.png`, `native-delete-dark-mobile-review.png`, `native-delete-dark-mobile-conflict.png`.

- [x] Shared dialog layout checked at desktop 1280px and mobile 390px across Paper and dark themes.
- [x] Long service title and domain/service diff values wrap; descriptions, warnings and controls remain legible.
- [x] Mobile dialog remains within x12..378 (width 366) of the 390px viewport. The tallest conflict view keeps the footer and close action accessible.
- [x] Cancel, destructive Delete/Stop, and primary Resume are visually distinct; footer targets have matching height.
- [x] The review screenshot shows visible red keyboard focus, not a blue browser box. Parent observed initial Cancel focus, Tab+Enter advancing to review, Escape closing and focus returning to the opener.
- [x] Stale conflict screenshot shows the alert, retained review context and disabled Delete service. The error does not suggest that deletion succeeded.
- [x] No self-hosted decorative bracket was introduced by these dialogs.

Initial `delete-*-dark-mobile`, `stop-light-desktop` and `native-resume-dark-mobile` screenshots had a capture scaling/blank-canvas artifact. They are superseded by the accepted native captures above and are not responsive evidence.

No actionable UI defect remains in this scope. This is not a fresh audit of all service-detail routes, real deletion, authentication or backend authorization. Touch, no-JavaScript and reduced-motion modes were not emulated. Reviewer changes were limited to this document and disposable fixture files.

## Shared catalog disabled-title styling

The related shared `.ops-card-link:disabled` rule uses Tailwind `@apply bg-transparent` to keep stretched card titles flat while pending. It overrides the generic disabled-button surface without changing disabled behavior or cursor. The reviewer inspected the final Paper desktop synthetic catalog-card capture and source; disabled title backgrounds were transparent in parent DOM evidence, with no rectangular fill over the names. Icon-only Copy remains the existing shared component mode. This scoped style change also passes; it does not add edition-specific implementation to the public UI.
