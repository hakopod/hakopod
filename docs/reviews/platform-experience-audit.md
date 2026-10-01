# Hakopod platform experience audit

October 1, 2026. Current source: Hakopod `85e07e0`, Cloud `2768be7` with the matching engine. This audit is separate from the completed runner UI changes in public PR #149 and Cloud PR #79.

## Assessment

The main problem is task organization. Primary flows expose the configuration model in a long sequence of settings, even when the user is creating one service or changing one value. The component library supplies consistent colors and controls, but it cannot compensate for repeated forms, excessive competing context, and actions that do not match the current scope.

The recommended direction is a compact service workspace: choose a source, focus one service and one group of settings, retain a summary of the rest, and review the actual changes. Use cards for meaningful choices, chips for finite choices and scope, and compact rows for variables. Keep names and free text as familiar inputs.

See [product direction](platform-experience-direction.md) for the proposed flows and shared layout contract, and [route inventory](platform-experience-routes.md) for the platform surface map.

## Evidence and limits

- Source inventory covers 75 public TSX route modules and 11 Cloud visual overlay modules. Route modules are not the same as rendered states.
- Browser evidence runs actual source components on the development VM with artificial API records. It proves presentation for those records, not live deployments, authentication, authorization, or provider operations.
- Earlier `stable-*` and `v2-*` screenshots are superseded: their Paper theme was reset during mounting. They are not accepted as both-theme coverage. Final evidence uses `v3-*` or later captures with computed theme verification and valid public assets.
- V3 task-input counts include rendered inputs, textareas, and comboboxes within the main task; they are not all required fields. Earlier all-control counts included shell/navigation controls and are superseded. Page length is supporting evidence; it is not, by itself, a usability score.
- This pass changes no product source, production state, or customer workload. Findings below remain open until implementation and verification.

## Findings

### UX-01 · High: multi-service configuration repeats the full editor

**Surface:** application configuration and the guided image creation path.

**Evidence:** `DeploymentForm` maps every service into a fully expanded editor (`web/src/components/deploy-dialog.tsx:611`). The valid V3 three-service development fixture exposes 67 task inputs and produces a 5,823px document at 1440px and 9,465px at 390px in both themes. The form repeatedly presents placement, serverless, image, credentials, compute, command, variables, import, and access. These measurements describe this fixture, not every possible configuration. [Desktop screenshot](evidence/v3/v3-three-services-light-1440-viewport.png), [mobile screenshot](evidence/v3/v3-three-services-dark-390-viewport.png).

**Impact:** adding a service increases the amount the user must scan. Related service settings are difficult to compare, and a small change is buried in repeated setup content.

**Change:** retain a compact service list and open one service editor at a time. Show changed, incomplete, and removed states in the list. Keep the full application draft intact when switching services. Review the complete deployment once.

**Acceptance:** create three services, switch between them, change an earlier service, resolve a validation error, and reach review with every draft intact. Make removals and shared-resource consequences explicit. Check mobile back navigation and focus restoration.

### UX-02 · High: single-service editing exposes irrelevant application settings

**Surface:** `/applications/:id/configure?service=...`.

**Evidence:** the selected-service editor already filters other services, but still renders the application name, release recovery, source-mode switcher, and the full selected-service settings sequence. V3 exposes 17 task inputs and measures 2,108px at 1440px and 3,305px at 390px in both themes. On mobile, the beginning of the page is dominated by context and application settings before the first service-specific control. [Mobile screenshot](evidence/v3/v3-web-edit-light-390-viewport.png).

**Correctness issue:** the release recovery selector at `web/src/components/deploy-dialog.tsx:562` updates `spec.recovery`. The targeted API path at `internal/api/api.go:621` starts from the existing application and copies only the selected services. It does not copy the incoming recovery policy. The UI therefore offers a policy change that the targeted operation does not apply. This is independently source-confirmed; live API acceptance was not rerun for this audit.

**Change:** open the requested service and setting group directly. Replace fixed application fields with compact context. Put application recovery in an application policy task and omit its editable control from service scope.

**Acceptance:** an image change opens at image/source; compute opens at compute; unrelated services and shared policy remain untouched. Cover a recovery-policy attempt in a focused regression test when implementing the fix.

### UX-03 · High: environment management spreads one task across repeated rows and separate sections

**Surface:** create/configure service, application defaults, and service environment pages.

**Evidence:** `EnvironmentFields` already supplies a responsive row grid and .env import. However, each existing plain value repeats Name/Value labels and a separate Store as secret action. The row layout is followed by a new-secret section, import controls, and a separate secret-reference section. Source also conditionally inserts an inherited-values section. V3's eight-variable development fixture is 2,063px desktop and 3,609px at 390px in both themes. Existing secret-reference context appears far below the first editable values. Inherited-default behavior needs its own fixture state. [Paper desktop](evidence/v3/v3-env-many-light-1440-viewport.png), [Ink mobile](evidence/v3/v3-env-many-dark-390-viewport.png).

**Impact:** users must navigate several visual areas to understand the effective environment. Repeated controls consume space without helping distinguish scope or source.

An existing empty-string variable also displays the example placeholder `production`. Empty values are meaningful overrides, so an inspection row should show an explicit empty state rather than resemble a populated value.

**Change:** use a compact variable workspace with name, value/reference, source/scope, and contextual actions together. Keep Add variable, Add secret, and Import together. Open import and multiline editing on demand. Preserve clear separation between plaintext values and protected secret references without requiring a second management journey to understand what is attached.

**Acceptance:** zero/one/many variables; inherited and overridden values; empty strings; missing and long secret references; multiline input; duplicate import; secret promotion; review; conflict; failed save with retained draft. Existing secret values must remain write-only and absent from review diffs and logs.

### UX-04 · Medium: add-service and source selection ask users to compare forms

**Surface:** `/applications/:id/services/new` and application creation.

**Evidence:** the add-service route renders catalog/Compose actions, an HTTP-function form, and a container-image form together before handing the draft to `DeploymentForm`. V3 shows a 1,396px mobile page for just four task inputs because the competing paths and repeated explanation occupy so much space. A disabled function path still presents its name input and both starter buttons before the image form. [Mobile screenshot](evidence/v3/v3-add-service-dark-390-viewport.png). Source inspection also shows five creation source-method choices when Git is available; some switch the editor while others navigate to another workflow.

**Change:** begin with a small set of clearly named source choices. Selection opens that task, with concise help and one primary continuation. Keep Compose/TOML discoverable. Avoid a wizard step whose only purpose is moving unchanged fields to another page.

**Acceptance:** every existing source path remains accessible; back/change-source behavior preserves compatible drafts; unavailable capabilities have visible reasons; initial focus and errors follow the chosen path.

### UX-05 · Medium: build creation and build editing use different information structures

**Surface:** `/builds/new` and `/builds/:id/edit`.

**Source evidence:** `BuildForm` already has Repository, Build recipe, Runtime, and Review steps. Its edit conditions (`build || step === ...`) reveal all sections together. Adding another wizard to creation would miss the existing design and the edit-specific problem.

**Change:** preserve the working creation steps; simplify dense steps based on rendered evidence. Give editing consistent task navigation and direct entry to the relevant group, with a compact summary and change review.

**Acceptance:** repository, recipe, runtime and triggers remain independently discoverable. Review supports changed values; switching groups preserves draft and validation context. Do not force existing users through every onboarding step.

### UX-06 · Medium: permanent explanatory rails add weight without supporting the current decision

**Surface:** application, service and environment form pages using `FormPage` help.

**Evidence:** the accepted environment desktop screenshot devotes a permanent right-hand column to two general explanations while the variable editor runs for several screens. The single-service view repeats application scope and review guidance before its task controls. The page already has named heading help controls, so explanation has several competing locations.

**Change:** keep task context as a compact summary, with contextual help beside the decision it explains. Reserve a persistent side panel for a useful live summary, an inspection task, or an actual review—not repeated onboarding prose. Retain necessary safety instructions, failed-dependency reasons, and warnings visibly beside their controls.

**Acceptance:** required guidance remains available with hover, keyboard and touch. Removing a help rail must not introduce another page inset or arbitrary width cap. Check heading/action balance and peer control alignment at every supported width.

### UX-07 · Medium: shared input and select heights break the grid

**Surface:** the Port, Size, Replicas row in service configuration; other mixed-control rows require the same check.

**Measured evidence:** in the focused V3 desktop rendering, all three controls start at Y=441.30px. Port and Replicas are 44px high and end at Y=485.30px; Size is 48px high and ends at Y=489.30px. The select extends 4px below its peers. The row has no horizontal overflow, illustrating why overflow checks do not catch visual alignment defects.

![Annotated crop of the actual fixture rendering; the line marks the input bottom edge](evidence/control-alignment-evidence.png)

**Change:** define consistent size variants for shared single-line controls and use one variant for a peer row. Retain shared heading/control/help tracks so wrapped labels or validation do not reintroduce offsets. Apply the rule to mixed input/select rows across route families rather than padding this one field.

**Acceptance:** compare top and bottom edges for every affected peer row, both themes, normal and wrapped labels, validation, and disabled states. Keep textarea growth intentional. Review actual screenshots after geometry checks.

## Additional family findings

**Database creation:** the valid Paper desktop rendering has a clear two-column layout and a review action, but Name/Engine already demonstrates the shared 44px/48px control mismatch. Raw CPU (`250m`) and memory (`512Mi`) are primary choices. Offer resource presets with visible capacity, retain custom allocation as an optional task, and keep engine/version/layout choices focused. This page does not need the same extensive restructuring as the multi-service editor.

**DNS credentials:** the valid mobile rendering already groups credential, permitted zones and availability and has a review step. Preserve its security instructions and scope model. Improve the fixed-provider presentation and keep scope decisions compact; avoid adding unnecessary onboarding screens to this shorter administrator task.

**Edge policy:** the loaded configuration page groups traffic protection, ordered rules, and a JSON settings editor. The safety instructions are useful, but repeated wide explanatory blocks and an always-present advanced editor compete with rule management. Prefer a compact ordered-rule list, focused rule editing, and an optional advanced settings task. Preserve first-match behavior and visible protection-disabled state.

**Cloud compute:** current component evidence shows unrounded capacity amounts such as `0.30078125 GiB` and several paragraphs of allocation explanation in one card. Round display values consistently and put actionable allocation facts in a compact summary. Preserve the distinction between requested, allocated and available capacity. Component captures do not establish the full workspace route's navigation or authentication behavior.

**Coverage:** V5 renders ten added public surfaces in both themes at 1440px/390px: database catalog/create/import, alarm inbox/settings, DNS catalog/create, secret-provider catalog/create, and backend certificate setup. The edge page was recaptured after correcting the fixture's self-hosted auth mode and required edge-policy schema; its four final captures are valid. Default catalogs use explicit empty fixture records. This is representative-family coverage, not all conditional states or runtime acceptance.

**Cloud coverage:** Cloud V5 renders six actual overlay component surfaces—workspace onboarding, plans, shared compute, BYO node inspection, team access, and approvals—in both themes at 1440px/390px, for 24 captures. All loaded expected bodies and fonts without console errors. Compute now uses the ready fixture state and displays Allocated. These standalone components do not establish full route-shell alignment, Cloud authentication, entitlement enforcement, or live operations. Earlier Cloud V4 availability content is superseded.

Cloud geometry found a 4px horizontal bracket overhang in BYO inspection at both widths and team/approvals on desktop. The wide elements are decorative bracket spans. Cloud permits bracket styling; keep the geometry finding qualified until inspected inside the full route shell. It is not evidence of the larger content overflow suspected in the earlier font-invalid captures.

**Original public families:** the final V5 matrix covers 20 representative routes across catalog/template, source builds/create/edit, deployment details, backups, networks, infrastructure/registry, settings/profile/integrations, host access and authentication. Each ran at 1440px/390px in both themes: 80 captures. All rendered expected bodies, correct theme and fonts, with no console errors or document overflow. Two mobile catalog captures have a blank vLLM icon, so 78 pass the complete asset check. The asset returned HTTP 200; the rendering cause remains unconfirmed. Infrastructure's mobile table has an inner horizontal-scroll/clipping concern requiring interaction verification.

The whole platform's source route inventory is complete; rendered coverage is a representative audit of route families. It does not cover every saved state, provider branch, denied state, error/review path or all route modules individually. Keyboard/touch help, draft retention and full Cloud shell behavior remain unverified in this audit. The existing runner review has separate deeper interaction coverage. No redesigned platform flow is implemented or released by this audit.

Independent evidence records: [public review](public-independent-review.md), [Cloud review](cloud-independent-review.md), and [priority review](independent-review.md). Exact capture metadata lives in `evidence/v3`; the complete screenshots and reproducible fixture remain on the development VM under `/srv/hakopod-backup-scratch/platform-audit-20261001/ui-review`. The two owned preview servers were stopped after capture; evidence was preserved.

## Implementation order

| Order | Slice | Why first | Required evidence |
| --- | --- | --- | --- |
| 1 | Scoped service editor and application policy separation | Fixes an ineffective control and the most common small-edit overload | Targeted payload regression, keyboard/mobile task flow, both-theme screenshots |
| 2 | Multi-service workspace and source chooser | Removes repeated full editors from application creation and configuration | Three-service draft, add/remove/back/review/error states, source path parity |
| 3 | Shared environment workspace | Improves creation and updates at application and service scope together | Import/secrets/inheritance/conflict/failure and long-row matrix |
| Alongside 1–3 | Shared control sizes and row tracks | Prevents the same 4px mismatch in each redesigned flow | Peer top/bottom bounds with wrapped labels, errors and both themes |
| 4 | Build edit and remaining setup flows | Applies the same interaction patterns without rewriting working shorter tasks | Family-specific cases and shared layout geometry |

This order is a proposal for implementation. The audit itself is not a production sign-off, and the runner PRs do not contain these platform redesigns.
