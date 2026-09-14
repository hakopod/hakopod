# Installation settings independent review

Review date: 2026-09-14. Source review completed; rendered UI approval is pending.

The reviewer implemented the SMTP backend and did not author the Settings UI.
This pass covers Teams and access, email delivery, GitHub/Google/GitLab sign-in,
and enterprise OpenID Connect settings. Git connection UI has a separate owner.
The standing visual gate is [the dashboard checklist](ui-ux-checklist.md).

## Source coverage

| Area | Inspected behavior | Rendered status |
| --- | --- | --- |
| Teams and access | Compact team selector and member actions; first-team and additional-team gates; role edit/cancel and removal confirmation | Pending |
| Email delivery overview | Effective saved configuration, fixed-recipient test request, disabled test control, visible failures | Pending |
| SMTP editor and review | Nested form, connection/security fields, credential retain/replace/clear, revision and impact review | Pending |
| Sign-in provider overview | GitHub, Google, GitLab and enterprise SSO cards, entitlement and configured-state distinctions | Pending |
| Provider editor and review | Nested forms, callback display, OIDC issuer validation, write-only secrets, review before save | Pending |
| Restricted access | Cloud/non-admin controls stay unmounted; direct nested routes have guards; unlicensed users can inspect and disable enabled providers | Pending |
| Failure handling | Entered drafts survive request failure; stale revision prevents resubmission and directs the administrator to reload saved state | Pending |

Source uses the shared Button, SelectField, FormPage and form-section components.
Long forms have dedicated routes, meaningful labels and explicit review steps.
SMTP test requests contain only expected_revision; the backend fixes the message
and recipient to the current administrator. Credentials are never included in
review summaries or settings GET responses.

One source finding was resolved in the frontend before this record: SMTP inputs
accepted values above backend limits. The updated helper checks UTF-8 byte limits
for host (253), sender (254), username (256) and password (4096), and rejects
username controls and password NUL/CR/LF. Input maxima now match those limits.
The frontend includes multibyte boundary and forbidden-delimiter regression tests;
this reviewer inspected those tests but did not rerun the frontend suite.

## Backend review and verification

SMTP backend verification passed the full Go suite against temporary PostgreSQL
databases and local SMTP fixtures, focused API/store race tests, command tests,
vet and diff checks. It covers encrypted persistence, revision conflict, credential
retention/clear, fixed test recipient, STARTTLS/implicit TLS, dynamic account/alarm
delivery, disabled override, Cloud fallback and cancellation. No external email
was sent. The isolated backend checkout lacked dashboard dependencies, so its
attempted dashboard build could not start; combined frontend build results belong
to the frontend/integration record.

Independent source review also identified and relayed two login checks: bounded
settings-handler database work, and rechecking enabled state/configuration after
external token exchange. The login implementer added both. Git source review
identified a connection-disable race during repository import. The Git implementer
added a post-fetch revision check and a share lock on the exact connection inside
initial acceptance. Its internal expected revision stays outside the idempotency
digest so already accepted retries remain recoverable. The reviewer inspected
these fixes and the GitLab source OAuth session/PKCE and refresh-generation guards;
this does not claim live GitHub/GitLab or identity-provider verification.

## Outstanding visual gate

- [ ] Pro, Free, expired-license and Cloud fixtures rendered with real API data.
- [ ] Teams, SMTP and provider overview/editor/review states inspected in dark and Paper.
- [ ] Desktop and mobile screenshots inspected after data and fonts settle.
- [ ] Actual element bounds, 24px/16px shared insets, navigation visibility and overflow checked.
- [ ] Keyboard focus, review/edit focus transitions, SelectField and help interactions checked.
- [ ] Touch interaction, mobile menus and long values checked.
- [ ] Live stale-save/failure retention and Cloud/non-admin deep-link states checked.
- [ ] All visual findings resolved before UI publication or source freeze.

The browser API was unavailable. Native CUA Chrome was accessible, but it refused
a separate review-window attempt while the user was actively using Chrome. No
fixture route was opened and no fixture interaction or screenshot was completed.
The reviewer stopped native interaction; existing production tabs were untouched.
The user subsequently directed code work to continue and visual review to wait.
The disposable API/UI fixtures were stopped by their owner and their temporary
databases removed; restart scripts are retained locally. This record grants no
visual sign-off and must be completed before publishing the UI.

## Git connection UI source follow-up

The same independent reviewer subsequently inspected the named Git connection
list/editor/review, GitLab OAuth callback, repository connection selector,
application source/build/import propagation, legacy-route redirect and named
webhook proxy. This follow-up also grants no rendered approval.

Three findings were resolved in source and re-inspected:

- Git editor requests now use a base revision captured with the draft. A focus
  refetch can no longer advance expected_revision while keeping old field values.
  Deletion uses that same captured revision.
- Explicit migrated github-default/gitlab-default IDs normalize only for selector
  presentation; they no longer appear as unavailable connections. Saved bindings
  keep their actual connection IDs and never substitute the first named record.
- Source/import selectors honor read_source=false for OAuth connections needing
  authorization. Unavailable selected values remain visible and disabled, and
  valid public-repository defaults remain selectable. Build selectors separately
  honor the builds capability.

The source includes meaningful regression cases for explicit defaults and denied
source access. The reviewer independently ran
`node --experimental-strip-types --test src/server/git-webhook.test.ts`: both
cases passed. These verify exact signed bytes, removal of browser Authorization,
Cookie/Origin and forwarded-address headers, response-cookie stripping, strict
webhook endpoint paths, POST-only access and the 512 KiB request bound.

OAuth authorization navigation is limited to the exact GitLab authorization
origin/path. The callback clears code/state from the visible URL and completes
through the authenticated API proxy; backend same-session, PKCE and revision
checks remain authoritative. The page uses the shared no-referrer policy.
Credentials stay in password/private-key inputs and are omitted from review rows;
the newly generated webhook secret is shown only on the immediate save result.
Error handling preserves draft state, and consequential saves/removals have
review or typed confirmation. No further actionable source finding remained in
this scoped pass. Browser focus behavior, screenshots, responsive layout and live
provider authorization still require the deferred rendered/integration review.
