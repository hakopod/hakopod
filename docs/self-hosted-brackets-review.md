# Self-hosted bracket removal review

Independent source review on 2026-09-15 against the shared Hatch checklist and
the user's edition-specific requirement. No rendered review was performed.

## Coverage

Reviewed the SSR root edition marker, foundation styles, shared shell and auth
styles, card/list/topology/command-palette selection styles, and Hatch bracket,
input, OTP, select, menu and dialog implementations.

- The SSR `<html>` element declares `data-edition` before hydration.
- Decorative corner components consistently use `.brackets`; the self-hosted
  selector hides those nodes throughout the document, including body-portaled
  dialogs, sheets, menus and selects.
- The alternative CSS gradient focus corners now exclude self-hosted pages.
- Self-hosted native controls and focusable elements receive a simple red
  keyboard-focus outline. Active OTP cells have an explicit outline even though
  the real input is visually overlaid by the OTP component.
- Command-palette selection, selected build cards, topology selection, theme
  choices and highlighted menu/select options retain other visible states after
  their brackets disappear.
- Cloud retains the existing bracket nodes and focus-corner styling. The new
  hiding/outline selectors do not match its edition marker.

## Resolved finding

The generic self-hosted focus outline initially overrode the catalog card link's
`outline: 0`, while its pseudo-element also outlined the entire card. The follow-up
now explicitly suppresses the native `.ops-card-link:focus-visible` outline in
self-hosted mode and sets its `::after` outline color to `--navigation-active`.
Source inspection confirms one red outline around the stretched card target.

No actionable source findings remain in this bounded review. The implementation
owner separately reports an HTTP/SSR check confirming the edition marker appears
before hydration and the served stylesheet contains the scoped rules; this is
not rendered interaction evidence.

## Limits

Browser access remains unavailable. Screenshots, theme contrast, actual focus
geometry/clipping, keyboard traversal, OTP entry and portal interactions are
unverified. Earlier user approval of Cloud auth previews does not constitute
rendered approval of this self-hosted change. This review makes no build, test or
deployment claims.
