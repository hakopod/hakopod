# Slack integration UI review

This review follows the [dashboard UI checklist](../ui-ux-checklist.md).
The review covers Settings navigation, the Integrations catalog, the nested
Slack management and configuration pages, and the Cloud edition workspace
extension. The website review is recorded in that repository's
`docs/ui-review.md`.

## Review status

The independent reviewer approves the affected dashboard UI in the states
recorded here. Source review, the current 40-case dashboard matrix, expanded
event recovery screens, keyboard-focus repairs and final interaction captures
are complete. No actionable visual finding remains in this scope. This is UI
approval only; the runtime and release limits below still apply.

The later application-notification policy increment is recorded separately in
the legacy destination review below.

Earlier `branded-`, `native-` and `final-`
dashboard screenshots under `work/slack-ui-review-2026-10-04/` are historical
evidence: route nesting, edition styling, integration cards and event selection
changed afterward. They do not approve the expanded dashboard source.

The user's Slack brand-mark request also supersedes captures taken before that
change. The current matrix includes consistent Slack identification in the
catalog, page titles, setup and Pro locks, in both themes. This explicit brand
requirement takes precedence over the general rule against decorative heading
icons. The user's request for integration cards and visible descriptions likewise
applies to the catalog cards and individual notification events.

The dashboard and website both use the exact user-supplied Slack JPEG, with
SHA-256 `0f77eb68933e4c442406f6c7cf9fd333b34490c56177405c107f22c185be4e08`.
The shared form component uses a separate brand-mark slot, preserving the
previously ignored legacy icon strings used by unrelated routes. The real Slack
detail route owns its branded access-denied heading; the fixture follows the
same Settings-to-Integrations-to-Slack route hierarchy.

The reviewer session lost its browser connection during the final review.
The lead agent is collecting the current VM preview screenshots and browser
measurements for independent inspection. Those observations will be identified
separately from the reviewer's direct screenshot judgments.

## Source review

The review identified and prompted fixes for:

- A missing Settings entry and Cloud settings allowlist entry for Integrations.
- Cloud workspace owners being denied access to the nested Slack route.
- An owner exception that initially also covered unrelated integration routes.
- A catalog action that skipped the management page containing delivery history,
  test delivery and disconnect controls.
- Setup cancellation returning to the same setup page.
- A Cloud access-denied title that incorrectly required an installation admin.
- A saved channel disappearing while asynchronous channel choices loaded.
- A failed save refetch overwriting the user's draft after a revision conflict.
- Disconnect errors appearing outside the active confirmation dialog.
- Cloud Pro denials linking to the self-hosted license page.
- A delivery creation timestamp being labeled as an attempted delivery time.
- Fixture route nesting, edition attributes, Cloud owner identity and router
  search-state handling that did not match product behavior.
- Unsupported saved event IDs being retained even though the API rejects them.
  The form now visibly requires their removal before a save, and writes changed
  event selections before a changed channel.
- A search-field Enter key implicitly submitting the event configuration form.
- Partial saves losing the successful mutation's revision before retry.
- Revision-conflict copy suggesting an unavailable retry. A dedicated reload
  action now loads the current server state while preserving the local draft.
- Channel discovery ignoring the API cursor. Later pages can now be loaded in
  bounded requests, with manual channel-ID entry after the page limit.
- Mutation fixtures reading only `fetch` options when the typed client passes a
  `Request`. The fixture now reads the method and cloned body in both forms.
- Keyboard focus reaching event controls hidden behind the desktop sticky
  footer. The browser reproduced four covered controls in a 16-tab sample.
  Enabling the existing focus-reveal helper initially exposed only each input;
  screenshot inspection still found its description covered. The helper now
  measures the entire wrapping checkbox or radio label. The latest 16-sample
  desktop measurement keeps every complete label above the footer. The final
  uncropped native screenshot independently confirms the complete label,
  description and focus outline above it.
- Missing actionable channel membership guidance. The form now instructs users
  to invite the Hakopod app to the desired Slack channel before selection.
- A delivery-history scroll container that accepted keyboard focus and Arrow
  scrolling without displaying a visible focus outline. The final keyboard
  screenshot exposed this after the functional scroll check passed. It now has
  an explicit name, region role, tab stop and simple visible focus outline.
  The final screenshot confirms the repair.

The review fixture uses explicit artificial records. It cannot establish API
authorization, OAuth success, entitlement enforcement, actual Slack delivery,
production availability or live workspace ownership. Builds and service tests
belong to the lead agent's separate verification record.

## Rendered coverage

The lead agent operated the VM preview browser, collected real element bounds
and captured the images. The independent reviewer inspected the original image
files. No reviewer browser actions or local builds were used in this pass.

| Route or state | Current screenshot coverage | Result |
| --- | --- | --- |
| Integrations catalog, Settings selector, Slack management, event configuration, self-hosted setup, expired entitlement, denied access and request error | Eight states in Paper and dark, desktop and narrow mobile: 32 images | No visual blocker in the inspected states |
| Cloud owner setup and Cloud Pro lock | Two states in Paper and dark, desktop and narrow mobile: 8 images | No visual blocker in the inspected states |
| Filtered events and grouped review | Paper desktop | Labels, descriptions and selection summaries remain readable |
| Partial save and successful retry | Paper desktop | The error identifies the saved event change, preserves the draft and exposes retry |
| Unsupported saved events and revision conflict/reload | Paper desktop | Recovery requirements and retained choices remain visible |
| Empty first channel page, later channel choices and page limit | Paper desktop; page-limit fallback also on mobile | Saved option and manual channel-ID entry remain usable |
| Missing event catalog | Dark mobile | The visible error closes the review/save path |
| Keyboard focus | Dark mobile, plus Paper desktop full-label measurements and native screenshot | Full focused label and description remain above the sticky footer; delivery-history region has a visible outline |
| Invitation guidance, pending test and disconnect dialog | Dark mobile; disconnect also Paper desktop | Guidance, status and consequential-action wording fit and remain readable |

Current matrix files are `expanded-{catalog,settings,management,events,setup,expired,denied,error}-{paper,dark}-{desktop,mobile}.jpg`
and `expanded-cloud-{owner,unpaid}-{paper,dark}-{desktop,mobile}.jpg`.
The primary measured desktop viewport was 1440 × 900 CSS pixels; the main mobile
matrix and Paper Cloud cases used 320 × 844. The dark Cloud mobile capture
reported 291 × 767, which is recorded as the actual width rather than described
as 320 pixels. The lead reported completed fonts and decoded visible logos.
The reviewed content remained inside the document width. Scrollbar and native
capture dimensions differ slightly from the CSS viewport in some images.

The independent image inspection found readable integration cards and event
descriptions, a usable narrow Pro lock, wrapping self-hosted manifest content,
and an intentionally horizontally scrolling delivery table. Self-hosted controls
use simple focus outlines without decorative corner brackets. Cloud retains its
own edition styling.

The source review covers shared `SelectField` usage, native links through shared
Buttons, page nesting, server-provided catalog data, bounded channel discovery,
one-channel configuration, write-only credentials and explicit Pro/owner/admin
states. Project/application navigation, unrelated route families, and application
grid limits are outside this incremental Slack review.

The shared focus helper remains opt-in and defaults to disabled. Its existing
non-Slack consumers were also inspected in source: template setup, platform
creation/configuration, and Hakopod Edge configuration, including its nested
traffic-policy fields. Their checkboxes use compact single-control wrapping
labels. The change only substitutes that label's bounds for native checkbox or
radio bounds; inputs without a wrapping label retain their own bounds. Other
control types, viewport/header/footer logic, the oversized-element guard and
listener cleanup are unchanged. No additional source defect was found in these
consumers. Those three route families were not newly screenshot-tested here.

## Scoped checklist

This applies the standing checklist to the affected route family; a checked
item describes the source and rendered evidence in this record, not a claim
about every dashboard route.

- [x] Shared 24px desktop and 16px mobile insets, aligned headings and content,
  full-width page dividers, and no duplicate nested width caps.
- [x] Tailwind layout utilities and shared components, readable fields and
  compact grouped controls in both themes.
- [x] One main title per reviewed state, sensible subordinate headings,
  nested Settings navigation and explicit Slack branding requested by the user.
- [x] Visible integration descriptions and per-event descriptions as requested;
  necessary status, errors, warnings and field instructions stay visible.
- [x] Catalog cards have no additional outer panel; long names and manifest
  content wrap or scroll within their component.
- [x] Shared Button links and `SelectField` preserve names, values, disabled
  choices and the saved selection while choices load.
- [x] More-than-four-input configuration stays on its nested page and includes
  a review step; failure and conflict paths preserve the draft.
- [x] Inspected route captures show loaded content, fonts and visible logos,
  including explicit denied, expired and request-error states.
- [x] The fixture is clearly artificial and product code consumes API data;
  it does not fabricate live cluster state, metrics or Slack delivery.
- [x] Source review checks write-only credentials, role and entitlement state,
  bounded requests, limits, and stopped duplicate submissions.
- [x] Final native screenshot and full-label geometry confirm that keyboard
  focus reveals the whole checkbox label above the desktop sticky footer.
- [x] Final mobile screenshot confirms the actionable channel invitation text.
- [x] Delivery-history region is explicitly named, keyboard focusable, visibly
  outlined and scrollable with Arrow keys.
- [ ] Actual touch and assistive-technology interaction remain unverified;
  pointer and keyboard observations are recorded separately.

Default project selection, resource-detail scope, endpoint-link truncation,
application/service grid caps and unrelated account or infrastructure screens
were not changed by this slice and are not newly approved by this review.

## Interaction coverage and remaining limits

The consolidated lead-agent browser observations are in
`expanded-interactions.md`. The independent reviewer inspected the associated
screenshots. A pending test stays visibly pending, without claiming receipt.
The disconnect dialog explains the consequence, initially focuses Keep
connected and closes with Escape. Its mobile and desktop images were inspected;
the destructive action uses the named trash icon and retains a distinct style.

The final desktop focus check has 16 settled samples in
`expanded-full-label-focus.json`, all with the complete wrapping label above
the footer. The independent reviewer inspected
`expanded-events-full-label-focus-paper-desktop.jpg` and
`expanded-events-final-row-paper-desktop.jpg`: both label text and the simple
focus outline remain visible, and the final service entry and actions are
reachable. Earlier input-only `expanded-keyboard-desktop-fixed.json` measurements
and `expanded-events-focus-fixed-dark-desktop.jpg` are superseded; they missed
the covered description and are not evidence of the final repair.

The lead observed that searching for deployment shows 8 of 38 catalog entries,
Enter keeps the editor open, and Select shown increases the selection from two
alarms to ten events without losing hidden choices. Clear shown returns the
selection to the two hidden alarms. The independent reviewer inspected the
resulting filtered, cleared and grouped-review captures. The current real
catalog has 38 entries; the 64-selection boundary was source-reviewed and is not
claimed as a rendered maximum-capacity interaction.

The partial-save fixture covers an event change followed by a failed channel
update and successful retry. Its error and retry captures were inspected: the
draft retains three events, the security channel and revision 8, and retry
returns to the management view with those choices. Exact HTTP request bodies
were not separately captured by this browser pass.
The current save order updates events before the channel so a legacy unsupported
ID can be removed and the channel changed in one submission. Revision-conflict
captures show an explicit reload and retained draft; the permanent-conflict
fixture retains revision 7, so those captures do not prove a changed server
revision or a subsequent successful conflict resolution.

These are UI fixture checks only; they do not establish backend validation,
authorization, OAuth, delivery or live workspace ownership.

The channel captures cover an empty first page with a continuation cursor,
manual loading of later pages, a retained selected option, and the ten-page limit
with manual channel-ID entry. Two later pages add choices without duplicate
selected entries. Entering `C0123456789` in the fallback selects that ID.
Preservation after a later pagination request error has passed source review
but was not exercised in this browser pass. The API verifies manually entered channel IDs on save;
the artificial fixture does not verify a real Slack channel.

At 320 pixels, Tab from the delivery-history help button focused the scroll
container and Right Arrow moved its `scrollLeft` from 0 to 40. The first
independent screenshot review found no visible outline. After the repair,
`expanded-history-focus-final-dark-mobile.jpg` shows a clear simple outline
around the named region, fully inside the viewport. The lead measured a 2px
outline and table bounds from x=16 to x=304, y=496 to y=636, at 320 × 844.
The reviewer independently inspected that final image and closed the finding.

Actual touch gestures were unavailable through the earlier browser connection.
Pointer and keyboard checks must not be described as touch verification.
Physical devices, assistive technology and other browser engines remain outside
this pass. The real-cluster, API, Cloud, build and test outcomes are recorded in
[the validation record](slack-validation.md), separately from visual approval.

## Reviewed source fingerprints

SHA-256 values collected for the reviewed source, including the full-label focus,
channel invitation and delivery-history focus changes, are:

| Source | SHA-256 |
| --- | --- |
| `web/src/components/slack-settings.tsx` | `c672db4268af4d5a8f210d43270e941abc45c518d2dfbe39b8b7e9c74d1d470a` |
| `web/src/components/form-page.tsx` | `c71e521fa62cdc62eabb750cb5de3f0cc0b4231ea2020aa42c74ab4b52372685` |
| `web/src/components/slack-logo.tsx` | `58b552a3065ea96f1389a70d9c77343a67519b248649f41d720a085335c5fc02` |
| `web/src/routes/settings.integrations.slack.tsx` | `be82aa363c170387bb26f47bc50280d67392d28ad404951ab581ced921183674` |
| `web/src/routes/settings.integrations.tsx` | `c3e9e2e559cb4885858e38033ed7525f8c6f650e1de72d0ac6f5db4359d43eff` |
| `web/src/routes/settings.tsx` | `2b34cc8573e585ddb0d6641b3a7a76f61112d8ac341bab8295da35a4d4544fb6` |
| `web/src/styles/console.css` | `b294e288ea9a7f3804f79f02be460d564802644d0f8856431eedc4fabe4fefd0` |

## Legacy application notification destinations — 4 October 2026

The independent review approves the legacy destination policy UI in the
recorded states. This extends the Settings integration review above to
`applications.$applicationId.notifications.tsx`; it does not repeat unrelated
dashboard coverage. The reviewed route has SHA-256
`b1952eebe7e8c20ae544ffd6be556aac3736da7175cc40d369a2f86dc97a44f8`.

Two independent reviewer passes inspected 47 original legacy screenshots. The
first reviewer inspected 45 images; a second reviewer inspected only the final
pause notice and migration review summary. The lead agent operated the VM
preview browser and supplied the interaction observations and element bounds.
Neither reviewer built locally or sent a real notification.

| Coverage | Captures | Result |
| --- | --- | --- |
| Self-hosted free, Pro and expired entitlement, plus Cloud | 32: Paper and dark, desktop and mobile, top and form views | Policy guidance and destination controls are readable; Slack review is disabled except with self-hosted Pro |
| Missing policy | 4: Paper desktop and dark mobile, top and form views | Unavailable policy keeps Slack review disabled and leaves the explanation visible |
| Failed save | 2: dark mobile | The error and retained draft fit the narrow viewport |
| Pause reset, delete confirmation and completion, generic migration and selector keyboard state | 7: dark mobile | Observed state changes and visible focus remain usable |
| Final pause success notice and migration review summary | 2: dark mobile | Correct pause wording, complete consequential-action guidance and actions fit without clipping |

The matrix uses `legacy-{selfhost-free,selfhost-pro,selfhost-expired,cloud}-{paper,dark}-{desktop,mobile}-{top,form}.jpg`
under `work/slack-ui-review-2026-10-04/`. The missing-policy captures use
`legacy-policy-unavailable-*`, and the two failure captures use
`legacy-failed-save-*`. The lead's `legacy-geometry.json` records 1280 × 900
desktop and 320 × 844 mobile CSS viewports, document widths of 1273 and 313
pixels, bounded controls and decoded Slack images with a natural width of
1200 pixels. The exact approved Slack JPEG is unchanged. These geometry
observations support the independent visual review; document width alone was
not used as visual approval.

The lead exercised pause, deletion and migration in the explicitly artificial
fixture. Pausing reset the form. Deletion initially focused Cancel, Escape
returned visible focus to the delete trigger, and confirming deletion removed
the fake destination. Replacing a generic Slack webhook with a non-Slack URL
enabled Review. Return, Up Arrow and Return selected Discord and kept focus
on the centralized selector. Saving the generic migration succeeded in the
fixture and enabled Send test. The first reviewer inspected all seven resulting
screenshots: `legacy-pause-success-dark-mobile.jpg`, the three `legacy-delete-*`
images, `legacy-migrate-review-enabled-dark-mobile.jpg`,
`legacy-selector-keyboard-dark-mobile.jpg` and
`legacy-migrate-success-dark-mobile.jpg`.

The pause success text originally implied that future messages would be sent.
The final `legacy-pause-notice-dark-mobile.jpg` instead shows “Destination
paused. Notifications will not be sent.” above the destination list. The second
reviewer confirmed its wrapping and placement. The final
`legacy-migrate-review-summary-dark-mobile.jpg` shows the replacement host,
enabled state, chosen event, future-only behavior and the warning that editing
skips messages queued with previous settings. Save remains fully visible and
Cancel edit has a clear simple focus outline. No actionable visual finding
remains in these two final captures.

This focused pass applies the standing checklist to policy guidance, shared
controls, failed-request preservation, confirmation content, theme contrast,
narrow wrapping and keyboard focus. The screenshots are development fixture
evidence with no destination contacted. They do not establish transport,
entitlement or authorization enforcement, live cluster state, production
availability or a release. Actual touch gestures, physical devices, assistive
technology and other browser engines remain unverified. Runtime and test
claims belong to the separate validation record.
