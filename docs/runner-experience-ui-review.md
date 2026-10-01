# Runner setup and overview review

This review responds to the October 1 setup recordings and runner overview
screenshot. It covers the shared dashboard used by self-hosted Hakopod and Cloud.

## Design references

- [Hatch review checklist](ui-ux-checklist.md) and [Hakopod brand kit](../brand-kit/README.md).
- Apple HIG: [Layout](https://developer.apple.com/design/human-interface-guidelines/layout),
  [Disclosure controls](https://developer.apple.com/design/human-interface-guidelines/disclosure-controls),
  [Onboarding](https://developer.apple.com/design/human-interface-guidelines/onboarding),
  and [Accessibility](https://developer.apple.com/design/human-interface-guidelines/accessibility).

The applicable principles are consistent alignment, clear relative importance,
related choices grouped together, contextual guidance and progressive disclosure.
The implementation retains Hatch typography, theme tokens and controls.

## Problems found

- Setup distributed short choices across the full viewport. Back and Next sat at
  opposite edges, and edit mode repeated a provider choice that could not change.
- Resource values, routing labels and secondary settings competed for attention.
- Review displayed image digests and a machine diff before explaining the effect.
- Overview repeated status across the pool, Runtime and a full pod table. Equal
  Runtime and Resource usage panels created empty space and nested borders.
- Runtime values had an extra inset. Custom disclosure rows lacked a chevron.
- Peer settings used different heading and control structures, shifting their
  controls vertically. Fields with wrapped help could stretch their inner rows.

## Changes

Setup retains Connect, Compute, Workflow and Review. The current decision and a
compact live summary occupy separate desktop columns. Mobile exposes that summary
through a disclosure. Resource cards show reservation and limit values; optional
settings and workflow syntax stay available behind labeled disclosures. Back and
Next remain adjacent. Review explains the server plan, masks sensitive changes and
keeps the exact diff available on demand. Drafts, validation and the deployment
review remain in the existing flow.

Concurrent jobs and node placement share heading, control and help tracks.
Advanced connection settings and custom resource fields use the same alignment
principle. Stepper validation messages occupy a full row below its controls.
The standing UI checklist now requires measured vertical alignment of peer
settings, including wrapped help and validation states.

Overview separates observed job activity from configured slots. Configure appears
once and uses the loaded application's project permissions. Restart remains in
the service menu with its existing confirmation. Pool configuration and sampled
resource usage share an aligned grid without nested outer panels. Pod inspection,
image details, registrations and alarms remain accessible. Partial resource
overrides retain their known values instead of implying complete defaults.

The shared overview grid also affects ordinary services and build configuration.
Cloud consumes these components through its engine dependency; it does not carry
a second runner implementation.

## Verification

The development harness renders the actual shared components with labeled
artificial API data and blocks external requests. It does not change provider
accounts or live runner pools. Cloud edition rendering uses the shared edition
flag; the composed Cloud dashboard was separately built and typechecked.

| Coverage | Result |
| --- | --- |
| GitHub setup: Connect, Compute, Workflow and Review | 64 captures across both themes, both edition flags, and 1920, 1440, 390 and 320 pixel widths |
| Runner and ordinary service overview | 32 captures with complete metrics fixtures, visible-control bounds and active-tab visibility |
| Failed plan, edit with partial resource overrides, shared Input focus | Both themes and edition flags at 390 pixels |
| Build configuration and generic Logs | Both themes and edition flags at 1440 and 320 pixels |
| Shared Input, Textarea and OTP keyboard focus | Natural Tab order in both themes and edition flags at 390 pixels; 12 screenshots |
| Peer-setting alignment, advanced settings, custom resources and invalid job counts | 64 additional captures; matching desktop heading/control edges and helper starts within one CSS pixel, both themes and editions |

The interaction checks verify that a rejected plan preserves the pool name,
workspace and selected node through Back navigation. Choosing another resource
preset does not alter the saved card's original partial override or profile
values. Node selection supports touch and keyboard, and Escape restores focus.
Workflow and lifetime disclosures support keyboard and touch; raw-change
disclosures open from the keyboard. Generic Logs retain wrapping by default.
Self-hosted shared controls retain a visible
2px focus outline without decorative hatching; Cloud retains its existing focus
treatment.

Bounds checks include visible disclosure summaries and radio-card hit areas,
exclude closed disclosure contents and visually hidden inputs, and check card
overlap. The review found and fixed overlapping chips at 320 pixels, an oversized
desktop workflow disclosure, an orphaned mobile help control and mismatched
vertical placement of peer settings. Capacity values of zero and above the pool
limit were also checked at 1440 and 320 pixels. Incomplete
metrics fixtures and an overlapping fixture-only header were corrected before
the final overview review.

Validation ran on the development VM with bounded resources:

- `go test -p 1 -timeout 30m ./...` passed with `GOMAXPROCS=1`.
- Public dashboard build, typecheck and all 169 tests passed on the final source.
- Composed Cloud dashboard build and subsequent typecheck passed on that source.
- Cloud shared Python tests passed: 101 tests.
- Prettier checked the changed source files successfully.

The final source hashes were compared between the local worktree and VM export.
Independent screenshot review approved the corrected alignment: all 48 desktop
peer comparisons had zero Y-axis difference, and all 16 invalid-count states
kept the controls aligned with validation messages below them.
Evidence is retained in the ignored `work/ui-review/runner-hig-20261001/`
directory, with the final additional review results in `evidence-final/`.

The earlier Actions captures cover normal and error rendering, but the extended
pagination script stopped on a stale selector. That scenario was not part of
this change and is not claimed as newly verified. The full Cloud shell,
authentication and entitlements, provider-specific GitLab and Bitbucket setup,
physical touch devices, and live provider operations were not browser-verified
in this pass.

This is a dashboard change. It does not enable a CI provider, change Kubernetes
or runner execution behavior, or establish production deployment acceptance.
