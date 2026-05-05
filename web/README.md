# Hakopod dashboard

TanStack Start presentation layer for the Go management API. Go owns authorization,
validation, plans, deployments, and reconciliation. The dashboard never connects to
Kubernetes or PostgreSQL.

## Run

Use Node.js 22.12 or newer and pnpm 10.7.1:

```sh
cd web
pnpm install --frozen-lockfile
cp .env.example .env.local
chmod 600 .env.local
```

Set `HAKOPOD_API_URL` to the management API origin (default
`http://127.0.0.1:8080`). HTTPS is required unless that host is loopback. The URL may
end in `/api/v1`, but cannot include credentials, a query, or a fragment. Normal TLS
certificate verification is always enabled.

Set `HAKOPOD_SESSION_SECRET` to at least 32 random characters; generate a value with
`openssl rand -base64 48`. For a local installation, remove `HAKOPOD_WEB_ORIGIN`
from `.env.local`. For a TLS reverse proxy, set it to the exact dashboard HTTPS
origin, for example `https://console.example.com`.

```sh
pnpm dev
# Vite uses 3000, or the next free port. Existing services are not stopped.

pnpm build
PORT=4173 HOST=127.0.0.1 pnpm start
```

The production command loads `.env.local` if present, serves `dist/client` through
srvx, and forwards dynamic requests to the built TanStack Start fetch handler.
The static path `../client` is relative to `dist/server/server.js`, as defined by
srvx. Set `HOST=0.0.0.0` only when access is intentionally provided through the
installation's firewall and TLS reverse proxy. Keep the dashboard origin separate
from deployed applications.

Sign in using the installation's administrator or scoped API key. No key is
bundled into the application or placed in URLs. The local development bootstrap
key is managed by the repository's development scripts, outside `web/`.

## Implemented workflows

- Project/environment selection and project creation for administrators.
- Real application overview, grouped services, desired versus observed state,
  deployment history, public URLs, private addresses, and network membership.
- Image form and strict TOML import → Go plan → paginated before/after diff →
  revision-checked, idempotent deployment submission. Resource profiles are
  displayed from the plan; staged edits have no infrastructure effect.
- Deployment timeline, explicit failure explanations, resolved image digests,
  per-service results, safe-boundary cancellation, and auditable rollback from
  a previous successful revision.
- Service-specific recent/live logs with bounded reconnection, hidden-tab pause,
  and an explicit ephemeral-log notice.
- Real Kubernetes node readiness, architecture, pod count, and allocatable CPU
  and memory. Allocatable values are clearly distinguished from resource usage.
- Administrator key creation, metadata, expiration, last-use, revoke, rotation
  with bounded overlap, and recent audit metadata. Created keys are shown once.
- Keyboard-accessible Radix dialogs, tabs, menus; dark/light themes; responsive
  navigation; deliberate loading, empty, permission, and error states.

Custom-domain management, certificate diagnostics, secret-entry UI, metrics
collection, member management, bootstrap enrollment, node drain, and backup
status are not implemented in this dashboard milestone. The interface does not
present these controls as working. The API also rejects secret-reference bindings
in this milestone. See the repository milestone tracker.

## Authentication and limits

The browser submits its existing key once to `/session`. Start verifies it at Go
`/me`, then seals it using AES-256-GCM in a 12-hour HttpOnly, SameSite=Strict,
host-only cookie. HTTPS cookies use the `__Host-` prefix and Secure attribute.
Mutations require an exact trusted `Origin`. The presentation server has no
session database or growing session cache. Changing the session encryption secret
invalidates browser cookies. Go still validates key revocation and permissions
on every API call. Logout clears this browser's cookie; revoke the API key to
invalidate copied credentials.

Only named platform API paths are proxied. The upstream URL comes from operator
configuration, never a browser field. Request bodies are bounded to 1 MiB
(4 KiB for sign-in), API requests have a 30-second timeout, and log connections
have a five-minute timeout. No credential values or request bodies are logged
by the dashboard. Standard server access logs include method, path, status,
and duration.

The production Node heap has a 192 MiB old-space cap. This is **not** an RSS limit;
the process also uses native memory. Pages are split into route chunks. There is
no charting, code-editor, topology, or animation runtime. Application lists and full application/deployment data are released when their
last observer leaves. Smaller metadata queries become collectable after 60
seconds. Application pages contain at most 25 items and follow the Go API’s
512 KiB payload budget; previous-page cursors are bounded to 20. History contains
metadata only. The preceding full revision is fetched separately for a diff. Overview refreshes every 15 seconds, application detail every 10 seconds,
active deployments every 2.5 seconds, and nodes every 30 seconds; background
polling is disabled. Live logs keep at most 1,000 lines / 256 KiB, update at most
four times per second, stop in hidden tabs, and retry ended streams at most three
times. Configuration diffs render at most 50 rows per page without hiding changes.

## Schema and verification

```sh
pnpm generate:api  # ../api/openapi.json → src/lib/api.generated.ts
pnpm typecheck
pnpm test         # session security, body/TLS bounds, non-submitting key Copy
pnpm build
pnpm smoke --public-only # SSR/security edges; no Go authentication required

# With production dashboard and Go API running; key is read without being printed.
HAKOPOD_WEB_URL=http://127.0.0.1:4173 \
HAKOPOD_API_KEY_FILE=../.local/admin-key pnpm smoke
```

`src/lib/client.ts` binds openapi-fetch to the generated operation, path, request,
and response types. Dashboard queries and mutations use that client. Generated
files are committed so schema drift can be reviewed.

The HTTP smoke test checks real authentication, unauthorized access, encrypted
cookies, CSRF rejection, endpoint allowlisting, the body limit, static assets,
SSR, and logout. It does not claim Kubernetes rollout correctness; that belongs
to the repository's real-cluster acceptance suite. Browser verification and
measurements are recorded in [VERIFICATION.md](./VERIFICATION.md).

## Upstream decisions (checked 2026-09-12)

- [TanStack Start build-from-scratch](https://tanstack.com/start/latest/docs/framework/react/build-from-scratch),
  [server routes](https://tanstack.com/start/latest/docs/framework/react/guide/server-routes),
  and [hosting](https://tanstack.com/start/latest/docs/framework/react/guide/hosting)
  were checked against current upstream sources. Start 1.168.52 and its matching
  Router 1.170.35 require a modern Node/Vite toolchain. Current Vite 8.3.0 and
  React plugin 6.1.1 build successfully on Node 22.13.1. TypeScript is pinned to
  5.9.3 because openapi-typescript 7.13.0 declares a TypeScript 5 peer requirement.
- Current Start supports a fetch-style server entry. A small srvx 1.0.4 adapter
  serves that entry and static assets, avoiding an additional deployment framework.
  The current Nitro distribution was a beta; it was unnecessary for this Node
  target. The package lock pins all resolved dependencies.
- [Railway staged changes](https://docs.railway.com/deployments/staged-changes)
  informed the local configuration → explicit diff → apply workflow.
  [Render deploys](https://render.com/docs/deploys) informed release history,
  readable failure detail, and readiness-oriented language. Hakopod's logo,
  illustrations, tokens, and screen composition are original; no brand assets
  were copied.
- Radix accessible primitives and locally owned shadcn-style components are
  adapted to Hakopod's tokens. Dependency licenses and the shadcn MIT notice are
  listed in [THIRD_PARTY_NOTICES.md](./THIRD_PARTY_NOTICES.md). System fonts avoid
  downloading a font package or requesting an external font service.
