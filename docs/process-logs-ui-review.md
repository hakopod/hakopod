# Current-process API logs UI review — 2026-09-14

Independent scoped review using AGENTS.md and docs/ui-ux-checklist.md. The standing checklist is retained in `work/ui-migration/probe-setup-review/REVIEW.md`; the pass below covers the logs change and shared focus fix. No production implementation changes were made by the reviewer.

Approved after implementation agent resolved three findings:

- Empty current-process logs previously described absent journal entries. Source-neutral empty state now reads “No API log entries available.”
- Unlayered code-panel CSS overrode overflow-auto. overflow-auto! now allows vertical keyboard scrolling through bounded log output.
- Focusable pre blocks had no visible keyboard focus. Shared code-panel[tabindex]:focus-visible now has a 2px inset theme-token outline; Logs and Setup both verified.

## Completed checks

- Rendered process and journal sources at 1440px and 390px, dark and Paper (8 screenshots), then inspected every screenshot. Correct content and fonts loaded with zero page errors; synthetic records explicitly marked as fixtures.
- Process source explicitly identifies the current process, missing journal history, buffer start time and reset on API restart. Journal source retains journal copy and does not show process-reset text.
- Real bounds: 24px desktop / 16px mobile content inset; document width equals viewport. Log pre height capped at 512px including border; long entries wrap inside it with no horizontal document overflow.
- ArrowDown moved scrollTop by 40px in every source/theme/width combination. Computed overflow is auto and whitespace is pre-wrap. Logs area is named and focusable.
- Pause button changes to Resume and shows refresh-paused text. Empty process and journal arrays render the neutral empty state.
- Additional four empty-state screenshots verify real Tab focus on log pre at desktop/mobile in both themes: :focus-visible true and 2px solid state-token outline. Mobile dark and Paper screenshots inspected closely. Shared Setup pre checked in both themes with screenshots: visible focus outline and ArrowRight moves scrollLeft 40px.
- Actions align in compact row; active API logs tab remains visible and red with no selected underline/background. No decorative page heading icons or extra page insets introduced.

## Evidence and limits

Local evidence under `work/ui-migration/probe-setup-review/`: logs-results.json, logs-focus.json, logs-empty.json, scroll.json, screenshots/logs-*.png, screenshots/setup-focus-*.png. The earlier logs-empty.json includes one captured pre-fix process focus result; final logs-focus.json is the passing replacement for that focus check.

Fixture only: live authenticated preview, journal access, actual process-log capture/redaction, backend permission behavior, polling lifecycle and native Safari/Firefox are not newly verified by this UI review. Those backend checks belong to the implementation agent. Cloud/denied/failure paths were source-inspected only; no production accounts or workloads changed. Test browser contexts and fixture server stopped after review.
