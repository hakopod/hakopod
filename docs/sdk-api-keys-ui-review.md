# SDK API key UI review

Independent review completed on 2026-09-28 using the
[dashboard checklist](ui-ux-checklist.md). The reviewed API key interface passes
source, interaction, element-bound and screenshot review.

The review covers `web/src/routes/settings.tsx`, the account-menu entry in
`web/src/components/shell.tsx`, and the Cloud dashboard edition's settings gate.
Shared Dialog, SelectField, SettingsLayout, focus and spacing behavior were
checked where these changes use them.

## Rendered coverage

Both self-hosted and composed Cloud production dashboard builds ran in disposable,
resource-limited containers on the development VM. Their API calls went only to an
explicitly synthetic fixture proxy. No production account, key or workload was
used or changed.

| Coverage | Result |
| --- | --- |
| Both editions, dark and Paper, at 1440, 390 and 320 pixels | 12 complete key-flow cases passed |
| Empty key list, list failure and denied key-management access; both editions and themes at 390 pixels | 12 additional cases passed |
| Keyboard and touch; both editions and themes at 1440 and 390 pixels | 8 focused interaction cases passed |

The 24 flow/state cases produced 100 screenshots. All were inspected through
contact sheets, with full-resolution checks of the desktop list, Cloud creation
form, narrow review and failed creation state. Browser checks waited for the
expected content and fonts. There were no page errors or document-width overflow.

## Checked behavior

- The shared page inset measures 24px on desktop and 16px on mobile. Settings
  content fills its available track. Active settings navigation is red, without a
  selected border or background, and stays visible after a deep link.
- Self-hosted screens, forms and dialogs show no corner brackets. Keyboard focus
  uses a visible outline. Cloud retains its existing bracket treatment.
- Creation uses four fields in self-hosted installations and three in Cloud.
  Expiry and access use the shared SelectField with visible labels. Cloud omits
  application restriction.
- Review shows the name, project, environment, application restriction, expiry
  and exact permissions before any creation request. Management access includes
  Git, network and application-management permissions; Read only has no write
  permission. Management access rejects an application restriction without losing
  the entered value.
- A failed creation preserves the draft and supports going back, editing and
  retrying. Long key names and permission lists wrap within the review.
- Rotation reviews the existing key's scope, including when a previous creation
  used an application restriction. Its overlap warning remains visible.
  Revocation requires the exact key name and uses the destructive action style.
- The one-time key disappears after Done. Its copy and storage instructions remain
  readable at 320px. Help supports focus, Space, Escape and touch toggling;
  opening and closing a dialog restores keyboard focus to its opener.

Initial findings were resolved: the Cloud content gate, excessive form controls,
missing management permissions, inaccurate database-credential wording, missing
Access label, stale rotation scope and the Cloud account-menu shortcut. No review
findings remain open.

## Evidence and limits

Evidence is retained in `/opt/hakopod-sdk-work/review`: `results.json`,
`interactions.json`, fixture/review scripts, `screenshots/` and `contacts/`.
Cold-start and development-server captures were discarded. Open menus were
captured at the viewport size so screenshot resizing did not dismiss them.

This is UI approval for the listed routes and states. Synthetic responses do not
prove backend authorization, real key issuance, Cloud policy enforcement or
Kubernetes behavior; those require their separate API and acceptance checks.
The rest of the dashboard was not represented as newly reviewed.
