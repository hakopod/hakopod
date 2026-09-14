# Probe Setup UI review — 2026-09-14

Independent review of `InstallationSetup` and `/infrastructure?tab=setup` for prebuilt probe image instructions. Production source was not edited by the reviewer. Artificial local fixture only; live accounts, workloads, and the user's Chrome were untouched. Fixture process stopped after review.

## Result

Approved within the scoped documentation change after two fixes from the implementation agent:

1. The new inline release-assets link was indistinguishable from surrounding prose. Plain Tailwind underline was overridden by unlayered Hatch CSS; `underline!` now visibly identifies it in both themes.
2. The systemd code panel's unlayered `overflow:hidden` overrode `overflow-auto`, clipping mobile commands. `overflow-auto!`, `tabIndex=0`, and a descriptive accessible label now enable keyboard scrolling. ArrowRight moved scrollLeft from 0 to 40px in both themes.

## Completed coverage

- Source: AGENTS.md, Hatch AGENTS.md, standing checklist, InstallationSetup, infrastructure route, CSS cascade relevant to links/code panel.
- Rendered and visually inspected all four full-page screenshots: dark and Paper at 1440px and 390px, loaded listener readiness text and fonts ready, no console errors or loading placeholders.
- Main/document bounds equal viewport width. Code/content inset 24px desktop, 16px mobile; no duplicate insets or document overflow. Main heading and tab dividers reach viewport edges. Selected Setup tab remains visible and uses red text without selected background/underline.
- Four documentation links focusable, visible Hatch corner focus markers. Release-assets Enter and touch tap each opened the expected GitHub releases URL in both themes; external documents intercepted locally, no actual external browser session accessed.
- Systemd configuration block remains within viewport; horizontal keyboard scrolling verified on mobile. Instruction prose, installed-helper fact, older-release limitation and redeploy instruction remain visible.
- Uses existing theme tokens, no new forms/dependencies/mutations/polling. Existing owner gate and query controls unchanged.

## Limits

This is UI verification using explicitly marked artificial records, not a fresh-install, registry-pull, release-publication, or server configuration test. Cloud/denied/error/loading branches and unrelated Infrastructure tabs were source-inspected where relevant but not newly rendered. Native Safari/Firefox and physical-device touch scrolling were not tested. Fixture covered unconfigured helper; actual live digest value was not fetched.

Evidence: `results.json`, `interactions.json`, `scroll.json`, `screenshots/setup-{dark,light}-{1440,390}.png`. The full local review retains the standing checklist from [the Hatch UI review guide](ui-ux-checklist.md). Evidence lives under `work/ui-migration/probe-setup-review/`.

