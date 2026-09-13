# Public TCP installation-policy UI review

This independent review covers the service Networking panel's installation-policy
message and the Overview/Networking “Configured exposure” label. The certificate
form is unchanged and retains its [earlier review](delivery-ui-review.md).

The reviewer followed the [Hatch checklist](ui-ux-checklist.md). An isolated fixture
at `127.0.0.1:4194` rendered the real dashboard components using the backend's exact
self-hosted and managed-cloud messages. All data was artificial and external
requests were blocked. No production account or deployment was changed.

The matrix covers self-hosted empty/configured listeners, managed-cloud
empty/legacy-disabled listeners, loading, API failure and read-only roles at 320
and 1484 pixels in dark and Paper themes. A configured listener uses port 45873 to
exercise an ordinary administrator-provisioned TCP port. The managed legacy case
represents retained desired configuration, not a claim that managed cloud can
start with an active public TCP route.

Screenshot review found two issues, both resolved before the final pass:

- A disabled legacy listener repeated the installation-policy sentence. The
  shared policy appears once; the listener retains its port and disabled status.
- Static TOML exposure looked like observed reachability. Overview and Networking
  now label it “Configured exposure”; runtime policy and status remain separate.

Final review passed:

- [x] All 36 state/theme/viewport cases passed, including Overview labels.
- [x] No page exceptions, missing fixture responses or horizontal overflow.
- [x] Policy text appears once; unavailable managed-cloud states do not imply
  enabled listeners or show the redundant empty-list message.
- [x] Read-only roles retain policy visibility without mutation controls.
- [x] Four interaction checks passed for keyboard focus, help/Escape,
  desktop hover or mobile touch, and aborting/releasing an inactive probe.
- [x] Final screenshots were inspected after fonts and expected content loaded.
  Long values and the two-line mobile exposure label stay readable.
- [x] Fixture browsers and server were stopped after review.

The evidence lives under
`work/ui-migration/public-tcp-policy-review/`, including fixture sources,
`results.json`, element bounds and screenshots. Backend enforcement and real
network isolation are verified separately; browser fixtures cannot prove them.
