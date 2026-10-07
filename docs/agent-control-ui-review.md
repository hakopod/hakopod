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
