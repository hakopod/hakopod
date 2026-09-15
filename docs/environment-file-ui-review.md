# Environment file attachment UI review

Independent review on 2026-09-16 of the shared dashboard’s TOML and Compose
`env_file` attachment controls on `feat/env-file-import`. No open UI findings
remain in this reviewed slice.

The review used isolated headless Playwright, copied real dashboard source and
the Cloud overlay, with synthetic API responses. No production account,
credential, workload, email or deployment was changed.

## Coverage

Eight cases cover TOML and Compose imports in dark and Paper themes at 1440 and
390 pixels. Four additional cases cover keyboard file selection, paste
interaction and element bounds at the same themes and widths. Two mobile cases
exercise touch upload, removal and paste disclosure, plus the upload busy state. The review follows
[the standing UI checklist](ui-ux-checklist.md).

- Upload one or multiple files, edit the relative file paths, paste another file,
  remove an attachment and reattach it.
- Duplicate paths from paste and from edits preserve existing attachments and
  pasted content, and prevent a request with ambiguous filenames.
- Individual size, aggregate size, UTF-8 byte and eight-file limits reject the
  addition atomically without losing existing data.
- File upload, paths, removal, paste, source editors and parent mode controls
  disable while a file read is delayed.
- Failed plan and Compose conversion requests preserve attachments and draft
  contents for retry.
- Plan and Compose conversion send the exact synthetic `env_files` map.
  Canonical responses replace raw secret values with opaque references before
  review and do not display those raw values.
- Accepting generated Compose TOML replaces old TOML attachments; the subsequent
  review sends an empty file map instead of resending unreferenced files.
- Desktop and mobile controls stay within the content box, text remains readable
  in both themes, and there is no horizontal document overflow.

## Findings and resolution

1. Compose’s local file-read state did not disable the parent mode controls.
   Propagating `onBusyChange` fixed the gap; all eight delayed-read cases pass.
2. Accepting canonical Compose output retained attachments from an earlier TOML
   draft. Clearing those attachments fixed the invalid subsequent review
   request; the focused mixed-mode route passes.
3. The initial upload input rendered a 21-pixel-high native chooser instead of the
   existing shared upload action. It now uses the shared 40-pixel-high Button
   with a hidden input. Keyboard and touch activation pass, and final screenshots
   in both themes and widths show readable controls within the content bounds.

## Evidence and limits

Local ignored evidence is in `web/work/env-file-review/`: `review.mjs`,
`results.json`, `interaction.mjs`, `interaction-results.json`, `stale.mjs`,
`stale-results.json`, `touch-results.json`, logs and screenshots. Initial failing scripts or screenshots
are historical findings, not approval evidence.

This review verifies rendered interaction and request shape. Parsing relative
paths, secret persistence, authorization, API/CLI behavior and delivery to a real
cluster require the separate backend and acceptance checks. No production or
cluster success is inferred from mocked responses.
