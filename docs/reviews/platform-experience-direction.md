# Platform experience direction

Audit date: October 1, 2026. This is a design brief for the platform audit, not a claim that the redesign is implemented. The evidence report records current rendered behavior separately.

## Product principle

Hakopod should help someone finish a deployment task without requiring them to understand its entire configuration model. Keep the underlying model precise; present its decisions in the order people need them.

The brand gives us restrained Ink/Paper surfaces, clear typography, modular geometry, and purposeful accents. Hatch supplies shared controls and tokens. HIG contributes hierarchy, consistency, disclosure, and feedback. Applying those principles means a coherent workspace, not adding decorative cards around every field.

## Interaction patterns

| User task | Appropriate pattern | Avoid |
| --- | --- | --- |
| Choose a source, runtime, or resource preset | Small selectable cards with one name, one useful fact, and a clear selected state | A wall of equally prominent forms or unexplained icons |
| Configure several services | Compact service list, focused editor, changed-state indicators, application review | Repeating the complete service editor down one long page |
| Change one existing setting | Direct entry to that service and setting group, with current value and impact | Restarting creation or asking the user to scan unrelated settings |
| Manage variables | Compact rows with Name, Value/reference, Scope/source, and actions; import in its own panel | Oversized cards per variable, repeated headings, separated secret context |
| Inspect logs | One step-owned stream, collapsed noise, wrapping, stable line alignment | A second duplicate log panel below the step UI |
| Review a consequential change | Changed values and operational impact, then one explicit action | Reprinting the whole configuration or hiding warnings in help |
| Configure an integration | Provider choice, connection details, scope, review; expose only the selected provider | All provider fields at once |
| Inspect an established resource | Compact facts, activity, and task actions | A permanent setup form as the resource's home |

Cards represent choices or resources. Chips represent finite choices, scope, or status. Use standard inputs for names and free text, shared SelectField for longer option sets, and disclosures for optional specialist settings. A short task may fit on one screen; a wizard is useful only when its steps correspond to real decisions.

## Proposed application flow

1. **Choose a source.** Repository, container image, or catalog. Compose and TOML imports remain easy to discover as expert entry paths. Selecting a path opens only that path's task.
2. **Define the service.** Name and source first, then runtime choice. Show inferred values as editable suggestions rather than demanding every value immediately.
3. **Choose compute and access.** Resource presets and node placement share aligned rows. Public/private access carries its concrete effect. Advanced resources, command overrides, and recovery policy are concise optional sections.
4. **Add variables if needed.** Reuse one environment editor with import, secret handling, and explicit scope. Make skipping this optional step clear.
5. **Review the application.** A service summary shows additions, edits, removals, resources, exposure, and warnings. Users can add another service or return to one service without losing any draft.

This is an interaction proposal; the final number of screens should follow the selected source and workload. Container-only creation should not require repository questions. Existing configuration uses direct task entry instead of replaying onboarding.

## Proposed service workspace

Keep the service selector and compact summary visible while one task is open. Use consistent groups: Image/source, Compute, Network, Variables, and optional advanced runtime settings. Direct links from service detail open the matching group. At application scope, a compact list of services shows name, runtime, resource preset, access, and an unsaved-change indicator. Switching services preserves the full draft.

The current single-service editor already filters other services and preserves accepted configuration. Retain that behavior. The desired improvement is focus and navigation within the selected service, not a new mutation model.

## Proposed environment workspace

- Keep name, value or secret reference, and scope/source together. Label inherited and overridden values distinctly.
- Provide Add variable, Add secret, and Import. File import and paste share an import panel with a preview of additions, duplicates, and values promoted to secrets.
- Use a compact desktop grid and readable stacked rows on mobile. Do not make each variable a large card. Long values remain contained; multiline values expand on demand.
- Existing secret values remain write-only. New secret drafts are masked by default and never appear in review diffs, URLs, analytics, logs, or persistent browser storage.
- Display which services an application default affects. Review replacements and removals before deployment. Preserve drafts on validation, request failure, and conflicts.
- Retain backend limits, duplicate handling, empty-string overrides, imported-secret promotion, and revision conflict handling.

The existing editor already implements row grids and .env import. The proposal consolidates fragmented tasks and repeated context; it does not assume those capabilities are absent.

## Layout contract

- One shared page inset: 24px on desktop, 16px below 640px. No nested page inset or arbitrary width cap.
- Compact heading and action row; page dividers extend to the viewport edge while content stays aligned.
- Peer sections share heading, control, and help tracks. Measure both top and bottom edges in normal, wrapped-help, disabled, and validation states.
- Reserve consistent heights for like controls. Long labels wrap without pushing only one sibling's control down.
- Keep active navigation theme-aware red, with visible keyboard focus. Use the configured accent for primary actions.
- Use visible labels and short contextual instructions. Put optional explanations in accessible help; keep errors, unavailable-state reasons, cost/impact, and necessary warnings visible.
- On mobile, keep the current task and next action easy to reach. Avoid multiple nested scroll areas. Touch targets need adequate hit areas and separation; visual icon size is not hit-area size.

## Release acceptance for later implementation

Every slice must include create, edit, validation, request failure, review, and permission states; both themes; desktop and 390/320px layouts; real control bounds; keyboard and touch; retained drafts; and normal-size screenshot review. Compare task completion and exposed decisions against the current baseline rather than using page height alone as a quality score.

Preserve Go authorization and /api/v1 behavior. UI fixtures prove presentation only. Lifecycle, deployment, secret persistence, and provider behavior still require the relevant real development-cluster acceptance.

## References

- [Hakopod contributor rules](../../../AGENTS.md)
- [Dashboard UI checklist](../../../docs/ui-ux-checklist.md)
- [Hakopod brand kit](../../../brand-kit/README.md)
- [Hatch package](../../../packages/ui/README.md)
- [Apple HIG: Layout](https://developer.apple.com/design/human-interface-guidelines/layout)
- [Apple HIG: Disclosure controls](https://developer.apple.com/design/human-interface-guidelines/disclosure-controls)
- [Apple HIG: Onboarding](https://developer.apple.com/design/human-interface-guidelines/onboarding)
- [Apple HIG: Accessibility](https://developer.apple.com/design/human-interface-guidelines/accessibility)
