# Runner scale-to-zero UI review

## Review: 2026-11-02

Targeted review of the scale-to-zero controls in `ManagedActionsForm` (Capacity
and Review steps), the pool summary and the `ManagedActionsStatus` pool card. An
independent reviewer drove the synthetic fixture in `web/review/runner-scaling`
(all API responses are fixtures; a banner marks the page). No installation,
GitHub account or runner pool was used.

### Coverage

- Capacity step from a fixed pool and from a saved scale-to-zero pool; light at
  1440, dark at 390 and 320. Review step in dark at 1440. Sleeping pool card in
  light at 1440 and dark at 390.
- No document overflow at 1440, 390 or 320. Choice buttons 44px high, help
  buttons 32 by 44px, number fields 48px.
- Keyboard: Tab order through the choice, help and fields; arrow keys change the
  choice; visible focus; help opens on focus or activation and closes on Escape.
- Invalid "Runners kept online" (9 of 4 slots) shows an error on submit.

### Findings and resolution

1. The pool card showed "Sleeping" for a pool that should keep runners online
   but had none. Fixed: a pool is shown as sleeping only when it keeps no
   runners online; otherwise "No runners registered" remains.
2. Idle and kept-online inputs stretched to the full column. Fixed: both use
   the same stepper as Concurrent jobs. On desktop the scaling row spans both
   columns, with Idle minutes under Concurrent jobs and Runners kept online
   under Run on node; below 640px they stack.
3. An invalid value showed the field error and a second hint. Fixed: the hint is
   hidden while the field is invalid.
4. The summary repeated invalid values and "Scales to 1" was unclear. Fixed:
   the summary line appears only for valid values and reads "Scales down to N
   runners" or "Stops all runners".
5. The help popover for Idle minutes covers the choice above it at 390px.
   Accepted: shared help placement, dismissible with Escape.
6. "Sleeping until a job is queued" pushed the check time onto its own line.
   Fixed: the label is now "Sleeping".

Following the user's layout feedback, step actions moved under the sticky pool
summary on desktop; small screens keep them at the end of the form. The review
step also states that the pool wakes on GitHub Workflow jobs webhook events, and
the plan label for `idle_seconds` now names its unit. The step and review
containers no longer draw an outline when focus moves to them after a step
change; focus still moves there for assistive technology, and every control
keeps its own visible focus.

### After fixes

The author rechecked the Capacity step (dark, 1440) with the invalid value,
step actions under the summary advancing through Cache to Review, and the review
step at 390 with no overflow. The reviewer did not repeat the full matrix after
these fixes.

### Not covered

Status card at 320; review step in light and at 320; Idle minutes values 0 and
1441; a physical touch device; screen-reader output beyond DOM attributes.
`.github/workflows/runner-scaling-ui-review.yml` captures the full matrix
(both themes at 1440, 390 and 320) as an artifact on pull requests.
