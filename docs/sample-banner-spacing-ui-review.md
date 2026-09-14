# Sample banner spacing independent UI review

Approved on 14 September 2026 for the single `.sample-banner` `@apply mt-6`
addition on `fix/sample-banner-spacing`, based on `94005ad`. No new regression
was found in this scoped change.

All **8 requested cases passed**: application detail and the
`/projects/demo?environment=development` list at 1440/390 in Paper and dark.
Eight actual screenshots were inspected after the real showcase response and
fonts loaded. Every case measured a 24px top margin and a 24px gap below the
header; the 22px bottom margin, 17px/20px internal padding and 15px internal gap
remain unchanged. Banners retain 24px desktop / 16px mobile page insets and cause
no document overflow.

Keyboard and touch opened removal confirmation and dismissed it with Escape or
Keep sample, restoring focus. Explore sample reached the tracked application in
all four list cases. No removal request was sent. The fixture shows a real
accepted showcase marker with queued/not-observed state; no workload execution
is implied.

**Unchanged follow-up:** at 390px the Queued badge ends at x383.19 while the banner
ends at x374, extending 9.19px beyond its border but remaining inside the viewport.
A browser-only baseline with the new top margin removed reproduced identical
horizontal child geometry; only banner y changed from 113 to 89. This pre-existing
title-row crowding is outside the approved margin-only patch.

The [standing checklist](ui-ux-checklist.md) was applied to the affected source,
themes, loaded screenshots, element bounds, page insets, keyboard/touch and real
fixture observations. Unchanged route families, removal execution, Kubernetes,
physical devices and other browsers were not retested. The implementer reported
passing build/typecheck/format/Go tests. This review does not approve or claim an
Ubuntu artifact installation; that remains the release owner's separate step.

Evidence: ignored `work/sample-banner-spacing-review/` contains `review.mjs`,
`results.json`, `review.log`, eight screenshots, and the quantified baseline in
`baseline.json`. Review used disposable UI/API ports 4317/8217, with no workers.
Credentials were kept private; reviewer browsers closed. No product source,
4173 preview, or Ubuntu 4174/API was changed by the reviewer.
