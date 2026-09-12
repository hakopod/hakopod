# Hakopod

Your apps. Your rules. Deploy applications on infrastructure you own. Hakopod combines
a Go API and reconciler, a small CLI, PostgreSQL durable state, and a TanStack
Start dashboard over K3s and HAProxy.

This repository includes the deployment engine and an expanded cockpit with
human accounts, paid team roles, source builds, templates, live service monitoring,
searchable logs, container and host terminals, custom domains, encrypted backups,
registry credentials, TLS controls and worker enrollment. See the
[cockpit guide](docs/cockpit.md) and [verification and remaining gates](docs/milestones.md).
It remains a development release; production installation and operational recovery
have separate acceptance gates.
The intended project name/domain is Hakopod / hakopod.com; no domain ownership
or public infrastructure is assumed.

## Run locally

Requires Docker, Go 1.26.8, Python 3, kubectl, Helm 3, Node >=22.12 and pnpm.
The development script pins and verifies its local k3d download. It creates an
isolated `hakopod-dev` cluster without changing your Kubernetes default context.

```sh
python3 scripts/ui-source.py restore  # public component sources; no private repo access
./scripts/local-up.sh
make build
make api                       # leave running in this terminal
```

In another terminal:

```sh
pnpm --dir web install --frozen-lockfile
make dashboard
```

Open [the dashboard](http://127.0.0.1:4173). On first installation, choose your
administrator name, email and password. The installer proof is saved in the
restricted `.local/setup-secret` file; no default human account is created.
Fresh installations then queue a labelled, removable shop sample. Existing
accounts and applications are preserved when the server restarts or upgrades.
The local API is on port 8080; application HTTP ingress is
on port 18080. Local HTTP is explicitly a loopback development configuration.
Dashboard production startup, HTTPS origin and session-secret settings are
documented in [web/README.md](web/README.md).

Deploy the real public web/private API example using the same Go API:

```sh
source .local/env
export HAKOPOD_API_URL=http://127.0.0.1:8080
bin/hakopod login --project demo --environment development
bin/hakopod validate --file examples/shop/hakopod.toml
bin/hakopod plan --file examples/shop/hakopod.toml --project demo --environment development
bin/hakopod deploy --file examples/shop/hakopod.toml --project demo --environment development --wait
```

For CI, create a named project/environment-scoped key through the dashboard or
`hakopod key-create`; store it in the CI secret store as `HAKOPOD_API_KEY` with
`HAKOPOD_API_URL`. CI needs no browser session or Kubernetes credentials.
The `bootstrap` command remains an explicit machine-credential recovery/development
tool. Dashboard sign-in uses human accounts.

## Install on a Linux server

The [interactive installer](installer/README.md) supports Linux amd64 and arm64.
It asks for your domains, node address, dashboard access, certificates, storage
and resource limits, then shows the plan before making changes. The first person
to complete setup chooses the administrator account.

```sh
python3 release/build.py
python3 release/build-installer.py
sudo bash scripts/install.sh --artifact-dir .local/installer-artifacts/0.1.0-dev
```

Run the installation step on a dedicated supported Linux host. Use `--dry-run`
to review configuration and artifact checksums, or a reviewed `--config` for
unattended input. No public download service is assumed; build or transfer the
local release artifacts first. Read the installer guide for Linux prerequisites,
SSH/HTTPS access, DNS, firewall rules, resume and the host-verification boundary.

## Accounts, licensing and components

Deployment and operational tools stay available in Free. Team creation,
invitations and project role management require signed Pro entitlements. The
dashboard shows the feature catalog and activation state; the Go API and durable
workers enforce it. Expiry preserves accounts, application data and running
workloads. See [paid features](docs/paid-features.md) for the exact behavior.

The [design system](packages/ui/README.md) is a separate shadcn/Radix component
repository at `packages/ui`. The public source bundle in `third_party/ui/` makes ordinary
builds independent of submodule hosting. The license issuer is a separate private
submodule and is excluded from public release artifacts. Current submodule
origins are local bare repositories; hosted remotes have not been created.
Public contributors should restore the UI bundle, without recursively fetching
the private issuer. See [submodule development](docs/submodules.md).

The [logs and terminals guide](docs/observability.md) explains SQL-style filters,
live resource observations, terminal permissions and bounds, and GitLab source sync.
The [template catalog](docs/templates.md) records each preset's credentials,
storage, memory and verification limits. [Backups](docs/backups.md) explains
S3-compatible destinations, schedules, recovery keys and restores into fresh
database names. A management backup covers the logical database, not the whole
host or Kubernetes cluster.

## One application, several services

```toml
schema_version = 1
name = "shop"

[services.web]
image = "ghcr.io/your-org/web:1.0.0"
port = 3000
public = true

[services.web.env]
API_URL = "http://api:8080"

[services.api]
image = "ghcr.io/your-org/api:1.0.0"
port = 8080
```

This illustrative configuration uses placeholder images. The runnable
[shop example](examples/shop/README.md) uses public multi-architecture images.
Services share a private network by default; only `public=true` creates an
Ingress. Browser code calls the public web server, which proxies to the private
API. A background worker can omit its port entirely.

The Go API owns strict TOML validation, staged diff, expected-revision checks,
digest resolution and durable deployment execution. Accepted work survives
client disconnection and management restarts. Rolling updates wait for the
new pod template to be ready, and failed groups attempt recovery to the last
healthy release. Explicit rollback creates a new auditable revision.

## Keep it small

The default profile runs no Redis, retained logging stack, Prometheus or external
secret service. Deployment concurrency is two in one Go process with a 192 MiB
soft memory target; streams, API pages, query caches and connection pools are bounded.
The same process runs one backup or restore at a time with a reusable 8 MiB upload
buffer. Optional agent and model workloads run only when deployed; browsing their
templates does not start them or download their images.
Application pages use a 512 KiB specification/observation budget; deployment
history is metadata-only, with full revisions fetched on demand.

Local limits are 2304 MiB for the K3s node including its pods, 256 MiB for
PostgreSQL, and 64 MiB for the forwarding helper. An idle pre-application
measurement was roughly 717 MiB across those three containers. This is an
observed development snapshot, not a minimum hardware or load guarantee.
Allow at least 4 GiB for Docker plus capacity for applications and rollout surge.
Stop development infrastructure without deleting data using
`./scripts/local-down.sh`; stop API/dashboard terminals separately.

## Verify and extend

```sh
make check
source .local/env
HAKOPOD_TEST_DATABASE_URL="$HAKOPOD_DATABASE_URL" go test ./internal/api ./internal/store
python3 scripts/acceptance.py
```

See the [local guide](docs/local-development.md), [architecture](docs/architecture.md),
[threat model](docs/threat-model.md), [TOML reference](docs/toml.md),
[OpenAPI contract](api/openapi.json), and [contributor instructions](CONTRIBUTING.md).
The project uses Apache-2.0 for original code; dependencies retain their own
[licenses and notices](docs/licenses.md).
