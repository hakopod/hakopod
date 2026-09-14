# OAuth auth UI review — 15 September 2026

**Result: approved for the scoped visual change.** Both original review findings
are resolved in the final red-button revision.

## Scope and evidence

Independent review covers the shared OAuth provider controls in
`web/src/components/auth-screen.tsx`, provider styling in
`web/src/styles/account-access.css`, provider SVG assets and Cloud dark canvas
token in `web/src/styles/foundation.css`. It follows the applicable sections of
[the Hatch UI checklist](ui-ux-checklist.md). The composed Cloud fixture at
`http://127.0.0.1:4176/` is explicitly synthetic with submissions disabled.
No authentication, account or production mutation was performed by this review.

The reviewer browser connection was unavailable. The implementer captured real
in-app browser screenshots and DOM bounds; this independent reviewer inspected
the resulting files directly. Evidence is in `/tmp/hakopod-oauth-review-shots/`:
32 `{route}-{desktop,mobile}-{dark,light}.png` cases, eight initial ordinary
`viewport-{login,signup}-{desktop,mobile}-{dark,light}.png` captures,
`viewport-focus-github.png` and `bounds.json`. Final evidence superseding the
initial purple-button revision consists of all 12
`red-{login,signup,invite}-{desktop,mobile}-{dark,light}.png` viewport captures,
`red-focus.png` and `red-evidence.json`. Every final image was inspected
independently; mobile invitation viewport captures show the introductory
portion, while its below-fold provider color and loaded images are verified by
DOM evidence and its layout by the earlier full-page review. Review contact sheets crop only
empty capture canvas from the original images. Full-page screenshots had a
browser capture artifact (half-scale content with blank remaining canvas);
ordinary viewport captures resolve that ambiguity for login/signup.

## Verified visual coverage

- [x] Login, signup, password recovery, password reset, verification, MFA,
      first-admin setup and invitation: desktop 1280px and mobile 390px,
      both Paper and dark. All 32 cases were independently inspected.
- [x] Login/signup additionally inspected through eight normal viewport
      screenshots. Expected form headings are present; no loading/error
      placeholder substitutes for the real page.
- [x] Google is the full-width first OAuth row with the final user-requested
      red accent and black Google mark. The shared theme token produces
      `#e8705f` in dark mode and `#b8422f` in Paper; theme-appropriate label
      colors remain readable. GitHub and GitLab use equal halves beneath it. Labels fit and align at both sizes. No Microsoft option.
- [x] DOM measurements: all provider controls are 40px high. Desktop Google
      is 410px wide with 199px secondary buttons; mobile Google is 316px wide
      with 152px secondary buttons. Horizontal/vertical grid gaps are 12px.
- [x] Global Cloud dark canvas is `rgb(15, 0, 4)` across all eight route states;
      Paper is `rgb(244, 245, 238)`. Card/form surfaces remain distinct and
      readable. Document widths are within the tested viewports.
- [x] Story copy changes with route. Mobile stacks a compact story above the
      form and the page scrolls for longer forms. This is existing behavior,
      not a hidden-story implementation.
- [x] Source: provider links consume shared Button `asChild`, have accessible
      `Continue with …` labels, and are filtered by enabled providers. Google
      and GitLab images have explicit 20px bounds, preserved aspect ratio,
      decorative alt text, and no shrinking. GitHub uses ServiceIcon size 20.
- [x] Parent observed Tab from Google move focus to GitHub. Final
      `red-focus.png` visibly confirms a clear outline, separated from the
      control by 3px. Computed dark outline is 2px solid
      `rgb(244, 245, 238)`; source uses the theme foreground token. This resolves
      the earlier faint bracket-only focus.
- [ ] Touch and reduced-motion emulation were not available and were not tested.

## Resolved findings and final assessment

1. **Keyboard focus visibility resolved:** provider links now have a clear
   theme-aware 2px focus outline at 3px offset. The final GitHub focus capture
   is visibly distinct from adjacent unfocused GitLab, without overlap.
2. **Mobile dark login icon readiness resolved:** final
   `red-login-mobile-dark.png` shows both GitHub and GitLab logos. All three
   provider images in every final scenario are `complete=true` with positive
   natural widths (Google 118, GitHub 150, GitLab 494). The initial missing
   logos were a screenshot readiness problem; no missing-image defect remains.
3. **Latest color request verified:** the final Google mark is black through
   `brightness(0)` in every DOM result and final visible provider screenshot;
   GitLab retains its original colors and GitHub adapts monochrome to theme.
   The earlier purple style is superseded and is not the approved revision.

The final provider layout, icon readiness, requested colors, Cloud canvas and
keyboard-focus change pass the scoped independent visual review. The earlier
32-case route matrix still covers the unchanged surrounding auth layout.

No other actionable geometry or color defect was found. This document does not
claim full OAuth success, a runtime no-JavaScript test, a provider brand-policy
review or a retest of logged-in dashboard routes. Review source edits were
limited to this record; screenshots/contact sheets are local evidence only.
