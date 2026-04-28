# Local development

This path runs real K3s, HAProxy and PostgreSQL on Docker. It is a development environment, not a Linux host installer. The existing Docker context and default Kubernetes context are preserved. Every command uses the named `hakopod-dev` cluster and repository-local kubeconfig.

## Start

Requirements: Docker with at least 4 GiB available, Python 3, curl, kubectl 1.35, Helm 3, Go 1.26.8, pnpm as pinned by `web/package.json`, and Node satisfying that file's engine range. Tested infrastructure host: macOS arm64 with OrbStack, 8 GiB Docker VM. amd64 OCI manifests were checked but an amd64 cluster was not run here.

```sh
scripts/local-up.sh
source .local/env
go run ./cmd/hakopod-server bootstrap --key-file .local/admin-key
go run ./cmd/hakopod-server
```

The startup script checksum-verifies a pinned k3d binary into `.local/bin`, creates an isolated K3s cluster and installs the versioned HAProxy chart. Its Helm repository config is also local. The database has its own labeled container and persistent volume, and startup refuses to reuse an unowned container with the same name. Re-running the script resumes infrastructure and preserves data.

This host blocks upstream UDP DNS from containers, so the local CoreDNS override forwards public queries to `1.1.1.1` and `8.8.8.8` over TCP with 64 concurrent requests maximum. Cluster service discovery remains local. `deploy/local/coredns-custom.yaml` is the operator-editable resolver configuration; use approved resolvers for your network. The K3s-managed Corefile is preserved, and both external and private DNS were tested from a real application pod.

Use a second terminal for the dashboard, after building or installing its dependencies:

```sh
cd web
pnpm install --frozen-lockfile
pnpm dev
```

Open `http://localhost:3000`, with the management API on `http://127.0.0.1:8080`. Configure the API key using the local bootstrap key file. Do not commit or print that key into routine logs. CLI credentials can be supplied by `HAKOPOD_API_URL` and `HAKOPOD_API_KEY`; for local use, read the latter from `.local/admin-key` in the shell. CI should use a scoped machine key, never the bootstrap administrator.

```sh
export HAKOPOD_API_URL=http://127.0.0.1:8080
export HAKOPOD_API_KEY="$(cat .local/admin-key)"
go run ./cmd/hakopod validate --file examples/shop/hakopod.toml
go run ./cmd/hakopod plan --file examples/shop/hakopod.toml --project demo --environment development
go run ./cmd/hakopod deploy --file examples/shop/hakopod.toml --project demo --environment development --wait
```

Create the project/environment first with the dashboard or API if bootstrap has not supplied it. The sample is an actual public web proxy plus private API. See `examples/shop/README.md` for update and failure variants. Default development application hosts end in `127.0.0.1.sslip.io:18080`; if a DNS resolver rejects loopback wildcard responses, use curl's `--resolve` option with the generated hostname.

## Verify

```sh
go test ./...
pnpm --dir web typecheck
pnpm --dir web build
python3 scripts/acceptance.py
python3 scripts/cross-node-acceptance.py
kubectl --kubeconfig .local/kubeconfig get pods -A
kubectl --kubeconfig .local/kubeconfig top pods -A
```

The acceptance script modifies `demo/development/shop`, revokes its temporary scoped key afterwards, and leaves the app on a successful rollback revision. It checks real API authorization, durable request semantics, routing, actual generated network policies, pod replacement, a service image update, failed readiness, explicit rollback and measured traffic errors. Its compact credential-free report is `.local/acceptance.json`. The optional cross-node script adds a temporary tainted 1 GiB worker, proves the installed CNI's permitted/denied traffic across two nodes, and deletes its fixtures and worker afterwards. This is a containerized-node test, not a physical multi-machine installation test. API restart/accepted-work interruption and browser verification are separate checks; a successful script alone does not prove those cases.

## Resource budget

| Component | Limit / default |
| --- | --- |
| K3s node including its pods | 2304 MiB, max 50 pods |
| k3d TCP forwarding helper | 64 MiB / 0.25 CPU |
| PostgreSQL | 256 MiB / 0.5 CPU, 30 connections, 32 MiB shared buffers, 2 MiB work memory |
| HAProxy, within the K3s budget | 96 MiB request / 256 MiB limit, two threads, 1024 connections |
| Go API/reconciler | `GOMEMLIMIT=192MiB`, `GOMAXPROCS=2` in local environment; Go's memory target is not a hard process limit |
| Default small application service | 128 MiB request / 256 MiB limit; 100m CPU request / 500m limit |

No Redis, Prometheus, logging database, cert-manager or secrets operator runs in the M1 profile. On an idle cluster before application deployment, one measurement was 687 MiB for the K3s node, 12 MiB for its forwarding helper and 18 MiB for PostgreSQL. HAProxy's 104 MiB is included in the node measurement, not additional. This is an observed sample, not a load-test guarantee. Budget spare resources for rolling updates.

Stop infrastructure to reclaim memory while preserving data:

```sh
scripts/local-down.sh
```

Stop the API/dashboard processes in their terminals separately. `scripts/local-up.sh` restarts the preserved infrastructure. Do not delete volumes for routine shutdown. Destructive reset is intentionally separate: after independently backing up required data, an operator can delete only the `hakopod-dev` k3d cluster and `hakopod-postgres` container/volume by name.

## Supported boundaries

The intended first Linux host target is Ubuntu 24.04 LTS on amd64/arm64. There is no tested production installer yet. Containerized K3s does not verify host firewall, NAT, systemd, upgrade or recovery behavior. The current environment does not establish public DNS/TLS, custom domains, private registries, provider outage handling, worker enrollment, physical multi-node/HA topology or backup restoration. These remain explicit milestone gates.

Production recovery must back up PostgreSQL, the K3s datastore, bootstrap token/CA material, encryption keys and configuration outside the host failure domain. A functioning backup command is not evidence of restorability; restoration in a disposable Linux VM is required before an operational-release claim.
