# TCP and certificate UI review

This review covers the service Networking tab's TCP/workload-access panel, the
nested backend certificate form and the existing HTTP TLS upload dialog where
shared file-input styling changed. It does not replace earlier reviews of other
routes.

The reviewer used the [Hatch checklist](ui-ux-checklist.md), contributor rules,
shared tokens, Button, SelectField, form layouts and help controls. The isolated
fixture served real dashboard components at `127.0.0.1:4194`. All records were
artificial and every request outside that origin was blocked. No production
account, certificate or deployment was changed by the UI review.

## Coverage

The route matrix covers configured, empty, expired and unavailable delivery
states; the certificate form for HTTP and TCP-only services; and denied access.
Each runs at 320 and 1484 pixels in dark and Paper themes. Assertions wait for
loaded components and fonts, reject fixture schema errors and inspect element
bounds. Screenshots also receive visual inspection; geometry alone is not approval.

Interaction checks cover uploading into a review, explicit deployment, retained
filenames and inputs after returning from review, reuse of an uploaded reference,
failed upload/deployment, disabled controls during requests, private-ingress
source availability, keyboard focus, tooltip focus/Escape/touch and native
navigation from the service to its certificate form.

Additional cases cover long filenames, the existing HTTP TLS upload dialog,
loading delivery observations, read-only roles and unavailable certificate
observations. The Go acceptance tests separately verify authorization, immutable
certificate storage, service ownership and actual read-only mounts/rotation in
the named development cluster. Browser fixtures do not prove those backend behaviors.

## Findings resolved

- Private/TCP-only services no longer offer an unavailable ingress import.
- Files retained in state after returning from review have visible filenames.
- Upload-file size checks do not interfere with an ingress import.
- Section help stays beside its title. The mobile delivery heading wraps its
  action below the title instead of squeezing the title into three lines.
- Shared native file controls fit narrow fields, including the existing HTTP TLS
  upload dialog.
- Certificate/key fields use the shared two-column form grid with a mobile gap.
- Retained filenames wrap within file fields, including valid filenames over
  180 characters long.

## Completed review

- [x] All 28 route/state/theme/viewport cases passed with no page or fixture schema errors.
- [x] Four interaction runs passed, covering both themes and widths.
- [x] Four additional runs passed for long filenames, existing HTTP TLS uploads,
  loading/error states and read-only roles.
- [x] Page wrappers have no outer horizontal padding; headings have balanced
  padding; controls and long values stay inside the viewport.
- [x] Final screenshots of forms, review/error states, delivery details, long
  filenames and the shared TLS dialog were inspected. The earlier cramped
  headings, unspaced file fields and overflow are resolved.
- [x] Fixture browser processes and the isolated server were stopped afterward.

The independent review passes for these affected routes and states. It does not
claim production SMTP deliverability, DNS ownership or AWS authorization from
browser fixtures. Local evidence is under `work/ui-migration/delivery-review/`, including fixture sources, route and
interaction results, element bounds and screenshots. Earlier failed captures are
retained for diagnosis and are not sign-off evidence.
