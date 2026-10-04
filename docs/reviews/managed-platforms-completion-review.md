# Managed-platform dashboard review

Reviewed on 2026-10-03 against the [Hatch checklist](../ui-ux-checklist.md).
An independent reviewer inspected the rendered screenshots and the recorded
element bounds. This is UI evidence from an explicitly marked development
fixture. It does not establish native runtime, recovery or production readiness.

## Coverage

- Databases and Platforms lists, platform creation, Supabase configuration,
  detail tabs, backup, restore and cancellation.
- Dark and Paper themes at 1440, 390 and 320 pixels.
- Empty, unavailable, denied, loading, failed submission and stale data.
- Retained form values after failed create, configure, backup and restore
  requests; project scope and global back navigation.
- Touch tab selection, keyboard tab navigation and visible focus, help on
  touch, and horizontal scrolling inside the resource table.

The final matrix contains 268 captures with no browser errors or geometry
failures. Separate edge checks passed in both themes. At 320 pixels, the
resource table has a 286-pixel viewport and a 323-pixel scrollable content area;
the storage column remains reachable without document overflow. Every selected
tab remains fully visible. Keyboard focus has a two-pixel solid outline.

## Corrections made during review

- Added Databases/Platforms navigation and scope-aware back links.
- Kept nested creation URLs bound to their requested project and environment.
- Removed duplicate setup headings and moved configuration actions into the
  resource heading row.
- Preserved page headings in unavailable and denied form states.
- Removed the decorative empty-state grid in the self-hosted edition.
- Added a visible interruption warning before applying configuration changes.
- Stopped fetching backup destinations outside the permitted backup flow and
  stopped polling lifecycle history outside the Activity tab.

The independent reviewer found no remaining visual blocker within this scope.
The evidence covers managed-platform routes and their shared navigation and
empty-state treatment; it does not claim a new review of every unrelated route.

Ignored evidence lives under
`work/database-enterprise/platform-ui-20261003/`. VM evidence uses the matching
`/srv/hakopod-backup-scratch/platform-ui-20261003/` directory. The final screenshot
matrix is `captures-v9/results.json` from capture run v10; the final interaction
record is `edges/results.json` from edge run v3. Source builds, type checking
and all 211 dashboard UI tests passed on the isolated validation VM.
