# Hakopod

Your apps. Your rules. Deploy applications on infrastructure you own. Hakopod combines
a Go API and reconciler, a small CLI, PostgreSQL durable state, and a TanStack
Start dashboard over K3s and HAProxy.

This repository includes the deployment engine and an expanded cockpit with
human accounts, team roles, source builds, templates, live service monitoring,
searchable logs, container and host terminals, custom domains, encrypted backups,
registry credentials, TLS controls and worker enrollment. See the
[cockpit guide](docs/cockpit.md) and [verification and remaining gates](docs/milestones.md).
It remains a development release; production installation and operational recovery
have separate acceptance gates.
The website and guides are at [hakopod.com](https://hakopod.com).

## Install on a Linux server

On a fresh dedicated Ubuntu 24.04/26.04 or Debian 12/13 server, with amd64 or
arm64 and at least 4 GiB RAM and 30 GiB free disk:

```sh
curl -fsSL https://hakopod.com/scripts/installer.sh | sudo sh
```

Already root? Use `| sh`. This installs the `0.1.0-alpha.4` prerelease for
evaluation. The script asks for your settings and shows a plan before installing
Hakopod. It installs missing prerequisites, K3s and prebuilt binaries. Choose
managed PostgreSQL or supply a dedicated existing database; external connections
require verified TLS. No Go or frontend compilation runs on the server.

First setup lets you choose the administrator. Public signup stays disabled in
self-hosted binaries; Free teams can enroll explicitly invited members.
See the [installer guide](installer/README.md) for reviewing the script,
requirements and noninteractive configuration, and the
[verification record](docs/prebuilt-installation-verification.md) for tested
behavior and remaining limits.

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

## Build release artifacts

Developers can build the server, dashboard and installer kit locally:

```sh
python3 release/build.py
python3 release/build-installer.py
```

Transfer the resulting kit to a fresh dedicated Linux server and use the
[installer guide](installer/README.md) to review its configuration and checksums.
The public installation command above downloads the verified release instead.

## Cloud deployments

AWS, GCP and Azure provisioning for the planned Bring Your Own Cloud managed
service lives in the private `hakopod/hakopod-cloud` repository. That repository includes this public engine as its `engine`
submodule; the public engine never depends on private Cloud source. It starts with one Ubuntu server and the same installer on
amd64 or arm64. See [cloud deployments](docs/cloud-deployments.md) for scope,
access and the remaining cloud acceptance tests.

Customer-owned BYOC uses the self-hosted feature set. Hakopod Cloud is the
planned shared hosting service for Git deployments, HTTP/HTTPS, custom domains
and private service networking. Public SMTP, SFTP and other custom TCP listeners
stay on self-hosted installations, whether Free or licensed. See
[product modes](docs/product-modes.md) for the enforced boundary and remaining
Cloud launch requirements.

The initial private Cloud beta supplies a control dashboard for one node you
bring. It includes no compute and is limited to allowlisted testers. Its service
lives in the private repository and reuses this engine’s authentication and
PostgreSQL storage primitives. Real hosted deployment acceptance is pending.

## Accounts, licensing and components

Deployment and operational tools stay available in Free. Team creation,
invitations and fixed shared project roles are included in Free. Advanced paid
capabilities require explicit signed entitlements. The
dashboard shows the feature catalog and activation state; the Go API and durable
workers enforce it. Expiry preserves accounts, application data and running
workloads. See [paid features](docs/paid-features.md) for the exact behavior.

Public self-hosted binaries keep signup closed; the installer claims the first
owner and explicit invitations enroll other members. Cloud builds require
managed-cloud mode and `HAKOPOD_SIGNUP_ENABLED=true` to open public registration.
Email verification and password recovery use configured SMTP; GitHub, GitLab and
Google use the configured OAuth clients. See [accounts](docs/accounts.md).

Server startup settings can live in a strict, versioned TOML file selected by
`HAKOPOD_CONFIG_FILE`. Environment values still override it, and credentials stay
in restricted files or the environment. See [operator configuration](docs/operator-configuration.md).

The [Hatch design system](https://github.com/hakopod/hatch-ui) is a separate
shadcn/Radix component repository at `packages/ui`, with its consumer package at
`packages/ui/packages/ui`. The public source bundle in `third_party/ui/` makes
ordinary builds independent of submodule hosting. The license issuer and
commercial cloud toolkit are separate private submodules, excluded from public
release artifacts. Submodule URLs resolve to sibling repositories in the Hakopod
GitHub organization.
The shared [template catalog](https://github.com/hakopod/templates) is a public
submodule at `templates/`. Initialize it with `git submodule update --init templates`
before building. Public contributors should restore the UI bundle without recursively fetching
private repositories. See [submodule development](docs/submodules.md).

The [logs and terminals guide](docs/observability.md) explains SQL-style filters,
live resource observations, terminal permissions and bounds, and GitLab source sync.
[Runtime alarms](docs/alarms.md) covers the dashboard inbox, unhealthy-resource
and recovery notifications, scoped settings, and optional SMTP delivery.
The [template catalog](docs/templates.md) records each preset's credentials,
storage, memory and verification limits. [Application lifecycle](docs/application-lifecycle.md) covers deployment jobs, configuration files, connection bindings, multiple HTTP endpoints and capacity checks. [Backups](docs/backups.md) explains
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

Build and release workflows: [framework setup, MCP, schedules, previews, recovery and build secrets](docs/engine-workflows.md).
