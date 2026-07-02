# Hakopod - logo and marketing identity kit

**Recommended direction: 01 / Open Port.** Eight modular blocks surround an open center, developing the rhythm of the supplied reference into a consistent identity for a self-hosted application platform.

Open **index.html** for the local asset gallery and background switcher. Open **guide/hakopod-brand-guide.pdf** for the six-page visual guide.

## Start here

| Use | File |
| --- | --- |
| Standard horizontal logo | `logos/lockups/hakopod-horizontal-ink.svg` |
| Horizontal logo on dark surfaces | `logos/lockups/hakopod-horizontal-paper.svg` |
| Bright mark on dark surfaces | `logos/marks/hakopod-mark-ion.svg` |
| Standalone primary mark | `logos/marks/hakopod-mark-ink.svg` |
| Inline symbol inheriting a UI color | `logos/marks/hakopod-mark-currentcolor.svg` |
| 16px optical mark | `logos/marks/hakopod-micro.svg` |
| Browser favicon | `icons/favicon.svg` or `icons/favicon.ico` |
| App and touch icons | `icons/icon-180.png`, `icon-192.png`, `icon-512.png` |
| Social sharing card | `marketing/open-graph-1200x630.png` |
| Brand colors and type tokens | `brand/tokens.css`, `brand/tokens.json` |

The `black` and `white` logo variants are true #000000 and #FFFFFF. The `ink` and `paper` variants use the warmer brand palette. All logo SVGs have transparent backgrounds. Application icons and marketing layouts include their intended backgrounds.

Transparent PNG exports are included for the standalone mark at 512px and the horizontal lockup at 1200px wide, in all five fixed colors.

## Six logo directions

1. **Open Port** - closest to the supplied reference; an open core surrounded by modular pods. The complete identity uses this direction.
2. **Bridge** - a stronger architectural H, with two sides working together.
3. **Shell** - brackets and a central stack, taking cues from a developer's command line.
4. **Signal** - staggered vertical modules for a more asymmetric rhythm.
5. **Container** - an H in negative space inside a compact, rounded pod.
6. **Vector** - a diagonal interpretation of the lead geometry.

Alternate concepts live in `concepts/`. They are exploration choices, rather than six interchangeable marks for one release.

## Color

| Color | Hex | Role |
| --- | --- | --- |
| Ink | #111A17 | Primary text, dark surfaces, logo |
| Paper | #F4F5EE | Main light background, reversed logo |
| Ion | #D8FF45 | Highlights, campaign surfaces, symbols on Ink |
| Forest | #2F5641 | Accent text on Paper, secondary surfaces |
| Fog | #B8C4B9 | Supporting graphics and text on Ink |

Checked sRGB contrast: Paper/Ink **16.17:1**, Ink/Ion **15.46:1**, Forest/Paper **7.57:1**. Body copy on Paper can use the supporting muted token #607065 (**4.78:1**). Avoid Ion text on Paper.

## Typography

Space Grotesk: Regular 400 for body copy, Medium 500 for headlines, Semibold 600 for identity and labels. Prefer sentence case and clear, concrete language.

The horizontal, stacked, and standalone wordmarks use **outlined letters with fixed spacing**. Use these files instead of retyping the logo. Marketing SVG lettering is outlined too, so the compositions stay consistent across design tools and exports.

The original variable font is included in `fonts/`, with its SIL Open Font License. Source: [Google Fonts / Space Grotesk](https://github.com/google/fonts/tree/main/ofl/spacegrotesk). Copyright 2020 The Space Grotesk Project Authors. Font licensing applies to the font software; retain the included license when redistributing it.

## Usage

- Keep at least one module width of clear space around the visible mark. One module is 12 units in the 96-unit master viewBox.
- Use the standard symbol at **24px or larger**. Use the pixel-grid micro mark at **16px**.
- Keep horizontal lockups at **140px wide or larger**.
- Scale proportionally. Keep the eight modules together and preserve the negative space.
- Use one solid color. Avoid outlines, gradients, shadows, and recoloring individual modules.
- Use the square app icon as supplied; platform masks can apply their own rounded or circular crop.

## Marketing assets

- Open Graph card: **1200 x 630**, SVG + PNG.
- Square social post: **1080 x 1080**, SVG + PNG.
- Profile banner: **1500 x 500**, SVG + PNG.
- Portrait launch poster: **1080 x 1350**, SVG + PNG.
- Built with Hakopod badge: **288 x 56** SVG, 2x PNG.
- Modular pod background: **1200 x 800**, SVG.

Copy direction: **Your apps. Your rules.** Supporting line: **A home for your apps, on infrastructure you own.** These assets contain no invented performance statistics or customer claims.

## Editing and handoff

Open SVG files in Figma, Illustrator, Inkscape, or a browser. Every symbol remains native vector geometry. Paths in outlined lettering can be edited as vector objects; use the included font and source builder when changing the actual marketing copy.

For accessible web images, supply context-appropriate alt text, such as `alt="Hakopod"`. If the adjacent link already has an accessible name, use empty alt text to avoid announcing the name twice. The `currentcolor` SVG variants inherit CSS color when inserted inline; an external `<img>` does not inherit the surrounding element's color.

The guide's text and artwork are vector outlines for faithful reproduction. `brand/brand-copy.md` and this README provide selectable text for the identity rules and marketing copy.

The source builder is included for regeneration, but **no authoring library is required to use the delivered assets**. The primary SVG symbol is under 1 KiB. The local gallery has no framework, analytics, or external runtime requests.

No application source or dashboard configuration was changed as part of this kit.

## Regenerate

Authoring tools: Python 3 with `fonttools` and `reportlab`; Node.js with `sharp`. From this kit folder:

```sh
python source/build.py --output .
node source/render.cjs .
```

The gallery, README, and copy document are edited directly. Regenerating the logo system does not rewrite those files.
