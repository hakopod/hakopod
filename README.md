# Hakopod

Deploy containerized applications on infrastructure you own. Hakopod combines
a Go API and reconciler, a small CLI, PostgreSQL durable state, and a TanStack
Start dashboard over K3s and HAProxy.

This repository implements the first deployment milestone, with additional
named-network and HPA functionality. It is an early development release,
not production-ready software. See [verification and remaining gates](docs/milestones.md).
The intended project name/domain is Hakopod / hakopod.com; no domain ownership
or public infrastructure is assumed.

## Run locally

Requires Docker, Go 1.26.8, Python 3, kubectl, Helm 3, Node >=22.12 and pnpm.
The development script pins and verifies its local k3d download. It creates an
isolated `hakopod-dev` cluster without changing your Kubernetes default context.

```sh
./scripts/local-up.sh
make build
make bootstrap                 # once; saves the administrator key with mode 0600
make api                       # leave running in this terminal
```

In another terminal:

```sh
pnpm --dir web install --frozen-lockfile
make dashboard
```

Open [the dashboard](http://127.0.0.1:3001). Sign in with the bootstrap key from
`.local/admin-key`. The local API is on port 8080; application HTTP ingress is
on port 18080. Local HTTP is explicitly a loopback development configuration.
Dashboard production startup, HTTPS origin and session-secret settings are
documented in [web/README.md](web/README.md).

Deploy the real public web/private API example using the same Go API:

```sh
source .local/env
export HAKOPOD_API_URL=http://127.0.0.1:8080
export HAKOPOD_API_KEY="$(cat .local/admin-key)"
bin/hakopod validate --file examples/shop/hakopod.toml
bin/hakopod plan --file examples/shop/hakopod.toml --project demo --environment development
bin/hakopod deploy --file examples/shop/hakopod.toml --project demo --environment development --wait
```

For CI, create a named project/environment-scoped key through the dashboard or
`hakopod key-create`; store it in the CI secret store as `HAKOPOD_API_KEY` with
`HAKOPOD_API_URL`. CI needs no browser session or Kubernetes credentials.

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
secret service. Two workers share one Go process with a 192 MiB soft memory
target; streams, API pages, query caches and connection pools are bounded.
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
