# Shared API logs and resource removal UI review — 2026-09-14

Independent reviewer approval after source, interaction, real-bounds and screenshot checks using AGENTS.md and the Hatch checklist. Review used artificial local fixtures and a separate headless browser. Production source was not edited by this reviewer, and no live account/workload/deletion endpoint was used.

## Findings resolved and retested

1. Project trash stayed beside the badge because unlayered margin-left:0 overrode ml-auto. A scoped semantic rule now places it at the card's top-right in both themes/widths.
2. Closing the application-delete dialog returned focus to BODY because its menu trigger unmounted. Escape now returns to the persistent application ellipsis trigger in all four theme/width cases.
3. Wrapped logs kept fixed wide grid columns on mobile, placing the entire message offscreen behind apparently blank tall rows. Shared wrap rules now put metadata over the message on mobile and constrain desktop message tracks. Real message bounds fit the viewport for both service and installation viewers.
4. The API refresh/source notice was flush against the viewer border. Shared inner spacing now aligns it with chart/query content.

## Rendered coverage and results

Dark and Paper themes at 1440px and 390px:

- Installation API log explorer and application service-log explorer: 8 loaded-page screenshots, plus 8 settled viewport inspector screenshots. Query textarea, Errors example, Run query request body, wrap, histogram selection/reset, Copy clipboard data, JSONL Download action and row inspection passed. API mode has no Pod/Container inputs; service mode retains them. Inspector uses Source for API versus Pod/Container for service output.
- Project overview and project-delete popup: 8 screenshots. Icon is at top-right, click opens the confirmation rather than card navigation, and Escape restores icon focus.
- Application-list nonempty delete popup and empty-app conflict popup: 8 screenshots. Dialog is outside closed menu, nonempty application cannot submit even with exact name, empty application requires matching text. After opening at revision 7 and changing fixture's live application to 99, mocked DELETE still sends expected_revision 7. HTTP 409 remains visible and preserves typed confirmation.
- Service-card Delete service menu opens configure?remove=api&mode=form with the api service absent from the draft, the remaining client visible, and Review changes still required. Four screenshots confirm the review-based form, with no deploy mutation sent.
- All 36 resulting page/dialog captures were reviewed via contact sheets, with full-size mobile logs/inspectors and project cards inspected closely. Page documents stayed at viewport width; wrapped message bounds are within it; controls stay reachable. Zero page errors in final matrix.

Installation live-refresh behavior was additionally exercised: after 5.3 seconds request count increased 1 to 2; switching to Query stopped refresh; simulated hidden visibility stopped refresh. EventSource constructor count stayed 0. Source/reset/start and sampling-limit notices are visible; fixture warnings identify unavailable journal history. The fixture models a bounded process sample; it does not implement backend query parsing.

## Limits

This focused review does not claim live installation/journal access, actual log filtering correctness, backend authorization, successful resource deletion, Kubernetes retirement, real SMTP behavior, or physical-device/Safari/Firefox testing. Backend tests belong to the implementation agent. Service Live tail's existing SSE transport was source-inspected but not newly exercised. Denied/loading/error branches were source-inspected, except the application-delete conflict path that was rendered. Process logs were used for the new explorer's rendered matrix; journal API sampling behavior was not newly rendered in this pass (the earlier simple-log source-copy review is separate).

Local evidence lives under `work/ui-migration/logs-resource-review/`: logs.json, delete.json, poll.json; logs.mjs, delete.mjs, poll.mjs; screenshots/*.png and contact sheets. Inspector captures use viewport screenshots after transitions settle; earlier full-page inspector captures were replaced because fixed overlays distort their appearance in full-page output. Test browser contexts and fixture server were stopped after review.

The standing [Hatch checklist](ui-ux-checklist.md) remains the review gate; its full copy is retained with the local evidence.
