# Development build-run review

DEVELOPMENT ONLY. This fixture renders the actual build and deployment routes with artificial read responses. It does not build, deploy, contact a provider, or access a cluster. Unexpected fetch requests fail. Artificial runtime state is never acceptance evidence.

Start on the development VM, from `web`, after the coordinated jobs permit a preview:

```sh
NODE_OPTIONS=--max-old-space-size=256 node review/build-run/server.mjs
```

The preview binds only `127.0.0.1:4203`, refuses port conflicts, and stops after 45 minutes. It uses a separate Vite cache. Forward that loopback port through the existing research tunnel. No production build is required.

Open `/builds/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa?fixture=queued`. The banner links select `queued`, `processing`, `blocked`, `deployed`, or `manual`.

- `queued` and `processing` both return a successful, digest-verified build whose automatic work is incomplete.
- `Advance automatic step` changes only the next artificial API response: queued to processing, then processing to deployed. It does not invalidate queries. Wait for the product's ten-second polling to update the page.
- `blocked` returns a completed build and an explicit automatic-deployment failure. Run-detail polling should stop.
- `deployed` returns a linked deployment with a recorded successful outcome. `Open deployment` shows that recorded result beside an artificial current runtime failure. This fixture intentionally verifies the distinction.
- `manual` returns a completed manual build. Run-detail polling should stop.

The banner displays the count of run-detail requests. It does not count the separate existing run-list polling. Use that counter to check the polling boundary. `Refresh observation` remains available after automatic polling stops.

Use the dashboard's theme control and desktop/mobile viewports. Review Source, Build, Image, and Deploy pipeline controls; keyboard focus; the changed Build result label; the blocked message; refresh; the deployment link; and wrapping of long identities. This identity is a viewer, so mutating build controls are intentionally absent. Other dashboard routes are outside this fixture's coverage.

The regression tests are imported by the existing dashboard UI test suite through `src/components/shared.test.tsx`. Run the normal dashboard tests and typecheck on the designated VM. Source inspection is not screenshot approval.
