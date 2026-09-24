# Deployment secret setup UI review

Independent review of the shared missing-secret setup on 2026-09-24. The review
used the standing [dashboard checklist](ui-ux-checklist.md) and contributor rules.
All test values and API responses were synthetic. No production account, secret,
workload, or deployment was changed.

## Coverage

- The real shared `DeploymentSecrets` component rendered in a parent form and the
  shared Dialog: 16 combinations of self-hosted/Cloud styling, dark/Paper themes,
  and 320/1280-pixel widths. Each exercised missing references, save failure,
  successful value creation, generated creation, and all references saved.
- Real route/component integrations rendered in 32 combinations: TOML
  `DeploymentForm`, repository import, connected application source review, and
  built-image deployment review. All loaded their expected controls before
  screenshots. The source-import footer received another eight-case review after
  its action label was shortened.
- Four narrow keyboard/touch variants checked rechecking a secret created
  elsewhere, stale draft clearing, conflict preserving the draft, multiline
  editing, visible focus, generation format selection, duplicate-submit
  prevention, and focus after advancing to the next secret.
- Four visible failure screenshots and four no-reference states were checked.
  Missing-secret setup disappears when no references are required. No nested form
  is introduced. Enter in the hidden input saves its value; Enter in multiline
  input stays an editing action. Neither sends a deployment while references
  remain missing.
- Source inspection covered the Compose integration through its shared
  `DeploymentForm` review, the permission guards on source/build routes, and the
  native/npm CLI secret prompts. CLI flows offer enter/generate/cancel, hide
  supplied values, refuse unresolved secrets in noninteractive mode, and explain
  that generated randomness cannot supply provider credentials.

## Checklist result

- [x] Shared page insets and component bounds remain intact at 320 and 1280
  pixels; long reference names wrap within the component.
- [x] The component uses shared input, button, error, and select primitives.
  Save/generate use the primary action variant.
- [x] One value is edited at a time; the disclosure exposes generation format
  and guidance without creating a large multi-input form.
- [x] Password input is hidden; multiline visibility is explicit. Values remain
  component-local and are sent only to the write-only creation endpoint.
- [x] Provider credentials, certificates, and existing passwords are distinguished
  from generated random secrets.
- [x] Save failures and already-exists conflicts preserve the current draft.
  Advancing to another reference or scope clears the prior draft.
- [x] Loading disables mutations. Final deployment remains unavailable until
  no references are missing; existing final deployment review is preserved.
- [x] Keyboard focus is visible, next-secret focus is restored after saving or rechecking, and
  checkbox/disclosure touch areas are at least 44 pixels high. Shared buttons
  measure 40 pixels high and the format select 48 pixels.
- [x] Self-hosted styling removes decorative brackets; Cloud retains its shared
  styling. Dark and Paper screenshots were inspected, including full-resolution
  narrow import, TOML, failure, and dialog states and route contact sheets.
- [x] No document overflow, secret-control overflow, or page errors in the final
  16-component and 32-route matrices.

## Findings resolved

The initial implementation could retain a draft when rechecking advanced to a
different missing reference. It now clears drafts on reference/scope changes.
Long names initially forced the grid beyond a 320-pixel viewport; minimum-width
and wrapping fixes resolved this. Checkbox and disclosure targets were enlarged.
Explicit Enter handling and request-completion focus restore keyboard continuity.
Save/generate now use primary button styling. The source import's long final
button label extended outside its narrow footer panel; `Create application`
restored alignment. Recheck now restores focus to the current or next value
after its request completes. The final independent UI review is approved within
the coverage and limits recorded here.

## Evidence and limits

Ignored local evidence is in `work/secret-setup-review/`: `results.json`,
`route-results.json`, `footer-results.json`, `edges.json`, review scripts,
screenshots, and route contact sheets. Early captures made before fixture schema
corrections are not acceptance evidence. Error toasts were explicitly dismissed
for the generation-layout captures; separate visible-error captures record the
failure presentation.

Cloud coverage here uses the shared source and Cloud edition styling. This is
not a live Cloud gateway/authentication test. Compose's conversion editor and
native/npm terminal rendering were source-reviewed, not replayed end to end in
this UI review. Backend authorization, atomic creation, CLI tests, and production
rollout belong to their separate verification records. Unrelated navigation,
account, and permission combinations were not newly retested.
