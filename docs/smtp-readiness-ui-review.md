# SMTP readiness and automatic certificate UI review

The independent UI/UX review passes for the changed application cards, service
overview and service Networking panel on `fix/self-hosted-smtp-cutover`.
The review followed [the Hatch checklist](ui-ux-checklist.md), including the
current 24px desktop and 16px mobile page inset. The earlier zero-gutter assertions
in the delivery fixture were not reused.

## Scope and method

The affected production components are the shared readiness label, application
service cards, service overview and `ServiceDelivery`. The reviewer used their
actual source in an isolated Vite fixture at `127.0.0.1:4198`. Every fixture page
is labelled as artificial. All requests outside that origin were blocked; no
real account, certificate, application, SMTP endpoint or cluster was changed.

The source audit compared the UI wording with the readiness and backend
certificate documentation. Screenshot checks waited for the expected panel,
completed fonts and loaded responses. The deliberate loading case instead
required its loading indicator. Geometry checks reject unexpected page overflow,
incorrect shared insets and missing fixture API responses.

## Completed checks

- [x] Forty route/state/theme/viewport cases passed, with no page or fixture API
      errors. All affected views ran at 1484 and 390 pixels in dark and Paper themes;
      service cards and pending certificate status also ran at 320 pixels.
- [x] Application cards and service overview show the combined
      `/health + SMTP STARTTLS :2525` readiness label. SMTP-only configuration shows
      `SMTP STARTTLS :2525`. Existing process-health wording remains visible for an
      unmodified worker service.
- [x] Automatic certificate status covers healthy, pending, unavailable API,
      missing source metadata and loading states. Pinned uploads remain identified
      separately. Missing/pending metadata does not display a valid-certificate badge.
- [x] Long hostnames, read-only mount paths, role references and readiness labels
      wrap within their cards at narrow widths. Full-page and panel screenshots were
      inspected, including the application cards, service overview, pending/missing
      certificate messages and unavailable-state panel.
- [x] Four help interaction runs passed at desktop/mobile widths in both themes.
      The revised help text distinguishes pinned uploads from automatic ingress
      renewal. Keyboard focus is visible, Escape dismisses help, mobile taps toggle
      it, and the tooltip stays within the viewport. Focused screenshots were inspected.
- [x] An open automatic-certificate panel refreshed from Valid certificate to
      Needs attention on the next 15-second metadata interval.
- [x] Leaving the Networking tab discarded the certificate query and produced no
      additional reads after another interval. An in-flight read was aborted on tab
      exit. Pinned uploads retain no interval, background refresh disabled and zero
      query retention after unmount.

The only production finding was metadata freshness: the original certificate
query fetched once, which could leave an open automatic-renewal panel showing an
old validity state. The implementer added a 15-second interval only when an
ingress-source mount exists, retained `refetchIntervalInBackground: false` and
`gcTime: 0`, and preserved the abort signal. The lifecycle checks above verify
that fix without adding a persistent cache or background service.

The initial tooltip probe focused while the page was still settling after a
scroll, causing Radix to dismiss it. Repeating the established settled-scroll
keyboard sequence passed in all four combinations. Those initial failed captures
are diagnostic evidence, not sign-off results.

## Evidence and limits

Local evidence is under the ignored
`work/ui-migration/smtp-readiness-review/` directory:

- `results.json`: forty loaded-page, geometry and fixture-response results.
- `help-results.json`: final four keyboard/touch help results.
- `interactions.json`: the four successful query lifecycle checks; the earlier
  tooltip attempts in this file are superseded by `help-results.json`.
- `screenshots/cards-light-320.png`: narrow application service cards.
- `screenshots/overview-dark-390-panel.png`: combined readiness in the service overview.
- `screenshots/healthy-dark-1484-panel.png`: automatic source validity and expiry.
- `screenshots/pending-light-390.png`: pending source with the existing mount retained.
- `screenshots/missing-dark-390.png`: missing source metadata.
- `screenshots/error-light-1484-panel.png`: unavailable certificate observations.
- `screenshots/help-dark-390.png` and `help-light-1484.png`: inspected help and focus states.

Browsers and the isolated fixture server were stopped after review. This review
does not repeat unrelated dashboard routes or establish SMTP deliverability,
certificate issuance/renewal in production, AWS permissions, firewall exposure or
the actual kubelet probe behavior. Backend and named-cluster acceptance tests
cover those implementation boundaries separately; browser fixtures prove only
the reviewed UI behavior. The periodic refresh is a sampling interval, not a
real-time guarantee.
