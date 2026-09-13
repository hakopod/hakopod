# Dashboard feedback checklist

This checklist tracks the five follow-up comments from 13 September 2026.
An item is complete only after its current source and rendered behavior are checked.

- [x] Remove the outer application-list box, divider borders and heavy card outlines.
- [x] Replace the large application summary tiles with a compact, responsive row.
- [x] Match the From source and New application button heights and alignment.
- [x] Use at most four service cards on desktop and five on wide screens, with fewer columns on smaller devices.
- [x] Make each service's variables and secret bindings directly visible and editable from its own page, including secure secret-value management.
- [x] Verify desktop/mobile layouts, keyboard access, permission handling and retained drafts without changing live workloads.
- [x] Run required checks and refresh the preview.

The previous round is recorded in [dashboard verification](../web/VERIFICATION.md),
including virtual networks, project environments, template credentials and shared
selection controls. New findings and verification evidence will be recorded here.

The layout passed 16 isolated browser checks without browser errors. Both grids
use 1, 1, 2, 4, 5 and 5 columns at widths of 320, 390, 768, 1447, 1800 and 2560
pixels. The summary is 28 pixels high on desktop and wraps to 46 pixels on small
screens. Both heading actions have matching heights and vertical alignment.
Lists with two records retain the same card widths as fuller lists. Native card
links, copy controls, menus and keyboard focus work independently. The fixture
used artificial records and made no live configuration changes.

Fourteen service browser checks passed. They covered direct tab URLs, selected
service values and bindings, shared-secret warnings, name validation, viewers,
failed drafts, in-flight dialog protection, reviewed TOML changes and 390-pixel
layouts. Secret values stay write-only; saving a new value does not automatically
bind it or redeploy a service.

The Go suite, dashboard TypeScript, formatting, all 43 frontend tests and the
production build passed. Read-only preview checks confirmed the existing setup
and both application revisions were preserved. No dependencies or polling were
added; the Secrets panel loads on demand.

Publication checks are available in [GitHub CI](https://github.com/hakopod/hakopod/actions/workflows/ci.yml).
