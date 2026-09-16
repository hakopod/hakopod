# Backup and restore UI review — 2026-09-16

Independent review of the shared backup page heading, Restore backup shortcut,
stored-backup empty state and Syne Data resource link. This is a scoped UI review;
it does not establish that a production backup or restore completed.

The reviewer followed [the dashboard checklist](ui-ux-checklist.md), the root
contributor instructions and Hatch's public component guidance.

## Coverage

The review used the current shared route components and styles in an isolated,
explicitly labeled fixture. All records and HTTP responses were synthetic. The
fixture did not connect to a real API, object store, cluster or customer account.

| Surface | Rendered states |
| --- | --- |
| Job history | Populated, empty, request failure |
| Stored backups | Populated, empty, Restore navigation |
| Object storage | Populated destination list |
| Schedules | Populated schedule list |
| Restore | Compatible target selection, no compatible target, reviewed plan, rejected restore |
| Access | Non-administrator denial |

- [x] All four tabs and the restore states rendered at 1440px and 390px in dark and Paper themes. Additional empty-history layouts were checked at 320px and 768px in both themes.
- [x] Loaded-content assertions waited for the expected records or empty state and fonts; no unexpected page exceptions occurred.
- [x] Screenshots and element bounds were inspected. Headings retain the shared 24px desktop / 16px mobile inset; page dividers reach the viewport edges. No document overflow was found. Existing wide tables scroll within their container.
- [x] Header actions have matching heights and an 8px gap at 1440px and 390px; they wrap without clipping at 320px.
- [x] Restore backup navigates to Stored backups using keyboard activation and mobile touch. An artifact's Restore action reaches the target review form.
- [x] Restore requires a compatible target and exact database-name confirmation. A rejected fixture request preserves the entered confirmation. The no-target state keeps the submission disabled.
- [x] Active tabs remain visible when selected. Existing red selection and visible keyboard focus are preserved.
- [x] Heading help opens by keyboard focus and touch, remains inside the viewport and dismisses with Escape in both themes.
- [x] The resource link is visibly underlined, has a keyboard focus outline, names its new-tab behavior to assistive technology and uses `noopener noreferrer`. Its URL is `https://data.synehq.com`; no external navigation was performed during review.
- [x] The promotion remains below the data and actions with a 24px gap, wraps on mobile, and adds no background service or polling.
- [x] Non-administrators do not see the backup or restore actions.

## Finding resolved

Hatch's unlayered paragraph and anchor resets overrode the footer's initial
Tailwind utilities. The promotion touched the table and its link had no underline.
The footer now uses a scoped semantic class with `@apply` in the existing shared
stylesheet. The final render verifies the 24px gap and underline in both themes.

## Evidence and limits

Local evidence is in `/tmp/hakopod-backup-ui-review`: `review.mjs`,
`results.json` (98 passing checks), `extra-results.json`, `help-results.json`,
full-page PNGs and four theme/viewport contact sheets. Representative captures are
`dark-390-jobs.png`, `light-1440-artifacts.png`, `light-390-plan.png` and
`dark-390-restore-error.png`.

This pass used the real shared page container and route components with a fixture
identity provider. A subsequent Cloud-shell pass is recorded below. Neither pass
reviewed production permissions, actual database contents, object-storage credentials, scheduler
execution, backup encryption or successful restore execution. The destination and
schedule editor routes, job inspection and artifact deletion were unchanged and
are not newly verified by this pass. Backend acceptance and deployment checks are
recorded separately.

## Cloud shell verification

The composed Cloud dashboard was also rendered using its actual `DashboardShell`,
edition controls and edition gate with a synthetic scoped operator session. The
fixture displayed a persistent label identifying its artificial state.

- [x] Job history, the Restore backup shortcut, stored-backup empty state,
  schedules and the resource link passed at 1440px and 390px in both themes.
- [x] Cloud branding, scope selectors and active Backups navigation rendered with
  the new page heading; the operator edition gate allowed the backup route.
- [x] Heading insets, footer spacing and page bounds remained correct. Desktop
  and mobile screenshots were inspected; no new overlap or clipping appeared.
- [x] The shortcut worked by keyboard and touch. Mobile navigation could reopen
  Backups, and the promotion remained keyboard focusable.

This added 30 passing checks with no page errors, recorded in
`/tmp/hakopod-backup-ui-review/cloud-results.json`. The source fixture and review
script are in the same directory; screenshots use the prefix `cloud-`, including
`cloud-dark-1440-jobs.png`, `cloud-light-390-jobs.png` and
`cloud-dark-390-stored.png`. No production endpoint was contacted.
