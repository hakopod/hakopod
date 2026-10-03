# Managed platform security UI review

Reviewed on 2026-10-03 against the [dashboard checklist](ui-ux-checklist.md).
An independent reviewer approved the rendered changes with no corrections.

The review used explicitly marked development fixtures. It verifies the UI,
request handling and preservation of entered values. It does not establish
native Neon or Supabase readiness, certificate renewal or recovery success.

## Coverage

The reviewer captured 72 full-page screenshots in Chromium, using dark and
Paper themes at widths of 1440, 390 and 320 pixels. The Security tab covered:

- Verified certificates, waiting for the first check, expired and unverified
  certificates, an earlier platform revision and a failed state refresh.
- A failed public CA download, repeated failures without duplicate alerts,
  pending renewal, failed renewal and operator-managed certificates.
- The create and configure review pages in both themes at every width.

The review inspected screenshots as well as element bounds. It checked the
active tab inside the scrolling navigation, a single page heading, long-value
wrapping, table containment, warnings, action placement, keyboard focus and
touch interaction. No document overflow, error boundaries or clipped controls
were found.

Request inspection confirmed that configuring an existing Supabase platform
preserved both operator certificate references at their selected revision.
Creating a platform used managed TLS and omitted those certificate inputs.
The existing sibling detail tabs retained their layout and active treatment.

WebKit and Firefox were unavailable on the test VM. Their rendering was not
verified. The fixture server was stopped after review; no cluster resources
were changed.

Ignored local evidence is in
`work/database-enterprise/platform-ui-20261003/tls-review-captures/`. The VM
capture directory is `/srv/hakopod-build/platform-trust-ui-review-v1/captures`.
