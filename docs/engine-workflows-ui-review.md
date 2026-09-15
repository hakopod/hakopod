# Engine workflows UI review

Independent source and rendered review of the shared dashboard additions for automatic framework setup, build-secret references, scheduled jobs, preview environments and release recovery. Coding-agent integration has no new dashboard page and is outside this visual review.

## Scope and evidence

The review follows `AGENTS.md`, `docs/ui-ux-checklist.md`, `packages/ui/AGENTS.md`, the Hatch README and public Button/Card/Tooltip/token sources. It covers real production components and route modules in an explicitly labeled synthetic fixture at `http://127.0.0.1:4203/`. The fixture uses the production self-hosted theme and appearance helpers, a small synthetic header, intercepted API records, no credentials, blocked external requests and deliberately rejected mutations.

Browser inventory and direct in-app browser creation were unavailable. Native Chrome review stopped when the user resumed browsing. The user then explicitly authorized isolated Playwright. All subsequent screenshots and interaction checks used a new temporary headless Chrome profile, never the user's profile. Cases ran sequentially; contexts and browser processes were closed after use.

Local evidence is ignored under `work/engine-workflows-review/`: `main.tsx`, `review.mjs`, `results.json`, `supplement.mjs`, `supplement-results.json`, `final-controls.mjs`, `controls-results.json`, `editor-final.mjs`, `editor-final-results.json` and `screenshots/`.

## Coverage

| Route family | Screens and states | Rendered coverage |
| --- | --- | --- |
| Source builds | Create, edit, list, detail Configuration; static and Node recipes; detection suggestions, loading, failure, stale response, approved scoped source and denied source | Dark and Paper themes at 1440 and 390 pixels |
| Scheduled jobs | Service detail and application actions; observed runs, empty history, stale history, paused and failed service; Pause/Resume dialogs and failures | Both themes and widths |
| Preview environments | Application Previews tab, long branch names, active/deleting/deleted cards, empty/denied lists, owner controls, nested creation, revision conflict, retained draft, deletion confirmation and failure | Both themes and widths |
| Release recovery | Configuration selector, inline and expanded TOML editors, succeeded/running/failed/skipped recovery banners | Both themes and widths; expanded editor also at 844 × 390 |
| Shared controls | SelectField, heading help, keyboard focus, Enter/Space/Escape, explicit touch taps, textarea bounds and sticky footer overlap | Both themes and widths |

The baseline matrix has 25 cases × 4 theme/viewport combinations. Ninety-nine passed in the original run; the sole page-navigation timeout passed on targeted retry. Twenty-eight additional successful cases cover the latest layout fixes, owner permission controls, linked source detection, visible loading, deletion, editor dimensions and touch interaction. Four further sequences verify keyboard help and expanded-editor draft retention. Two earlier help-test timeouts were test assumptions: focus already opens the tooltip, so pressing Enter immediately toggles it closed. Corrected tests exercise focus, Escape, Enter and Space in sequence.

Screenshots were taken after expected controls, query completion where applicable, and font loading. Contact sheets cover the matrix; representative full screenshots were inspected for legibility, hierarchy, warning placement, wrapping, alignment, focus and dialog bounds. Superseded screenshots and `-FAILED` files are historical debugging evidence, not final approval evidence. Full-page captures of fixed dialogs/footers may show content below the original viewport outside the overlay; actual viewport bounds and focused-field hit tests were checked separately.

## Completed checklist for affected UI

- [x] Shared content insets measure 24px on desktop and 16px below 640px; headings, preview cards and form sections align without nested catalog padding.
- [x] Page heading and application/service tab dividers reach the viewport edges. Selected tabs use the existing red text treatment without selected background or underline.
- [x] Layout additions use Tailwind utilities or shared `@apply` rules. Undefined layout classes were replaced.
- [x] Necessary headings/actions remain compact. Explanatory help is accessible by keyboard and touch; operational warnings and field instructions remain visible.
- [x] Page titles, nested routes, validated Previews search state and expected loaded content were checked. The denied direct preview route presents its explicit access state.
- [x] Preview grids retain sparse card widths and responsive column limits. Long branch/command/reference values wrap inside their component.
- [x] Shared Button/SelectField controls preserve theme styling and labels. Destructive actions use the named trash icon and distinguish destructive styling from ordinary actions.
- [x] No document-level horizontal overflow or clipped mutation controls in reviewed states. Existing tables and tab rows scroll within their own container.
- [x] Large forms use nested pages. Labels/help are associated with fields. Inline code editors provide a usable minimum height and preserve resize; the expanded editor fits its available space.
- [x] Detection stages explicit suggestions, rejects stale source responses, disables save while pending and keeps errors beside the initiating control.
- [x] Static port 8080 is consistent; editing unrelated Node recipe fields preserves a custom port through submission.
- [x] Secret fields contain CI secret references, with visible instructions and an accessible description; failed saves retain them.
- [x] Preview creation requires review, handles parent revision conflicts explicitly and retains entered name/reference/TOML after refresh. Delete confirmation remains after failure.
- [x] Permission states cover administrator, Cloud owner capability and denied viewer UI. Linked non-admin build requests include the bound application ID; denied source responses preserve input.
- [x] Schedule actions use Pause/Resume terminology, explain active/missed run behavior and disable unsafe repeat submission after a revision conflict.
- [x] Observed schedule history is distinct from empty/stale history. Recovery outcome is distinct from the failed deployment and last observed runtime health.
- [x] Source review confirms bounded preview pagination/polling and duplicate-submit guards. These are source findings, not load-test results.
- [x] Self-hosted form/dialog focus is visible without decorative corner brackets. Both themes and mobile touch behavior were inspected.

Unchanged global navigation, authentication, production account permissions and project scope selection were not re-certified by the synthetic header. This review establishes the affected route bodies and controls, not a new global-shell audit.

## Findings resolved

1. Detection previously overwrote settings immediately. Results now require explicit Apply, bind to a source fingerprint, reject stale responses and block concurrent Save. Errors are adjacent and announced.
2. Static and service ports could disagree; unrelated Node edits reset custom ports. Static recipes now use 8080 consistently and Node ports survive edits/submission.
3. Default connection IDs and linked scoped builds were incorrectly excluded from detection. The UI accepts the default connection and binds requests to `application.id` or `build.application_id`; the engine remains responsible for authorization.
4. Build-secret instructions were unassociated, and detail pages omitted commands/output and secret-reference summaries. Labels/help and saved configuration inspection are now present.
5. Undefined paired-field layout classes and unbroken command/reference text caused layout risk. Responsive Tailwind grids and wrapping address both.
6. Schedule action labels, stale/empty history and observation timestamps were clarified.
7. Preview access controls, parent revision refresh, duplicate-submit guards, idempotency, tab validation and dashboard proxy forwarding were added or corrected. Proxy regression coverage is reported by the implementing parent separately.
8. Preview card headers used an undefined toolbar and duplicate catalog inset; narrow definition labels squeezed branch names. The shared toolbar, direct responsive grid and stacked fields now align and wrap correctly.
9. Shared textarea specificity kept the preview TOML editor at 112px. Inline code editors now measure at least 256px in both themes and widths.
10. The new minimum height overrode fullscreen-editor sizing on a short landscape screen, overlapping its footer. A scoped expanded-editor override restores zero minimum height and disables manual resize; final landscape verification is recorded below.

Recovery defaults were checked against existing engine behavior: `safe` retains existing automatic restoration for compatible stateless releases while skipping jobs, persistent storage and service additions/removals. The earlier suggestion to make it opt-in was not an established user requirement. Visible copy makes clear that external data and secret values are not restored.

## Decision and limits

Approved for the affected shared dashboard UI. No unresolved actionable findings remain in this review scope.

The final six expanded-editor checks passed in both themes at 1440 × 1000, 390 × 1000 and 844 × 390. The landscape editor measures 133px high and ends 20px above the footer; the footer stays inside the viewport. Desktop and portrait editors retain their available height. Escape preserves the edited TOML. Final screenshots were inspected after the scoped fix.

In total, the evidence contains 100 passing baseline combinations including the targeted navigation retry, 28 additional successful state/interaction checks, four corrected keyboard/expanded-editor sequences and six final editor regression checks. Isolated review browser processes and the fixture server were stopped after verification.

These fixtures establish rendering, request shape, UI access gating and failure handling only. They do not prove framework runtime compatibility, real provider secret setup, Kubernetes scheduling, live preview cleanup, actual release restoration, Cloud gateway authorization or a published deployment. Backend tests, real Docker/framework builds and cluster acceptance belong to the implementing parent's validation record.
