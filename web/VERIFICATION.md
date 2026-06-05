# Expanded dashboard — production HTTP verified; browser checks pending

The expanded dashboard is implemented and the production bundle built successfully
on 2026-09-12. The previous foundation verification below remains historical
and does not verify the new screens.

New local checks passed:

- `pnpm typecheck`, `pnpm format:check`, and `pnpm build` after route generation.
- Ten BFF/server security tests: human token stripping and encryption, installer
  proof forwarding without profile substitution, CSRF/body limits, provider MFA,
  OAuth state/errors, upstream session revocation, raw signed webhook forwarding,
  exact public webhook path/method, and webhook memory bounds.
- Three regressions bundled from actual TSX/code: Copy does not submit a form;
  canonical TOML preserves volumes/GPU/TLS/registry/secret settings; broad browser
  session envelopes do not override exact project roles.
- Client output contains neither the configured session encryption secret nor its
  environment-variable name nor a `node:crypto` import. Only booleans were printed.

The client has 33 JavaScript chunks totaling 711,114 bytes, or 232,192 bytes when
gzipped individually. The main entry is 413.18 kB raw / 131.41 kB in Vite's gzip
report; many optional management panels are 1–5 kB gzip. CSS is 11,855 bytes using
Node's gzip comparison. These are comparison sizes, not measured transfer or RSS.
The production command retains the 192 MiB Node old-space cap. Current process
measurements are recorded with the completed cockpit report.

The exact public POST `/api/v1/webhooks/github` bridge preserves raw bytes and
GitHub signature/event/delivery headers, forwards no browser authority, and leaves
HMAC validation to Go. Other mutations retain same-origin checks. Public smoke
now also checks that the exact webhook route accepts POST only.

The new production bundle was restarted on port 4173. Public production smoke
passed against the live Go API. A separate production dashboard/API and isolated
PostgreSQL database verified first-owner setup, email/password login, encrypted
Strict/HttpOnly cookies, authenticated proxying, ten account/platform routes,
team creation/membership, CSRF/path/body limits, logout and subsequent HTTP 401
from Go for the revoked session. The disposable database, credentials, logs and
both temporary processes were removed. The live installation remains unclaimed.

The browser connector returned “Codex auth token is unavailable,” and native
Codex access was denied. New screens therefore have no claimed visual browser
verification. The historical screenshots below describe the earlier dashboard.

No live owner account, invitation
email, repository commit, enrollment token, node operation or deployment was
created by the dashboard tests. Full HTTP smoke requires an explicitly supplied
existing human login file; it never bootstraps an owner. The first-owner form has
empty name, email and password fields.

---

# Dashboard verification — 2026-09-12

## Completed

- Node 22.13.1, pnpm 10.7.1; frozen dependency graph in `pnpm-lock.yaml`.
- Generated API types refreshed from the final OpenAPI schema, including cursor
  pagination and metadata-only `DeploymentSummary` history. All queries and
  management mutations use the typed openapi-fetch client.
- `pnpm typecheck`: passes with strict, no-unused-locals, and no-unused-parameters.
- `pnpm format:check`: passes across the dashboard source and scripts.
- `pnpm test`: five tests pass: session integrity/expiration; cookie/Origin
  enforcement; chunked-body memory bound; verified-HTTPS or loopback management
  API validation; and generated-key copying cannot submit its creation form.
  The last test renders the actual shared Copy component inside a form and
  failed on the previous implicit-submit button, then passed after the fix.
  It uses the existing Vite/React toolchain and adds no dependency.
- `pnpm build`: client and production SSR build pass. No runtime chart, editor,
  topology, animation, or testing dependencies.
- `pnpm audit --prod`: no known vulnerabilities reported by the registry at the
  time of verification. This is not a guarantee that dependencies have no defects.
- Production started with `PORT=4173 HOST=127.0.0.1 pnpm start`. srvx correctly
  resolves `-s ../client` relative to the server entry in `dist/server`.
- `pnpm smoke --public-only` passes on the final build: SSR, real static CSS,
  unauthorized request rejection, cross-origin mutation rejection, and sign-in
  body-size rejection. This mode deliberately does not authenticate to Go.
- After shared container-engine recovery, the full HTTP smoke passed again on
  the final production dashboard: administrator authentication against the real
  Go API, encrypted cookie, authenticated proxy, CSRF, endpoint allowlist,
  1 MiB body bound, and logout.
- Browser verification against the real Go API before the outage: sign-in,
  application overview, project `demo` / environment `development`, real `shop`
  application, revision 3, two services, actual public endpoint, and an observed
  **partial** status. No fixture or fabricated health was used.
- Final production browser verification: SSR hydration and sign-in UI at
  1280×720 dark and 390×844 light. Mobile document width equals viewport width;
  there is no horizontal overflow. Theme control works, password values remain
  masked, and temporary viewport override was reset after testing.
- The UI displayed the actual Go error during the outage rather than inventing
  applications or successful authentication.
- After recovery, native Chrome sign-in succeeded against the real API. The
  overview rendered two actual applications (`shop` and `cli-check`), four
  services, and observed healthy status for both during the acceptance run.
  The application history rendered revisions 1–10 with real succeeded, failed,
  cancelled, and running states; the later overview displayed `shop` revision 11.
  These were observed browser states, not fixtures or a separate test API.
- The main task's isolated in-app browser completed service details, deployment
  history, revision 10's real failure reason and recovery timeline, configuration
  diff, networking, and live log follow/pause against the recovered Go API.
- The isolated browser submitted a TOML update after staging and reviewing four
  changed fields. Deployment `16da0ed5cde2e25da196fa620f68536b` became healthy
  `shop` revision 13 after the independent acceptance run had finished.
- Infrastructure showed the actual node `k3d-hakopod-dev-server-0`, ready 1/1,
  arm64, Kubernetes `v1.35.8+k3s1`, and seven pods.
- Authenticated mobile navigation opened, followed Applications, and closed;
  light theme worked. The requested 390×844 override yielded an effective CSS
  viewport of 325 pixels in that browser; document scroll width was also 325,
  confirming no horizontal overflow at the measured width. The override was reset.
- On the final rebuild, the browser created a `Browser verification` key scoped
  to `demo / development / ui-check`, with only `deployments:read` and an 89-day
  expiration. Clicking Copy left exactly one matching key in the backend.
  Rotation displayed 89 days selected, and the replacement's actual expiration
  matched that choice while preserving scope and permissions. The original key's
  remaining lifetime was reduced to at most 15 minutes, and both credentials
  authenticated during overlap.
  Both keys were then revoked through the UI and returned 401. Audit events
  showed creation, rotation, and revocations. Temporary credential files were
  deleted and the test clipboard was restored empty.
- The final mobile focus check used an actual CSS viewport of 390 pixels.
  Closed navigation had computed `visibility: hidden`, was absent from the
  accessibility tree, and received no focus during ten real Tab presses.
  Document scroll width and viewport width were both 390 pixels, with no
  horizontal overflow. The temporary viewport override was reset.
- The final desktop overview screenshot was refreshed after the rebuild and
  showed two real healthy applications and four services, with no credential
  values visible.

The final rebuild also corrects the key-copy implicit form submission, rejects
duplicate key submission while busy or showing an issued key, makes 89-day
expiration available consistently for creation and rotation, and removes claims
that the current API supports secret bindings. Closed mobile navigation now uses
`visibility: hidden` and exposes expanded/controls semantics on its opener.

Local, ignored screenshot files:

- `.local/screenshots/hakopod-login-desktop-dark.png` (repository root)
- `.local/screenshots/hakopod-login-mobile-light.png` (repository root)

User-facing authenticated screenshots are in the task's outputs directory:

- `/Users/theboringhumane/Documents/Codex/2026-09-12/s/outputs/hakopod-dashboard.png`
- `/Users/theboringhumane/Documents/Codex/2026-09-12/s/outputs/hakopod-deployment.png`
- `/Users/theboringhumane/Documents/Codex/2026-09-12/s/outputs/hakopod-mobile.png`

## Resource measurements and enforced limits

The final compiled client contains 12 JavaScript chunks totaling 579,768 bytes
across **all** routes. Their gzip level 9 comparison size is 188,084 bytes; CSS
gzip level 9 comparison size is 10,591 bytes. These are build-size measurements,
not a promise about transfer size without HTTP compression. Routes load separately.

The final production Node process was observed at 96,256 KiB RSS (about 94 MiB)
and 0.0% CPU after the production smoke test. That is a point-in-time
measurement, not a peak, concurrency benchmark, or RSS guarantee. The 192 MiB
old-space cap controls JavaScript heap, not all native process memory.

The application list holds one server page (at most 25 items, with the API's
512 KiB spec/observation budget). Previous navigation retains at most 20 cursor
strings. Full application and deployment cache entries are collected as soon as
their last observer leaves. History contains metadata; a prior full revision is
requested individually for a diff. Smaller query entries expire after 60 seconds.
Logs retain at most 131,072 UTF-16 code units (256 KiB) / 1,000 lines and render
at most four updates per second. The line bound uses a backward scan rather than
allocating an array of lines. Hidden tabs do not poll, and hidden log views abort
their streams.

## Remaining verification

The initial shared engine outage was resolved before the final authenticated
HTTP and browser checks. The main task's isolated browser completed the checks
above while native Chrome was left under user control. No simulated backend was
substituted.

The final key lifecycle and mobile focus checks passed. Multi-page application
navigation and UI cancellation/rollback submission have not been exercised in
this browser pass. Do not infer those from API or source checks.

To rerun the authenticated production HTTP smoke:

```sh
HAKOPOD_WEB_URL=http://127.0.0.1:4173 \
HAKOPOD_SMOKE_LOGIN_FILE=/path/to/protected-human-login.json pnpm smoke
```

Kubernetes rollout correctness and management-restart acceptance are recorded
by the repository's cluster/API acceptance suite, separately from this dashboard
report. Use a separate `ui-check` application for browser writes if that suite
is concurrently using `shop`.
