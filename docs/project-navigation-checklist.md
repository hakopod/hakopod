# Project navigation review

This pass applies the shared [Hatch UI review gate](ui-ux-checklist.md) to Projects home, project-scoped applications and the global interaction changes. The independent review is complete for the coverage below.

## Navigation and scope

- [x] `/` opens Projects even when a project/environment is saved in session storage. It lists authorized project records without fetching applications for every project.
- [x] Project cards are native links to `/projects/$project`; a valid environment is represented in the URL. Keyboard focus and middle-click work.
- [x] The header says Projects on home and Applications inside a project. Project, environment, application, nested form and deployment back links keep the right context.
- [x] Deep links and reloads resolve the requested project/environment. A missing environment uses a real returned environment; an explicit invalid or unavailable scope shows a useful state without an applications query or silent fallback.
- [x] Scope changes reset pagination and page-local filters. Delayed responses and caches do not flash applications from the previous project/environment.
- [x] Admin, developer/member, viewer and personal workspace access show only permitted actions. Projects with no environments, inaccessible projects and API failures have clear states.
- [x] Creation/cancel and command palette links open the project applications list with its environment. Guidance links were inspected in source; the guidance dialog was not exercised in this pass.

## Layout and interaction

- [x] Page and section headings use equal vertical padding, remain compact and align actions in both themes and at narrow/wide widths.
- [x] The Resource metrics card is clickable as a whole through a native link, with visible keyboard focus and native middle-click behavior.
- [x] Deploy changes uses the shared primary accent, including a user-selected accent. Removing an environment variable uses a trash icon with an accessible name.
- [x] Active tabs have accent text, icons and underline with no background fill. Check hover, focus and custom accent globally across affected tab families.
- [x] Pages remain edge-to-edge with no document overflow or clipped controls. Long identifiers wrap or truncate deliberately; a long build status no longer squeezes the title.
- [x] Wait for expected loaded body/control, no unintended skeleton/error boundary and loaded fonts before measuring or taking screenshots. Inspect the screenshots before sign-off.
- [x] No live accounts or workloads are changed. Fixture requests, buffers and processes are bounded and stopped after review.

## Evidence

The fixture under `work/ui-migration/project-review/` used actual dashboard source and clearly labeled artificial data on loopback port 4193. It blocked all external browser requests and did not change live accounts, workloads or cluster state.

- `matrix-results.json`: 112 loaded layout cases, covering 14 route states in both themes at 320, 390, 1484 and 2560 pixels with a custom purple accent. Projects, project applications, application and service details, the service environment editor, deployment, build, infrastructure, backups, appearance, virtual-network creation, application deployments/configuration and Git providers were covered. Element bounds, heading padding, action heights and active tab colors passed, with no unexpected loading state, error boundary, missing fixture endpoint or page error. The corresponding screenshots and contact sheets were visually inspected.
- `results.json`: 13 navigation, role and failure scenarios passed. They cover saved-scope home, native project links, unauthorized scopes, missing/invalid environments, admin/developer/viewer/personal roles, filtering/pagination resets, reloads and delayed scope transitions. A MutationObserver recorded one intermediate DOM sample after the history URL changed; bounded animation-frame observations found no old-scope painted frame, and the held new scope rendered no old applications or wrong-scope query.
- `interaction-results.json`: four scenarios passed for 24-project pagination and three-environment summaries; application, service-editor and deployment return paths; Resource metrics keyboard/corner/middle-click behavior, custom primary accent and local trash removal; new-application Cancel and command palette scope retention.
- `targeted-results.json`: 12 final checks passed across both themes at 320 and 390 pixels after the visual fixes. They verify compact Projects heading/search rows, native touch navigation, readable build headings and visible active settings navigation on deep links and resizing without document scrolling. All 12 final screenshots were inspected.

The review found and resolved four issues: unnecessary stacked Projects rows on mobile; long status text squeezing build titles; an active settings section hidden outside the horizontal viewport; and the service environment editor’s Back link returning to Settings instead of Environment. These were rechecked after the fixes. Active tabs retain a transparent background in resting, hover and keyboard-focus states. Focus outlines are visible in keyboard screenshots; they are not a tab background fill.

The 112-case matrix is a focused pass over the affected route families, not a claim that every possible route state was rechecked. The earlier global review remains separate historical evidence. No backend authorization, cluster operation, credential change or real deployment was exercised here. The reviewer’s browsers and fixture server were stopped after verification.
