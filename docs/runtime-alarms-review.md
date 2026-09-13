# Runtime status and alarms review

The independent review applies the [Hatch checklist](ui-ux-checklist.md) to current workload status, historical deployments, the alarms inbox and alarm delivery settings. The independent reviewer signed off after the final layout and interaction rechecks. This pass covers only the runtime and alarm changes listed below.

## Runtime truth

- [x] A historically succeeded deployment remains a historical outcome. It does not label a currently blocked application or service healthy.
- [x] Current pending or unschedulable workloads show an actionable warning, zero ready replicas and the observed scheduling reason. The warning remains visible without opening help.
- [x] Application lists, application/service headings and deployment inspection agree about current state while preserving immutable release results.
- [x] Healthy, recovering, stale and unavailable observations have distinct, truthful states. Missing metrics remain unavailable rather than fabricated zero.

## Alarms and delivery

- [x] The inbox distinguishes active, recovered, acknowledged and observation-unavailable alarms. Acknowledgement does not recover or hide an active fault.
- [x] Filters and native resource links preserve scope. Keyboard focus, activation, touch and independent acknowledgement actions work.
- [x] Empty, loading, failure and permission-limited states are useful. Failed requests retain entries and do not claim success.
- [x] Installation, project, environment and application settings show the effective scope, inheritance and allowed controls.
- [x] Email availability reflects configuration. Disabled or unavailable delivery is clearly explained. Enabling email is not presented as proof that a message was sent or received.
- [x] Settings validation and revision conflicts preserve the draft. No live email, configuration or workload is changed during review.

## Layout and cost

- [x] Compact headings and edge-to-edge wrappers follow shared Hatch tokens. Active navigation is visible and has accent text/underline without fill.
- [x] Both themes and mobile/desktop states load fully before measurement and screenshot inspection. Controls remain readable and there is no document overflow.
- [x] Lists, polling, caches and review buffers are bounded in source. Alarm polling is configured to stop in hidden views. Reviewer browsers and the fixture server were stopped after review.

## Evidence

The isolated fixture is under `work/ui-migration/alarms-review/`. It preserves successful historical release data while independently varying current runtime observations, and mocks alarm reads/writes. External browser requests are blocked; no production account, workload, configuration or outgoing email is changed.

| Coverage | Cases | Evidence |
| --- | --- | --- |
| Runtime lists, application/service detail and release history | 48, dark/Paper at 320 and 1484 pixels | `runtime-results.json`, with the four expected unavailable-service error states verified by `runtime-service-unavailable-results.json` |
| Alarm inbox, filters and email availability settings | 24, dark/Paper at 320 and 1484 pixels | `alarm-results.json` |
| Runtime notice, deployment controls and contextual links after fixes | 12 | `runtime-final-results.json` |
| Stale and unknown replica counts in lists, cards, service detail and topology | 32 | `replica-results.json` |
| Keyboard, native links, touch, pagination/Back, requests, conflicts and permissions | 11 scenarios | `interaction-results.json` |
| Stale-event guards, 1280-pixel header, scoped alarm links and complete mobile settings | 7 scenarios | `final-results.json` |
| Topology title/copy layout after correction | 8, both themes and widths | `replica-topology-final-results.json` |
| Review values remain uncovered after a failed save | 4, both themes and widths | `review-visibility-results.json` |
| Final runtime action permissions and resolved-unknown alarm copy | 28 focused cases | `permission-final-results.json` |

Loaded-content checks wait for the expected route body, query completion and fonts. Explicit unavailable-service scenarios expect their runtime error panel; unexpected error states fail the review. The initial unavailable-service assertion incorrectly rejected the intended panel and was corrected before those four states were rerun.

Full-resolution runtime, deployment, alarm and settings screenshots were inspected alongside contact sheets for the matrices. Element bounds and action behavior are measured separately; a passing geometry check alone is not visual approval.

## Findings

- The deployment runtime timestamp overlapped its inspection action. The legacy icon-only button dimensions were scoped correctly and the rendered row was rechecked.
- Browser Back retained an alarm cursor from another filter. Keying the inbox by its canonical search resets its page state; the 25-row pagination and Back scenario passed.
- Stale or undated observations still exposed unqualified ready replica counts. Lists, cards, service facts and topology now show unavailable current counts, and retain historical deployment results separately.
- The topology inspector squeezed a short service name into one letter per line beside the status and copy action. The title now keeps a readable minimum width and the copy action wraps. The final eight states passed bounds checks and screenshot inspection.
- The desktop alarm settings sticky footer covered the values in the review section. The alarm settings footer now uses normal document flow. All four final checks confirmed the values were visible and uncovered; failed drafts still remain available for correction.

## Final permission and copy check

After the layout review, the shared runtime notice gained explicit permissions for node and log inspection. Eighteen rendered cases checked admin, developer and viewer roles across application, service and latest-deployment notices, with unschedulable and CrashLoop diagnostics. Admins retain node inspection; developers and viewers do not see it. All three browser roles retain log inspection because their existing project roles include `logs:read`. Diagnostics and pod inspection stay visible. Two mobile viewer cases passed as well.

Four additional rendered shared-component cases verified that omitting the permission props hides node and log actions by default. An API credential is intentionally rejected at the dashboard's browser-session boundary, so it is not represented as a supported dashboard role.

Four alarm cases checked both themes at 320 and 1484 pixels. An incident resolved after its service was removed now explains that it was resolved without a healthy observation and keeps the resolution visible. Active incidents with unknown observations still say recovery has not been confirmed. All 28 cases passed; notice and alarm-row screenshots were inspected after loading.

The fixture was restarted only for this focused check, then stopped again with its browser contexts.

## Limits

This is an isolated UI review. Its artificial records exercise presentation, navigation, local form behavior and the API contract. Backend authorization, durable alarm evaluation, real cluster observations, SMTP delivery and release checks are verified separately by the implementation owners. The review does not claim real email receipt.

The isolated server on port 4193 and all reviewer browser contexts were stopped after the final checks.

A subsequent live browser check found missing alarm routes in the dashboard proxy, which the isolated fixture did not exercise. Those routes were added, the existing proxy behavior was independently reviewed, and two direct request regressions passed. The real authenticated inbox, email-unavailable settings and healthy shop page then loaded successfully. This transport correction did not change the signed-off layouts.

Source inspection confirms foreground-only 30-second alarm polling, 25-row inbox pages, a 40-page cursor bound, immediate disposal of inactive alarm query caches and abortable requests. The header requests one alarm only. Hidden-tab timing was not separately measured in the browser during this pass.
