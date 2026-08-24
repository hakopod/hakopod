# Hakopod dashboard

TanStack Start presentation layer for the Go management API. Go owns authorization,
validation, plans, deployments, and reconciliation. The dashboard never connects to
Kubernetes or PostgreSQL.

## Run

Use Node.js 22.12 or newer and pnpm 10.7.1:

```sh
python3 scripts/ui-source.py restore # from the repository root; restores the pinned UI package if absent
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
`scripts/serve.mjs` resolves the static directory from its own location and uses
no per-request logger, so OAuth callback query credentials are not access-logged.
Set `HOST=0.0.0.0` only when access is intentionally provided through the
installation's firewall and TLS reverse proxy. Keep the dashboard origin separate
from deployed applications.

On first run, choose an owner name, email and password, and supply the installer
credential (or use an existing sealed bootstrap administrator session). Once an
owner exists, use human email/password, a registered passkey, or a configured
GitHub, GitLab, or Google provider. API keys are reserved for CLI/CI workflows and are managed
under Administration. The dashboard never fabricates a first owner.

## Implemented workflows

- Project/environment selection and project creation for administrators.
- Real application overview, grouped services, desired versus observed state,
  deployment history, public URLs, private addresses, and network membership.
- Compact application and node table/card views with selectable inspectors;
  native SVG topology displays only declared service readiness dependencies.
  The command palette searches up to 50 real applications in the current scope.
- Image form and strict TOML import → Go plan → paginated before/after diff →
  revision-checked, idempotent deployment submission. Resource profiles are
  displayed from the plan; staged edits have no infrastructure effect.
- Deployment timeline, explicit failure explanations, resolved image digests,
  per-service results, safe-boundary cancellation, and auditable rollback from
  a previous successful revision.
- SQL-like log queries over recent Kubernetes logs, with actual matching entries,
  structured fields, sampled-pod counts, a histogram, and JSONL export. Separate
  live tail has bounded reconnection, hidden-tab pause, and an ephemeral-log notice.
- Pod terminals with real shell, PostgreSQL, Redis/Valkey, MySQL, MongoDB, and
  custom-command choices. Database clients must exist in the selected image;
  their normal authentication still applies. The terminal engine loads on Connect.
- Real Kubernetes node readiness, architecture, pod count, and allocatable CPU
  and memory. Allocatable values are clearly distinguished from resource usage.
- Administrator key creation, metadata, expiration, last-use, revoke, rotation
  with bounded overlap, and recent audit metadata. Created keys are shown once.
- Keyboard-accessible Radix dialogs, tabs, menus; dark/light themes; responsive
  navigation; deliberate loading, empty, permission, and error states.
- Dedicated nested pages for application, build, source, provider, registry,
  template, backup destination, and schedule forms. Breadcrumbs retain context;
  concise help cards explain the decisions. Failed requests preserve form values.
- Administrator Git repository import chooses the provider, branch, and TOML
  path, then reviews a pinned commit before creating its mapping and release.
  Opaque review tokens remain in local component state and expire after 15 minutes.
- Searchable database, model, and agent catalogs show real architecture,
  resource, license, secret, and verification metadata. Original provider and
  service SVGs are served locally. Template planning supports model/provider
  choices where the application supports them; guide-only entries do not deploy.
- Reviewed custom domains: create ownership proof, verify DNS, review the normal
  application diff, and deploy. A configured mapping is distinct from working
  DNS, deployment health, and certificate readiness.
- Encrypted database backups with object-storage destinations, connection tests,
  one-time recovery-key display, schedules, paginated jobs and artifacts, job
  cancellation, and explicit artifact deletion. Restore reviews an expiring plan
  with the pinned target and requires its exact fresh database name; it never
  overwrites an existing database.
- Profile editing with initials or optional DiceBear identicon/glass avatars and
  revision checks. Avatar requests use opaque seeds, omit credentials and
  referrers, and fall back to initials. Licensed teams support scoped usernames.
- Linux host terminals for the super admin or people with explicit, expiring
  node authority. Installation administrator status alone does not grant a
  root shell. The host-access page manages real grants; the terminal reuses the
  bounded, lazy-loaded engine and closes abandoned asynchronous connections.
- A fresh-install sample banner reads the server's tracked sample state. Removal
  is reviewed against the sample marker and exact application revision; claimed
  installations are not retrofitted with a sample by the dashboard.

Additional connected interfaces include per-service runtime metrics/pods/events,
service-scoped staging and restart, canonical TOML editing, installation accent,
human account security/sessions, licensed teams/project roles/invitations, CLI consent,
write-only application secrets, template planning, GitHub/GitLab TOML source review,
private registries and reviewed HAProxy settings. TLS supports PEM upload and
cert-manager issuer selection with observed readiness. Node views include actual
usage/GPU capacity, guarded cordon/drain and expiring K3s enrollment credentials.
Source builds support new applications without a pre-existing image, Dockerfile
or buildpack workflows, native architecture selection, explicit administrator
workflow installation in GitHub Actions or GitLab CI, build/cancel/status/log links, and a canonical
server-produced deployment diff. Automatic build/deploy is an explicit setting.
GitLab manages one `.gitlab-ci.yml` per repository, refuses an unowned/custom
entrypoint, and needs Pipeline events for recording automatic builds and deployments.
License settings display the server's installation ID and signed-license status.
Paid controls follow the real feature catalog; safe team removal remains available
without an active Pro license. Go enforces these permissions independently.
Optional panels load on demand.
Implementation and current verification are recorded separately in VERIFICATION.md.

The backup interface covers PostgreSQL and MySQL database exports and the
management database. Full host recovery also requires the Kubernetes datastore,
K3s bootstrap material, encryption keys, and installation configuration described
in the repository's recovery documentation. A database artifact alone is not a
complete cluster backup.

## Shared design system

The dashboard consumes `@hakopod/ui` from the independent `packages/ui` Git
submodule. Its shadcn-based Radix components, semantic tokens, brand mark, and
font are shared package assets. The supplied `brand-kit` remains intact. The
dashboard bundles this source package into production JavaScript; Node does not
load raw TSX at runtime. See [submodule setup](../docs/submodules.md) for the
pinned portable source restore path and local repository arrangement.
Individual service/provider icons use original project SVGs or CC0 Simple Icons,
with sources and upstream notices under `public/icons`. No icon runtime package
or remote icon request is needed. DiceBear is contacted only for a selected
generated avatar style; initials render entirely locally.

## Authentication and limits

The browser sends sign-in credentials to `/session`. Go validates them and issues
an opaque human session. Start strips that token from browser JSON and seals it
using AES-256-GCM in a 12-hour HttpOnly, SameSite=Strict, host-only cookie. HTTPS
cookies use the `__Host-` prefix and Secure attribute. OAuth state is restricted
to a five-minute HttpOnly/Lax host cookie; pending provider MFA is sealed in a
five-minute HttpOnly/Strict cookie. Mutations require an exact trusted `Origin`.
The presentation server has no session database or growing session cache.
Changing the encryption secret invalidates browser cookies. Go validates session
revocation and current roles on every API call; logout revokes the Go session.
Passkeys use native WebAuthn without an additional browser library. Passwords,
new credentials, MFA setup secrets and recovery codes are not stored in query caches.

Only named platform API paths are proxied. The exact public POST
`/api/v1/webhooks/github` and `/api/v1/webhooks/gitlab` have separate webhook
bridges: raw request bytes are limited to 512 KiB, only the provider's
authentication/event/delivery headers are copied, and no browser cookie or bearer
credential is forwarded. Go verifies GitHub HMAC or the GitLab webhook token.
Other dashboard mutations keep their same-origin requirement. The generic BFF
is not a public CLI bearer gateway; direct CLI API requests use the Go API origin.

The upstream URL comes from operator
configuration, never a browser field. Request bodies are bounded to 1 MiB
(4 KiB for sign-in), API requests have a 30-second timeout, and log connections
have a five-minute timeout. Terminal output streams have an eleven-minute BFF
timeout around the server's ten-minute session lifetime. The request-body reader
starts with at most 4 KiB and grows only as data arrives. No credential values,
request bodies, or per-request access logs are emitted by the dashboard adapter.

The production Node heap has a 192 MiB old-space cap. This is **not** an RSS limit;
the process also uses native memory. Pages are split into route chunks. There is
no charting, code-editor, topology, or animation runtime. The xterm terminal engine
and fit addon are dynamically imported only after Connect. Application lists and full application/deployment data are released when their
last observer leaves. Smaller metadata queries become collectable after 60
seconds. Application pages contain at most 25 items and follow the Go API’s
512 KiB payload budget; previous-page cursors are bounded to 20. History contains
metadata only. The preceding full revision is fetched separately for a diff. Overview refreshes every 15 seconds, application detail every 10 seconds,
active deployments every 2.5 seconds, and nodes every 30 seconds. Per-service
charts retain at most 24 actual samples. Source build lists are capped at 100,
run history at 20, and only the selected run polls provider observation every ten
seconds while unfinished. No dummy usage samples are generated; background
polling is disabled. Live logs keep at most 1,000 lines / 256 KiB, update at most
four times per second, stop in hidden tabs, and retry ended streams at most three
times. Configuration diffs render at most 50 rows per page without hiding changes.
Log queries retain at most 1,000 matched entries and sample at most 2,000 lines
per selected pod; changing or leaving the query releases its cache. Terminal
scrollback is 500 lines, queued input is bounded to 16 KiB, input/output blocks
are at most 4 KiB, and incomplete SSE frames are bounded to 16 KiB. Output waits
for xterm rendering before reading the next block. Leaving the panel closes its
session. Go also limits terminal concurrency to four sessions, with a two-minute
idle timeout. These are explicit bounds, not a measured peak-memory guarantee.
Backup history keeps one page of at most 100 records and 20 prior cursor strings;
destination, schedule, and discovered-target lists are bounded to 32, 64, and 128.
Backup query data is collectable immediately after its last observer leaves.
Only a selected unfinished job polls, every 2.5 seconds. Profile data and host
grants contain metadata; recovery keys, backup credentials, and source review
tokens never enter persistent browser storage or query caches.

## Schema and verification

```sh
pnpm generate:api  # ../api/openapi.json → src/lib/api.generated.ts
pnpm typecheck
pnpm test         # human-session boundary, body/TLS bounds, UI regressions
pnpm format:check
pnpm build
pnpm smoke --public-only # SSR/security edges; no Go authentication required

# With production dashboard and Go API running; use an authorized existing account.
# The protected JSON file contains email, password and optional current MFA code.
HAKOPOD_WEB_URL=http://127.0.0.1:4173 \
HAKOPOD_SMOKE_LOGIN_FILE=/path/to/protected-human-login.json pnpm smoke
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
  readable failure detail, and readiness-oriented language. The compact cockpit
  follows the supplied visual references and Hakopod brand kit, using its
  outlined logo, Ink/Paper/Ion/Forest/Fog tokens, and Space Grotesk font.
- Radix accessible primitives and shared shadcn-based components are adapted to
  Hakopod's tokens. Dependency licenses and notices are listed in
  [THIRD_PARTY_NOTICES.md](./THIRD_PARTY_NOTICES.md) and the UI package. The brand's
  variable font is self-hosted; no external font service is contacted.
