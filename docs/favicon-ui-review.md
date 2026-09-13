# Dashboard favicon review

Reviewed independently on 14 September 2026 against
`6133abff7dc8d14717afb75a4584d76d4c8be64d` on
`fix/dashboard-brand-favicon`. The production preview was served at
`http://127.0.0.1:4173/`.

**Result: approved for the favicon and shared document-head change.** No visual or
asset findings remain within this scope.

## Scope and checklist

This applies the [Hatch review checklist](ui-ux-checklist.md) to the affected
document metadata and brand assets. No page layout or interactive component
changed.

- [x] The ICO, PNG and Apple icon are byte-for-byte copies of the brand kit.
- [x] The existing SVG remains unchanged and also matches the brand kit.
- [x] Native-size icon renders are legible against Paper and dark backgrounds.
- [x] The shared document exposes all four versioned links on root and nested URLs.
- [x] Declared formats and dimensions match the supplied assets.
- [x] All linked assets return HTTP 200 and match their local source bytes.
- [x] The change adds no runtime service, polling, dependency or animation.
- N/A: page insets, headings, navigation states, cards and responsive layouts.
- N/A: keyboard/touch controls, forms, authentication, permissions and mutations.
- N/A: application data, metrics, loading states and live cluster behavior.

## Evidence

The document-head check passed eight cases: `/` and
`/applications/0db7a9ffde6c8e2f6d74a963230a93b8`, each at 390 and 1440 pixels in
light and dark themes. Fresh isolated browser contexts verified exactly these
four icon links:

| Asset | Document declaration | Verified source |
| --- | --- | --- |
| `/favicon.ico?v=hakopod-1` | `icon`, `image/x-icon` | `brand-kit/icons/favicon.ico`; 16, 32 and 48 pixel frames |
| `/favicon-32.png?v=hakopod-1` | `icon`, `image/png`, `32x32` | `brand-kit/icons/favicon-32.png`; 32 × 32 |
| `/favicon.svg?v=hakopod-1` | `icon`, `image/svg+xml`, `any` | `brand-kit/icons/favicon.svg`; scalable 16 × 16 viewBox |
| `/apple-touch-icon.png?v=hakopod-1` | `apple-touch-icon`, `180x180` | `brand-kit/icons/icon-180.png`; 180 × 180 |

Four independent HTTP/local/brand-kit byte comparisons passed. The server returns
`image/vnd.microsoft.icon` for the ICO and the expected PNG/SVG MIME types for the
other assets. Both ICO MIME names describe the supplied icon format.

The rendered contact sheet shows ICO and SVG marks at 16 and 32 pixels, the PNG
at 32 pixels, and the Apple icon at 180 pixels, all at device scale 1 in both
surrounds. Direct screenshot inspection confirms the Ion symbol remains distinct
at the smallest size, with clear gaps between its bars. The dark tile separates it
from Paper; on the matching dark surround the tile blends while the symbol retains
contrast. No stretching or clipped mark was observed.

Ignored local evidence is retained in the website workspace under
`work/expansion-review/`: `favicon-native-contact.png`, `favicon-results.json` and
`favicon-review.mjs`. The complete result contains eight passing document cases
and four passing asset comparisons, with zero failures.

## Limits

Review used installed Chromium through an isolated Playwright harness and Pillow
for raster dimensions. It did not use the user's browser profile or alter an
account or deployment. The harness closed its browser afterward.

This verifies shared metadata and rendered image assets, not loaded authenticated
application content. Browser-native tab icon selection, existing favicon-cache
eviction, Safari/Firefox selection and installation on a physical Apple device
remain unverified. The version query changes the requested asset URLs; it does not
establish that every browser has discarded an older cached icon.

Build, typecheck and CI results are the implementer's separate verification. This
review does not make broader dashboard layout or device-compatibility claims.
