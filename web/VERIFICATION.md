# Forms, catalog, backups, and host access — 2026-09-12

The current dashboard adds dedicated nested form pages with breadcrumbs and help,
Git repository bootstrap from a reviewed immutable commit, searchable database
and agent catalogs, original provider/service icons, profiles and avatars, team
usernames, custom-domain ownership and routing review, database backup/restore
management, explicit host-terminal grants, and the tracked fresh-install sample
banner. All screens use the Go API. The reports below this section are historical
and do not establish verification of these additions.

## Current checks

- Production `pnpm build`, strict `pnpm typecheck`, `pnpm format:check`, and all
  20 tests passed after final source changes. The generated route tree includes
  the new nested pages and the aggregate OpenAPI/TypeScript contract includes
  profiles, host access, backups, domains, repository import, and final template
  and sample schemas.
- The test suite includes 16 BFF/security/stream tests and four regressions bundled
  from actual TSX/code. New host-authority coverage proves that a browser owner
  or explicit matching node grant enables host controls; administrator status
  alone, the wrong node, and machine credentials do not. Canonical TOML preserves
  quoted custom-domain mappings alongside volume/GPU/TLS/secret settings.
- An isolated production server using `scripts/serve.mjs`, a random in-memory
  session secret, and ephemeral loopback port 49538 passed public HTTP smoke:
  real SSR and CSS, unauthorized access, exact GitHub/GitLab webhook method
  restrictions, mutation Origin rejection, and the sign-in body-size bound.
  Representative original SVGs served with the correct content type. The process
  was removed afterward; no live account was created or modified.
- Initial SSR HTML does not preload the xterm engine. Production SSR bundles the
  shared UI source without a raw `@hakopod/ui` runtime import. Client output
  contains neither the configured session secret nor `HAKOPOD_SESSION_SECRET`,
  `HAKOPOD_API_URL`, or `node:crypto`; only boolean scan results were emitted.
- Authored source/scripts/documentation passed the no-emoji audit, and the web
  diff passed whitespace checks. Original SVG assets and their licenses remain
  unchanged, with source attribution under `public/icons`.

The final source review also corrected hidden provider URL submission when
switching back to OpenAI, made model identifiers required before model-template
review, and hardened terminal cleanup against abandoned asynchronous connection
attempts. Repository review tokens and backup credentials/recovery keys stay in
component state, outside URLs, persistent browser storage, and query caches.
Avatar image requests omit credentials and referrers and use opaque seeds.

## Current resource measurements

These are complete build-output comparison sizes, using gzip level 9 separately
for each file. They are not measured network transfer or initial-route totals.

| Asset group                            | Files | Raw bytes | Gzip bytes |
| -------------------------------------- | ----: | --------: | ---------: |
| All client JavaScript                  |    73 | 1,193,180 |    370,602 |
| Main entry (included above)            |     1 |   428,912 |    134,792 |
| Optional xterm engine (included above) |     1 |   331,178 |     81,915 |
| All CSS                                |     2 |    93,266 |     18,647 |
| Self-hosted variable font              |     1 |   136,676 |     63,683 |
| Local provider/service SVGs            |    28 |   101,521 |     47,634 |

The isolated production Node process was observed at **91,040 KiB RSS (about
89 MiB)** immediately after public smoke and static-icon requests. The production
command retains its **192 MiB old-space cap**. This is a point-in-time measurement,
not a peak, concurrency benchmark, browser-memory measurement, or RSS guarantee.

New backup views retain one page of at most 100 job/artifact records, with at most
20 previous cursor strings; destination/schedule/target lists are bounded to
32/64/128. These query caches become collectable when their last observer leaves.
Only a selected unfinished backup job polls at 2.5 seconds. Hidden tabs do not
poll. Catalog search is bounded to 100 characters, and individual static icons
are requested as needed without a new runtime dependency.

Existing bounds remain: 25 applications per page, 32 topology services, 1,000
queried log entries, 1,000 live log lines / 256 KiB, 500 terminal scrollback lines,
16 KiB queued terminal input, 4 KiB terminal blocks, and 16 KiB incomplete SSE
frames. Terminal rendering applies read backpressure; Go enforces four sessions
globally, ten-minute lifetimes, and two-minute idle timeouts. New form pages add
route chunks; the terminal engine still loads only after Connect.

## Current verification limits

The browser connector returned **“Codex auth token is unavailable.”** New visual,
keyboard, responsive-layout, avatar-network, and browser-memory verification is
not claimed. Earlier screenshots and browser checks below describe prior builds.
The live installation is already claimed by the user and was preserved.

Dashboard checks did not issue external provider writes, invitation email,
live profile/theme updates, host commands, backup operations, or cluster mutations.
Public HTTP smoke did not authenticate to Go. Real API/provider/cluster acceptance
and full host recovery are recorded by the repository's separate test and recovery
reports. The database backup interface does not by itself verify restoration of
the Kubernetes datastore, K3s material, or the entire installation.

---

# Historical cockpit and design-system expansion — 2026-09-12

The current dashboard builds and runs in production with the shared local
`@hakopod/ui` package, supplied Hakopod brand assets, application/node inspectors,
dependency topology, SQL-like log explorer, pod/database terminal, signed-license
settings, paid-feature gates, and GitHub/GitLab source/build choices.
The user has now claimed the live installation. Earlier references below to an
unclaimed installation record the state at the time of those historical checks.

Current checks passed after regenerating the aggregate OpenAPI and TypeScript
contracts, including GitLab build provider/run fields and safe team deletion:

- `pnpm typecheck`, `pnpm format:check`, and production `pnpm build`.
- All 19 tests: 16 server/security tests and three regressions bundled from
  actual TSX/code. New coverage includes GitLab raw webhook byte/header isolation,
  exact webhook paths/methods/body bounds, tiny request allocation, and terminal
  SSE framing. The terminal parser accepts over 128 KiB of coalesced valid frames,
  preserves split UTF-8 bytes, and rejects oversized output or incomplete frames.
- Isolated production HTTP smoke using `scripts/serve.mjs`, a random in-memory
  session secret, and ephemeral loopback port 61345. Real SSR and static CSS,
  unauthorized access, both public webhook method restrictions, mutation Origin
  rejection, and the sign-in body bound passed. This public-only smoke did not
  authenticate to Go or create an account. Its temporary process was removed;
  the existing dashboard on port 4173 was untouched by this check.
- Production SSR contains bundled UI code, with no runtime import of raw
  `@hakopod/ui` TSX. The client contains neither the configured session secret nor
  `HAKOPOD_SESSION_SECRET`, `HAKOPOD_API_URL`, or `node:crypto`. Only pass/fail
  results were printed. Initial SSR HTML does not preload the xterm engine.

The direct srvx adapter resolves static assets explicitly and has no per-request
access logger. This avoids logging OAuth callback query credentials. Ordinary
API forwarding remains bounded to 30 seconds; terminal output uses an
eleven-minute BFF timeout around the server's ten-minute lifetime.

## Current resource measurements

The complete client output has 41 JavaScript chunks: 1,099,398 bytes raw and
333,929 bytes when each file is gzipped at level 9. The main entry is 397,117
bytes raw / 125,002 bytes gzip. The optional xterm engine is a separate 331,178
byte raw / 81,915 byte gzip chunk, imported only after Connect; the fit addon is
also separate. All CSS totals 86,893 bytes raw / 17,550 bytes gzip. The self-hosted
Space Grotesk variable font is 136,676 bytes raw / 63,683 bytes gzip. These are
build comparison sizes, not measured network transfer or initial-route totals.

The isolated production Node process was observed at 93,248 KiB RSS (about
91 MiB) immediately after public smoke. The production command retains the
192 MiB old-space cap. This point-in-time measurement is not a peak, concurrency
benchmark, browser-memory measurement, or RSS guarantee.

Explicit bounds include 25 applications per list page; immediate release of
unobserved full application/deployment queries; 60-second collection for small
metadata; 32 topology services; 1,000 queried log entries; and 1,000 lines /
256 KiB for live logs. Live logs render at most four times per second and stop
in hidden tabs. Terminal scrollback is 500 lines, queued input is at most 16 KiB,
input/output blocks are at most 4 KiB, and incomplete SSE frames are at most
16 KiB. Rendering applies backpressure to terminal reads. Small proxied input
starts with at most a 4 KiB allocation instead of reserving the 1 MiB body limit.
Go independently limits terminal sessions to four globally, ten minutes total,
and two minutes idle. Session closure runs when the terminal panel is left.

## Current verification limits

The browser connector returned “Codex auth token is unavailable.” No new visual
browser, keyboard, responsive-layout, or browser-memory verification is claimed
for this expansion. The historical screenshots and authenticated checks below
describe earlier dashboard builds. Current Go/provider/cluster acceptance is
reported separately by the repository's test and acceptance results; the UI
build and public HTTP smoke do not establish those behaviors.

No external provider write, invitation email, live owner setup, license issuance,
pod command, or cluster mutation was performed by these dashboard checks.

---

# Historical expanded dashboard — production HTTP verified; browser checks pending

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
both temporary processes were removed. The live installation was unclaimed at
the time of that check; the user has since claimed it.

The browser connector returned “Codex auth token is unavailable,” and native
Codex access was denied. New screens therefore have no claimed visual browser
verification. The historical screenshots below describe the earlier dashboard.

No live owner account, invitation
email, repository commit, enrollment token, node operation or deployment was
created by the dashboard tests. Full HTTP smoke requires an explicitly supplied
existing human login file; it never bootstraps an owner. The first-owner form has
empty name, email and password fields.

---

# Historical foundation dashboard verification — 2026-09-12

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
