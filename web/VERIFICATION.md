# Dashboard verification — 2026-09-12

## Completed

- Node 22.13.1, pnpm 10.7.1; frozen dependency graph in `pnpm-lock.yaml`.
- Generated API types refreshed from the final OpenAPI schema, including cursor
  pagination and metadata-only `DeploymentSummary` history. All queries and
  management mutations use the typed openapi-fetch client.
- `pnpm typecheck`: passes with strict, no-unused-locals, and no-unused-parameters.
- `pnpm format:check`: passes across the dashboard source and scripts.
- `pnpm test`: four security-boundary tests pass: session integrity/expiration;
  cookie/Origin enforcement; chunked-body memory bound; verified-HTTPS or loopback
  management API validation.
- `pnpm build`: client and production SSR build pass. No runtime chart, editor,
  topology, animation, or testing dependencies.
- `pnpm audit --prod`: no known vulnerabilities reported by the registry at the
  time of verification. This is not a guarantee that dependencies have no defects.
- Production started with `PORT=4173 HOST=127.0.0.1 pnpm start`. srvx correctly
  resolves `-s ../client` relative to the server entry in `dist/server`.
- `pnpm smoke --public-only` passes on the final build: SSR, real static CSS,
  unauthorized request rejection, cross-origin mutation rejection, and sign-in
  body-size rejection. This mode deliberately does not authenticate to Go.
- Before the shared container-engine outage, the full HTTP smoke passed:
  administrator authentication against the real Go API, encrypted cookie,
  authenticated proxy, CSRF, endpoint allowlist, 1 MiB body bound, and logout.
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

Local, ignored screenshot files:

- `.local/screenshots/hakopod-login-desktop-dark.png` (repository root)
- `.local/screenshots/hakopod-login-mobile-light.png` (repository root)

## Resource measurements and enforced limits

The final compiled client contains 12 JavaScript chunks totaling 579,695 bytes
across **all** routes. Their gzip level 9 comparison size is 188,084 bytes; CSS
gzip level 9 comparison size is 10,575 bytes. These are build-size measurements, not a promise
about transfer size without HTTP compression. Routes load separately.

The final production Node process was observed at 88,912 KiB RSS (about 87 MiB)
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

The shared OrbStack/Docker engine became unresponsive during the independent
real-cluster acceptance run. Go `/healthz` continued to return 200, while
`/readyz` returned 503 `PostgreSQL is unavailable` and `/api/v1/me` returned 503
after its timeout. This blocked additional authenticated browser verification;
it was not a browser authentication workaround or a simulated backend.

After the actual development engine and API become ready, rerun the full smoke:

```sh
HAKOPOD_WEB_URL=http://127.0.0.1:4173 \
HAKOPOD_API_KEY_FILE=../.local/admin-key pnpm smoke
```

Then verify the final application detail, metadata history, paginated overview,
deployment diff and submit, failure timeline, service logs, node list, API-key
forms, and authenticated mobile/theme views in the browser against real data.
These workflows are implemented and typechecked, but their final browser pass
must not be reported as complete until performed. Kubernetes rollout correctness
and management-restart acceptance are recorded by the repository's cluster/API
acceptance suite, separately from this dashboard report.
