# Free collaboration and paid audit UI review

Independent review completed 14 September 2026 for the UI in `1d36d7b`, with
backend/auth contract changes through `8a7192b`. **Approved for the reviewed UI
scope.** No implementation changes were made by this reviewer.

The review used the real Go API against a newly created disposable PostgreSQL
fixture, without Kubernetes access or workers. All people, teams and audit
records were explicitly synthetic. The browser used the built dashboard at
`http://127.0.0.1:3018`, with the fixture API at `127.0.0.1:8188`.

## Coverage and evidence

Evidence is in ignored `work/enrollment-ui-review/`. `results.json` records route
bounds; `interactions.json` and `states.json` record targeted behavior. The
screenshots were inspected at their actual rendered sizes, not approved solely
from overflow checks.

| Surface | Coverage | Result |
| --- | --- | --- |
| Teams & access | Free and Pro; 1440/390 Paper and dark; 320 dark | Team controls remain available on Free; headings and actions align; mobile layout stays within the viewport. |
| License & features | Same ten plan/viewport/theme cases | Teams, invitations and fixed project roles are marked Free. User audit history/export remains Pro. Installation/license IDs wrap at 320px. |
| Audit events | Same cases, plus selected-user Pro history | Recent events remain visible on Free with a clear paid-history explanation. User selector and CSV action are disabled in Free. A direct Free history request returned HTTP 402. |
| User history | Pro, all five viewport/theme cases | Selected identity remains clear and accessible. Long resources scroll within the table rather than expanding the document. Pagination returned 100 recent records, then 23 older records, then restored the latest page. |
| CSV export | Pro, desktop Paper | A real API download produced `hakopod-user-audit.csv`, with 124 lines including its header at the time of export. The fixture was below the 1,000-event export-page limit. |
| Invitation registration | 1440/390 Paper and dark | Join/personal choices, field labels, password rules and primary action remain readable. The valid local invitation works while public registration is closed. |
| Team/invitation dialogs | 390 Paper/dark; keyboard check at 1440 | Dialogs fit the viewport; role menu fits x33–357 at 390px. Escape closes the selector before the dialog. Keyboard-opened dialogs restore focus to Create team / Invite member. |
| Empty and failure states | Pro history at 390 Paper; invitation failure at 390 dark | Empty history shows a useful message. A deliberately simulated 503 shows an error while preserving the selected user. Password mismatch and simulated session failure preserve entered registration values. |

A final real invitation acceptance created the disposable invited account and
its actual team membership; a subsequent authenticated `/api/teams` read returned
`Local review team` with role `member`. No email was sent. The team invitation
form correctly disables email delivery and visibly explains the available copy
link path when SMTP is unconfigured.

## Standing checklist for this scope

The full standing checklist remains [ui-ux-checklist.md](ui-ux-checklist.md).
Applicable items were checked as follows; unrelated route families are not
represented as newly retested.

- [x] Shared 24px desktop / 16px mobile page inset; no duplicate settings inset
  or width cap; aligned headings and content; full-width heading divider.
- [x] Compact heading/action rows with one page h1 and clear subordinate section
  headings. Existing license summary decoration was not changed in this slice.
- [x] Active Settings navigation is theme-aware red without a selected fill,
  underline or border. The active mobile section is brought into view.
- [x] Desktop header remains one row; mobile controls reflow without document
  overflow. Long values remain contained.
- [x] Existing shared Hatch controls, tokens and SelectField are used. Both
  Space Grotesk and Space Mono report loaded in the final built preview.
- [x] Keyboard activation, visible focus, Escape and focus restoration checked
  for affected controls; touch opening checked at 390px.
- [x] Registration is a dedicated route; labels and password instructions remain
  associated with their inputs. Failed submissions preserve values.
- [x] Initial, selected, empty, denied and failure states remain usable. Expected
  route controls, query results and fonts were awaited before final captures.
- [x] The paid history capability is enforced by the backend, and the Free
  recent-event log remains readable. The review did not rely on UI disabling
  alone to establish the entitlement boundary.
- [x] Audit data came from the actual disposable API. Requests and page sizes are
  bounded; exported data is a real API response. No invented cluster state or
  production account was used.
- [x] Copy distinguishes Free collaboration from paid user history/export. No
  paid Cloud offering or hosted enrollment availability was inferred.

Project/default-scope navigation, application/service grids, endpoint links,
cluster observations, broader secret management, and unrelated route families
were outside this change and were not rerun. The installer OAuth prompts were
source/test work by the implementer, not browser UI exercised by this review.

## Review corrections and limits

The initial Vite preview refused font files outside its allowed root because
its dependencies were symlinked from the main checkout. Those fallback-font
captures were rejected. The parent replaced Vite with the built dashboard server,
and the route matrix was recaptured with loaded bundled fonts. An early run also
stopped at route readiness; the complete Free rerun and independent route probes
confirmed the final loaded states. Dialog/menu screenshots wait for their opening
transition to finish; initial transparent transition frames are not final evidence.

This review used installed Chromium and emulated mobile/touch viewports. It is
not a Safari/Firefox or physical-device certification, a production delivery
check, or a new verification of real Kubernetes behavior. Go tests, dashboard
build/type tests and installer tests remain the implementer's separate checks.
