# Agent control UI review

Independent review, 2026-10-08, using the [standing Hatch checklist](ui-ux-checklist.md). The reviewer inspected source, rendered screenshots and measured element bounds. The root agent captured screenshots and executed browser interactions; its execution record is `work/ui-review/agent-control-final/interactions.md`.

Status: scoped visual review complete. No remaining actionable visual finding in the covered states. This is approval of the inspected UI evidence, not release or runtime qualification.

## Coverage

All fixture screens are visibly marked DEVELOPMENT ONLY and use intercepted artificial API responses. Port 4201 renders actual platform route/components. Port 4202 renders the actual CloudRoleEditor inside a shared synthetic platform shell; this does not review the full Cloud dashboard shell.

| Family | Rendered coverage | Source coverage |
| --- | --- | --- |
| API keys | Form, review, synthetic success, list and rotation at 1440px and 390px in Paper and dark; mobile failed review; desktop application restriction and keyboard select; unavailable scope at both widths/themes | `settings.tsx`, `settings.keys.new.tsx`, `agent-grants.ts` |
| Project roles | Final create form/review at 390px in both themes; corrected existing-role edit/review at 1440px in both themes; synthetic failed review at 390px | `pro-access.tsx`, `agent-grants.ts` |
| Device consent | Selected and empty installation/host states at 390px and corrected 1440px in both themes; synthetic expired approval at 390px | `login.device.tsx` |
| SQL | Transactional and nontransactional write review, unknown outcome and unsupported engine at 1440px and 390px in both themes; MySQL read result at 390px; MyDuck read-only capability disabled and nontransactional write review at both widths/themes | `databases.$databaseId.query.tsx`, `lib/databases.ts` |
| Cloud roles | Form, review and existing-role edit at 1440px and 390px in both themes; synthetic failed review | Cloud `hosted/dashboard/overlay/src/components/cloud-collaboration.tsx` |
| Supplemental build/deployment | Build blocked, manual completion and linked to deployment, and linked deployment with stale or failed runtime at 1440px and 390px in both themes; queued build at 1440px dark | `builds.$buildId.tsx`, `deployments.$deploymentId.tsx`, `runtime-notice.tsx`, shared Pipeline |

Measured viewport width in `bounds.json` governs coverage. Early desktop-labelled device, final-role and SQL-read captures actually measured 390px and are excluded from desktop coverage. Corrected device/role screenshots measure 1440px. Contact sheets and selected full-resolution originals were critically inspected, including warnings, form labels, review actions, focus outlines and long results. Clean final key forms replace earlier screenshots containing a stale toast.

## Findings and checks

The custom-role credential disclosure finding was resolved. Both project and Cloud role forms and reviews visibly state that retrieved secrets can enter an agent's context and should be granted only to trusted agents/integrations. Screenshots confirm the warning in both themes. Key review and installation consent also disclose credential context exposure.

Source and root browser execution confirm SQL writes require SQL query access; credential access requires deployment access. Removing those prerequisite grants removes the dependent grant. Application-specific key restrictions clear and disable SQL and credential grants. Execution remains an explicit choice. Failed key/role requests retain entered values and review. Rotation Escape restores focus to its trigger. Select focus remains visible, using a simple outline.

Device consent clearly separates installation administration from host-terminal access. Empty scope states explain the missing access and disable approval. A synthetic expired approval preserves selection. These checks exercise UI behavior, not live scope authorization.

SQL review includes the target, revision, statement, parameters and execution mode. Nontransactional review warns that changes can persist after failure. Unknown outcomes preserve entered SQL and parameters and require another review. Read results visibly preserve SQL NULL and large numeric text; unsupported Redis does not render an executor form.

Supplemental build/deployment screens distinguish the CI result, recorded deployment outcome and current runtime observation. Root observed the actual ten-second fixture poll transition from queued through processing to deployed, followed by stopped polling. This is UI polling evidence, not deployment acceptance. Mobile pipeline stage buttons extend within an intentional horizontal scroller; root focused and activated Result with Enter, moving it fully into the viewport with a visible outline. Document width remains contained.

The inaccessible key-scope finding was resolved. The route verifies availability before rendering CreateKey. Final screenshots in both themes and widths show an unavailable message, no key form, and an unselected header scope. Root confirmed the inaccessible URL remained unchanged and no mutation was submitted.

The MyDuck capability supplement is visually approved in both themes and widths. A visible warning explains that this database cannot enforce read-only queries; the initial Run query action is disabled, and the user must explicitly select Read and write. Source disables the unsupported read option and guards execution with read_only_supported. Root CUA execution confirmed Read only has aria-disabled=true, Enter selects Read and write, and ArrowUp cannot return to the disabled option; Escape preserves write mode. The four final write-review screenshots show revision 4, Without a transaction, the persistence warning, statement and parameters, visible review focus and Execute write. These screenshots were independently inspected and remain contained/readable. This proves presentation and UI interaction, not database engine enforcement.

The desktop SQL viewport capture confirms the transaction warning and schema-change instruction remain readable above the form footer. Earlier full-page exports placed a sticky footer at the original viewport edge; the viewport inspection did not show a content overlap defect. Blocked and manual-completion build screenshots preserve the distinction between verified build image and accepted deployment. The intercepted manual-completion state has no Deploy action, so the manual deployment review flow is not covered.

## Limits

No backend authorization, credential retrieval, SQL engine execution, Kubernetes lifecycle, live deployment or public endpoint is proved by these fixtures. Physical touch, synthetic swipe, assistive technology, other browser engines, 320px layouts and the complete Cloud shell were not tested. The in-app browser rejected touch dispatch; keyboard scrolling was verified instead. Unrelated route families and unchanged loading/denied states are not represented as newly reviewed. No local build or test was run by the reviewer.


## SQL editor, Vitess scope and project settings supplement

Status: scoped independent visual review complete. No remaining actionable finding in the inspected states. The earlier completed review remains limited to its recorded states.

The independent reviewer used the standing Hatch checklist and inspected the actual project/settings/deletion routes, shared editor, parser worker and query route. Root captured the browser screenshots and executed interactions. All responses in these development fixtures are artificial and all screens display a DEVELOPMENT ONLY banner.

| Family | Final rendered coverage | Evidence |
| --- | --- | --- |
| Project settings | Base, empty, personal and viewer states in dark and Paper at 1440px and 390px | `work/ui-review/scope-deletion/*-final.png`, `*-empty.png`, `*-personal.png`, `*-viewer.png` |
| Environment deletion | Conflict with preserved staging confirmation in both themes and widths; Escape returns focus to the named trigger | `*-conflict.png` |
| Environment creation | Clean dialog in both themes and widths; light desktop conflict preserves the quality ID | `*-create-environment-clean.png`, `light-desktop-create-conflict.png` |
| Demo replacement | Confirmation, zero-project overview, creation review and resulting exact name/ID/environment; dark 390px only | `dark-mobile-demo-confirm.png`, `dark-mobile-demo-deleted.png`, `dark-mobile-replacement-review.png`, `dark-mobile-replacement-created.png` |
| Monaco SQL | Diagnostics both themes/widths; light desktop completion and correction focus; dark mobile help viewport; Vitess transactional write review and unknown query outcome both themes/widths; unsupported grammar dark both widths and light failure states | `work/ui-review/sql-monaco/v15-*.png` |
| Expanded TOML | Actual shared editor inside a bounded fixture parent in both themes and widths | `*-expanded-toml.png` |

The reviewer critically inspected each listed original. Settings titles and content retain the 24px desktop / 16px mobile inset. Project rename/delete and environment Open/trash controls measure 36px and share row positions. Empty projects remain reachable without selecting another environment. Personal projects disclose their account linkage and omit project/development deletion controls; viewers have navigation without mutation controls. These observations prove synthetic UI presentation, not backend authorization or deletion.

Deletion warnings, the preserved staging ID and error focus are readable. The mobile conflict dialog measures x=12, width=366 with a 324px input in a 390px viewport. Clean creation captures keep all field instructions, suggestions and actions visible. The transient conflict toast obscured help in an earlier capture; clean captures supersede it for layout approval.

Expanded TOML bounds are x=24, y=158, width=1392, height=654 at 1440x900 and x=16, y=209, width=351, height=598 at 390x844. Syntax colors, line numbers and wrapped values are readable; the editor fills its parent without document overflow. The collapsed preliminary fixture lacked Tailwind source discovery for fixture-only classes. The fix adds `review.css` with an explicit source declaration; it is not a product layout regression. This checks the shared expanded editor, not the full deployment dialog.

Earlier mobile-labelled desktop exports and collapsed fixture captures are superseded and excluded. Passing bounds did not substitute for screenshot inspection.

Source review confirms local advisory grammars for PostgreSQL, MySQL and Vitess; other SQL dialects disclose unavailable grammar lint. Valid bind placeholders or server extensions can produce advisory warnings. SQL is bounded to 64 KiB of UTF-8, diagnostics to 20 markers and messages to 512 characters. One worker holds at most one active and one latest pending request. Worker initialization after asset import is bounded to ten seconds and parsing to two seconds. Errors/timeouts terminate it; a later edit can start a new attempt without a timer-driven retry loop. Model-version checks discard stale diagnostics. Escape targets Parameters explicitly; root verified this focus transfer in the final browser pass.

The lazy SQL parser worker measured 3.763 MB before transfer compression. This is a material loading cost, not a performance benchmark. No page-speed, live SQL execution, Kubernetes lifecycle, deployment or release qualification is claimed. Physical touch, assistive technology, other browser engines, 320px layouts and unrelated route families were not newly tested.


Final v15 Monaco originals reviewed in `work/ui-review/sql-monaco` include diagnostic screens in both themes at 1440px and 390px, light desktop completion and corrected Parameters focus, and the dark mobile help viewport. Squiggles and F8 details are visible; syntax colors and controls are readable. Root verified SELECT FROM produces a warning, SELECT 1 clears it, blank SQL clears state, Ctrl+Space offers SELECT and Enter accepts it, repeated edits continue to lint and Escape focuses Parameters. These are UI/plugin behaviors, not execution approval. Mobile F8 text remains in Monaco's bounded viewport and can truncate long messages; full-message assistive-technology access was not tested. The advisory bind-placeholder instruction and Escape-to-Parameters help are fully visible below the editor, with no footer overlap in the inspected viewport.


Four final `v15-*-vitess-write.png` originals pass: the scope warning includes nontransactional requests, the reviewed revision is 4 and the review states One transaction on one shard. The statement and parameter 42 remain visible, mobile values wrap and actions fit. Four `v15-*-query-failure.png` originals preserve SELECT $1 and the quoted value 9007199254740993 after a synthetic unknown outcome, with readable error and operation ID. Dark desktop/mobile `v15-*-unsupported-grammar.png` originals also show explicit unavailable grammar guidance; light guidance is covered by failure states. The injected ClickHouse placeholder style and outcomes are fixture values, not native dialect qualification.

Root's accessibility snapshot exposed the full mobile F8 message; its visual horizontal truncation remains recorded, and no assistive-technology test was performed. Some desktop full-page exports place the sticky action footer across earlier content. The final corrected-focus and help viewport captures show the instructions without actual overlap; those viewport checks govern that conclusion. This supplement approves the recorded presentation and interactions only. It does not prove native query execution, live scope mutation, rollout, or release readiness.

## Oracle Free query supplement

Status: scoped independent visual review passed on October 8, 2026. No actionable findings remain in the inspected states.
The reviewer followed the Hatch checklist for the newly available Oracle query form and its shared editor.
The fixture displays a DEVELOPMENT ONLY banner and uses artificial API responses. Product source did not change for this review.

The reviewer critically inspected 12 full-page captures and eight ordinary viewport captures in dark and Paper themes at 1440×1000 and 390×1000.
Each configuration covered the initial form, nontransactional write review and an unknown query outcome.
The initial Run query action was disabled. Read only was unavailable, and the user had to select Read and write explicitly.
Oracle placeholder instructions and unavailable grammar guidance remained visible. Escape moved focus from Monaco to Parameters.
The Parameters accessible name remained stable after editing. Review focus had a visible two-pixel outline in both themes.
The review displayed the target, revision, statement, parameters and persistence warning. Unknown outcomes retained SQL and parameters and required another review.

Full-page exports placed the offscreen skip link and sticky footer over earlier content while scrolled.
The ordinary viewport captures showed no such overlap. The unfocused skip link measured x=8, y=-49, width=148, height=38, with its bottom at -11.
The viewport captures govern the overlap assessment. Root also inspected representative mobile review and desktop failure captures.
Document widths remained 1440 and 390 pixels. Mode controls measured 48 pixels high, and review actions measured 40 pixels high.

A dark 390×1000 browser context enabled synthetic touch with one reported touch point.
Taps opened Query mode, confirmed the unavailable Read only option, selected Read and write, opened and closed Statement help, and opened the write review.
The two touch screenshots showed readable help, warnings and review actions within the viewport. Physical-device testing is not claimed.

Evidence is in `work/ui-review/oracle-free/`: `results.json`, `viewport-results.json`, `touch-results.json` and the corresponding PNG files.
The headless browser needed task-local Fontconfig settings and fallback fonts. No system packages or product files changed for that repair.
This supplement does not repeat the earlier Monaco suite or prove database authorization, live SQL execution, Cloud rendering, deployment or release readiness.
Assistive technology, other browser engines, 320-pixel layouts and physical devices remain unverified.
