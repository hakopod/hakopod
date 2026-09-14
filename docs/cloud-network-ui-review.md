# Cloud private-network UI review — 15 September 2026

## Scope and fixture

Independent review follows the applicable Hatch checklist for Cloud network
list/creation permission guidance, self-hosted scoped-key permissions and the
latest light-theme Google-logo change. Production implementation was not edited
by the reviewer.

A local isolated fixture imports the actual public components from this
worktree, supplies ScopeContext and synthetic GET responses, and rejects every
mutation. It is at `/tmp/hakopod-network-ui-fixture`, served on port 4197. Its
small edition stub selects Cloud guidance for network cases and self-hosted for
keys; it is not a full Cloud navigation/authorization test. Fixture records are
visibly labelled and use `example.invalid` identities.

Cases prepared:

- `/networks?theme=dark&manage=false`: denied/empty Cloud list.
- `/networks/new?theme=light&manage=false`: denied Cloud creation.
- `/networks/new?theme=dark&manage=true`: owner form.
- `/settings?tab=keys&edition=self-hosted&theme=dark`: open Create API key.

Use the `theme=light` or `theme=dark` variants at desktop and mobile. The
self-hosted key screen is intentional: that installation issues the scoped
connected-node key; Cloud hides installation API-key settings.

## Source review

- Cloud denied-network copy names the workspace owner rather than a project
  administrator. Guidance identifies required node-key permissions, and the new
  page asks for a current node version and reconnecting the scoped key.
- Self-hosted retains project-administrator wording.
- The real key dialog includes a labelled `networks:write` checkbox alongside
  deployment and log permissions. Shared Input and checkbox-label styling remain
  in use. Help identifies the deployments:write prerequisite and disallows an
  application restriction. Backend enforcement is reviewed/tested separately.
- The shared Google mark is `brightness(0) invert(1)` under `.light` and remains
  black under dark mode. The button continues using the red theme token.

## Rendered review — approved for the scoped changes

The parent captured actual browser viewport screenshots after the subagent
connection failed. This reviewer independently inspected all 16 initial cases,
all 16 corrected final cases, four final auth-logo screenshots, the keyboard
capture and their DOM evidence. Evidence is in
`/tmp/hakopod-network-review-shots/`:
`final-{list,denied,create,keys}-{desktop,mobile}-{dark,light}.png`,
`final-bounds.json`, `key-keyboard.png`,
`logo-{desktop,mobile}-{dark,light}.png` and `logo-evidence.json`.
Contact sheets supplement direct full-resolution inspection of representative
controls and both theme variants.

- [x] Cloud denied list, denied creation, permitted creation form and self-hosted
  key modal reviewed at desktop 1280px and mobile 390px, Paper and dark.
- [x] Workspace-owner guidance and node-key permission names remain readable;
  long permission strings wrap without clipping. The list footer also correctly
  says workspace owners. Permitted creation shows the actual guided form.
- [x] Page insets and edge-reaching header dividers use the real shell classes.
  Final document widths are 1273/1280 on desktop and 383/390 on mobile for
  network pages; keys are 1280/1280 and 390/390. No horizontal overflow remains.
- [x] The key dialog is readable in both themes. Its mobile bounds are x12..378
  within a 390px viewport; body scrolling keeps controls and helper text inside
  the modal while footer actions remain visible. The new permission aligns with
  the existing checkboxes, is off by default and has a clear label.
- [x] Parent keyboard exercise pressed Space on networks:write and confirmed it
  became checked and remained focused. The independent keyboard screenshot
  shows a visible red outline. Parent then used Escape and confirmed zero open
  dialogs. No key was submitted or created.
- [x] Final light auth screenshots show the Google mark WHITE and matching light
  label on the red button. Dark screenshots preserve the black mark/dark label.
  Both desktop and mobile have all three provider icons loaded and legible.
  DOM evidence confirms the requested filters and positive image natural widths.
- [x] Source review includes the additional connection-empty guidance in
  `virtual-network-connect.tsx`, which selects workspace owner in Cloud and
  project administrator in self-hosted. This specific empty state was not
  separately rendered, so its coverage is source-only.

The only geometry finding came from the review fixture: its first version used
an invented `hako-main` class instead of the actual
`.hako-shell > main.page-content.hako-page-content` wrapper. This omitted shared
insets, min-width/grid sizing and bracket containment, producing 1–16px mobile
overflow. Correcting only the fixture and rerunning all 16 cases eliminated it;
no production CSS workaround was needed. The initial overflow screenshots are
not accepted as final product evidence.

No actionable defect remains in the reviewed changes. This approval covers the
scoped copy, checkbox, modal fit and auth-logo styling; it is not full Cloud
navigation, authorization, key creation or real-network acceptance. The fixture
omits the operational dashboard header/footer and supplies synthetic permissions.
Touch, no-JavaScript and reduced-motion modes were not emulated. The parent
closed review tabs and reset browser viewport overrides after capture. Reviewer
changes were limited to this document and disposable fixture/evidence files.
