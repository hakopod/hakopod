# Deployment templates and sample shop

Templates produce the same versioned application specification, review, optimistic revision check, scoped secret references, durable queue and reconciler as hand-written TOML. They do not start a second orchestrator, builder, database, or model runner in the management process. Optional application resources are charged to the application pods.

The shared catalog is maintained in [hakopod/templates](https://github.com/hakopod/templates), pinned at `templates/` in both the engine and website. Native TOML, metadata, requirements and logos have one source. The binary embeds the catalog at build time and never fetches or executes remote template definitions at runtime.

The catalog has 71 entries: 35 deployment presets and 36 migration guides. All 58 requested Dokploy blueprints are recorded, with the existing Valkey preset retained. A migration guide is not deployment support. The engine now has [deployment jobs, file mounts, connection bindings and named HTTP endpoints](application-lifecycle.md), but candidates still need conversion and live verification before promotion. Privileged/root initialization, public UDP, image compatibility and application-specific bootstrap behavior remain separate prerequisites. The deploy API rejects these guides. Consult each entry’s `README.md` and `migration.json` for exact coverage.

All enabled presets use immutable image digests. `templates/images.lock.json` records registry checks, while each entry’s `verification` distinguishes these from actual runtime acceptance. The original 13 deployment specifications are preserved exactly. New preset live acceptance is pending. BentoPDF was blocked by Docker Hub rate limiting before workload creation. Browserless reached image creation but filled the small development disk and timed out. Its namespace, credential and exclusive cached blobs were removed; all existing deployments returned ready. Reserve at least 8 GiB free disk before testing that browser image.

The form exposes ordinary configuration separately from scoped secrets and retains inputs after a failed request. Required configuration is checked before review and again by the API. Never substitute secret values into TOML.

## Original presets

Memory values below are Kubernetes request / limit. These are small starting configurations, not production capacity claims. Every persistent service has one replica and uses a PVC with a Recreate update strategy. Increasing replicas does not turn a single-writer database into a cluster.

| Template | Services and memory | Credentials and limitations |
| --- | --- | --- |
| PostgreSQL 17.11 | Database: 308 / 615 MiB | Private 5432; `database-password`; 64 MiB shared buffers, 50 connections. |
| Valkey 9.1.2 | Database: 154 / 308 MiB | Private 6379; `database-password`; AOF, 64 MiB maxmemory, no eviction. |
| Redis 8.6.6 | Database: 154 / 308 MiB | Private 6379; `database-password`; AOF, 64 MiB maxmemory, no eviction. Review upstream AGPL/RSAL/SSPL options. |
| MySQL 8.4.11 | Database: 308 / 615 MiB | Private 3306; separate `database-password` and `database-root-password`; 32 MiB InnoDB buffer, 12 connections, Performance Schema disabled. |
| CockroachDB 25.4.16 | Database: 615 / 1229 MiB | Private TLS SQL 26257; operator CA and node certificates; 128 MiB cache and SQL budgets. Secure single node, no HA. Review CockroachDB Software License eligibility. |
| ClickHouse 26.3.33.24 | Database: 615 / 1229 MiB | Private HTTP 8123; `database-password`; 256 MiB query and 640 MiB server memory budgets; bounded background pools. Native TCP is not exposed. |
| Metabase 0.63.17 | Workspace: 2458 / 4916 MiB; PostgreSQL: 308 / 615 MiB | `database-password`, `credential-encryption-key`; 1 GiB Java heap. PostgreSQL stores application metadata, dashboards and users. AGPL open-source image. |
| Infisical 0.165.10 | Workspace: 2458 / 4916 MiB; PostgreSQL: 308 / 615 MiB; Redis: 154 / 308 MiB | See connection and encryption requirements below. MIT core; enterprise features have separate terms. |
| Open WebUI 0.11.3 slim | Workspace: 2458 / 4916 MiB | `provider-key`, `session-secret`; select provider and model. No local Ollama service or embedding weight download. Preserve upstream branding under its license. |
| Flowise 3.1.4 | Agent workspace: 2458 / 4916 MiB | Five explicit application secrets; 1 GiB Node heap. Choose provider, model and credentials in the installed workspace. |
| Uptime Kuma 2.5.4 | Workspace: 308 / 615 MiB | Complete administrator setup before sharing. Persistent data and 256 MiB Node heap. |
| Gitea 1.27.3 rootless | Workspace: 308 / 615 MiB | Complete administrator setup before sharing. Persistent SQLite and app.ini; HTTP Git only. |
| vLLM 0.29.0 | Runtime: 8 / 16 GiB RAM, one NVIDIA GPU | `inference-api-key`; explicit Hugging Face owner/model and immutable revision. GPU VRAM is additional and depends on model. |
| xem.email | Guided prerequisite entry | Official frontend embeds its API origin during image build. See [the Xem deployment review](templates-xem.md); a blind runtime-only template is intentionally unavailable. |

Database templates force private exposure even if the plan request asks for public access. Their readiness gates are TCP for PostgreSQL, Redis/Valkey, MySQL and CockroachDB, and `/ping` for ClickHouse. These gates show a listening process; they do not establish recovery guarantees, replication, backup correctness, or production sizing. Set up backups before placing important data in a database. Redis/Valkey's no-eviction policy returns write failures when its explicit data budget is full.

A `storage_gib` setting applies to each persistent service, accepts 1–200 GiB and defaults to 5 GiB. vLLM's default model cache is 30 GiB. Storage-class availability and free capacity are operator prerequisites; a pending PVC is reported as real deployment state. Secrets are provided through the application's scoped secret references, never literal password fields in a template plan or deployment history.

PostgreSQL and MySQL expose initial database and user names. They default to `app` and `hakopod`; changing initialization variables does not change an existing database. PostgreSQL's initial user has administrator rights. MySQL uses separate application and root passwords. Create narrower database accounts inside the database when needed.

During review, generate or supply each required credential. Browser generation uses `crypto.getRandomValues`; copying a generated value is available before saving. The write-only secret API validates formats, and generation through the API uses Go's cryptographic random source. Existing references are preserved unless explicitly replaced. Infisical connection URLs can be built directly from the saved password references without returning those passwords to the browser. Provider keys and certificate material must come from their actual provider or issuer.

The template deployment endpoint parses the reviewed TOML, compares it with the current template and chosen options, then reads the scoped secret references and checks their formats and relationships before durable acceptance. A changed catalog or TOML requires another review. Failed validation queues no deployment. These checks apply to the guided template endpoint; ordinary TOML deployment remains available for custom specifications. Secret values remain mutable references and are not revision-pinned by this check.

## Application setup

Infisical requires `encryption-key` (32 hexadecimal characters), `auth-secret`, `database-password`, `database-url`, `redis-password` and `redis-url`. The database URL must point to `db:5432/app` with the `hakopod` user and the same database password; the Redis URL must point to `redis:6379` with the matching Redis password. URL-encode special password characters. Preserve the encryption key with the database backup. This preset installs the Infisical server; it does not configure Hakopod's separate optional Infisical operator integration.

Infisical and Flowise require a stable HTTPS `site_url` origin. After the application exists, open Custom domains, verify that hostname, and apply the route and TLS before using login or callbacks. Domain proof belongs to the real application ID, so template planning does not claim an unverified hostname. An uploaded certificate must cover the generated and custom names used by the service, or use the configured managed issuer.

Gitea, Metabase and Open WebUI also accept an optional HTTPS site origin. It sets `ROOT_URL`, `MB_SITE_URL` or `WEBUI_URL` respectively. It configures generated links and callbacks; it does not create a DNS record, claim a hostname or install a certificate. Metabase's generated credential encryption key is for a new application database; an existing Metabase requires upstream's migration or key-rotation procedure.

Open WebUI accepts `provider=openai` with the normal OpenAI endpoint, or `provider=openai-compatible` with an explicit HTTPS `provider_url`. The model is an external provider model ID, not a Hugging Face download. Inference and enabled provider features use the operator's provider account. The first administrator must complete setup before sharing the URL; later users start pending. Optional document/audio features need matching upstream provider configuration and capability. The slim image is still roughly 1.4 GB compressed, so it is optional and is never pulled by Hakopod merely for browsing the catalog.

Flowise installs the real upstream visual agent/workflow runtime. It requires `credential-encryption-key`, `session-secret`, `token-hash-secret`, `token-refresh-secret`, and `token-signing-secret`. Create the administrator, then select model/provider nodes, save provider credentials in Flowise, and create or import an agent flow. Protect a published prediction API with an API key. No ready-made autonomous flow, provider balance or model weights are assumed. Its image is roughly 1.5 GB compressed; tools run inside the restricted application container.

CockroachDB requires `database-ca`, `database-node-cert` and `database-node-key` PEM secrets. The node certificate must identify the `node` principal and cover `main` and `localhost` (plus any client-facing internal DNS names you use). The process writes them under a private `/tmp` directory and starts with TLS enabled; the admin HTTP listener is loopback-only. Keep the CA private key outside the application. Use a client certificate to initialize SQL users and database grants. No plaintext/insecure startup option is offered.

vLLM has an authenticated API and does not enable remote repository code. Public Hugging Face metadata may be resolved to an immutable model revision during planning. For a private or gated model, enable the model-token option, provide its immutable revision and save an approved Hugging Face read token as `model-token`; the workload receives it as `HF_TOKEN`. Its AMD64 and ARM64 images require a compatible NVIDIA GPU, CUDA 13 driver and device plugin. ARM64 refers to NVIDIA SBSA hardware, not an ordinary ARM CPU node. GPU workloads receive a private 1 GiB memory-backed `/dev/shm`. The image is about 10 GB compressed and is never downloaded by the control plane for catalog browsing.

## Verification

The development tests run only against `k3d-hakopod-dev`, in explicitly marked disposable fixture namespaces. A catalog run starts one selected application, checks real protocol behavior, restarts it on the same PVC where specified, and removes its namespace, PVC and dynamically provisioned PV before advancing.

- PostgreSQL: real SQL persists across service restart; ordinary workload cleanup retains its PVC.
- Valkey: unauthenticated requests refused and authenticated PING/SET/GET through Service DNS.
- Redis: unauthenticated requests refused; authenticated SET/GET and AOF data survive restart on the same PVC; fixture namespace/PVC/PV cleanup verified.
- MySQL: ARM64 non-root UID 999 startup under a 512 MiB limit, SQL dump, and restore into a newly named database verified by the backup acceptance test; the original database remained unchanged. The backup fixture uses a disposable writable volume, so this does not claim a MySQL PVC restart test.
- Gitea: administrator setup, persistent app.ini lock and administrator API login survive restart on the same PVC.
- Uptime Kuma: real administrator setup page returned over the service endpoint.
- ClickHouse: exact non-root template starts under its 1 GiB limit, rejects unauthenticated HTTP queries, accepts authenticated CREATE/INSERT/SELECT, and retains data across restart on the same PVC; namespace/PVC/PV cleanup verified.
- CockroachDB: exact template starts with operator CA/node certificates, rejects plaintext SQL and TLS SQL without a client credential, accepts root client-certificate SQL, and retains data across restart on the same PVC; namespace/PVC/PV cleanup verified.
- Metabase, Infisical, Open WebUI and Flowise: pinned manifests and upstream startup/configuration reviewed; large application runtime acceptance has not been performed on the small development node.
- vLLM: manifest and CLI configuration checked; no local GPU execution or model download was performed.

The evidence is implemented in `internal/cluster/live_catalog_test.go`, `internal/cluster/live_catalog_expanded_test.go`, `internal/cluster/live_showcase_test.go`, `internal/api/showcase_test.go`, and `internal/api/templates_test.go`. Focused spec/API/store tests passed with the Go race detector, including the source-import regression checks after the shared acceptance change.

Run an individual database fixture with `HAKOPOD_CATALOG_TEST=1`, `HAKOPOD_CATALOG_TEMPLATE=redis` (or another supported fixture ID), and `HAKOPOD_TEST_KUBECONFIG` pointing to the named development kubeconfig. The test rejects any other current context. This is an explicit test opt-in, not production startup behavior.

## Fresh-install sample shop

The server enables a first-owner bootstrap hook that atomically queues the small `demo/development/shop` application only for a fresh owner with no existing applications. Existing owners and legacy-owner conversions are not retrofitted. Test stores default to bootstrap disabled. No migration seeds sample rows.

The shop contains one public Python web service and one private Python catalog API. Each requests 154 MiB with a 308 MiB limit, runs a pinned small Alpine image as a non-root user, and has no database, PVC, builder, payment provider or stored orders. Products, basket and checkout are labelled sample data; the catalog reports the actual serving API pod. The shop's network requests and request bodies are bounded.

`GET /api/v1/showcase` returns the durable bootstrap state and actual application/deployment identifiers and status. It does not fabricate readiness. The server checks one row every ten seconds and submits through the normal durable deployment queue with an idempotency key. Sample acceptance and its tracked application/deployment IDs commit in one PostgreSQL transaction under the same lock used by cancellation. Losing the acceptance connection rolls the entire operation back. The record survives restarts and removal.

Removal requires a browser global administrator and the exact reviewed showcase revision, application ID and application revision. The operation revokes the bootstrap grant, cancels pending deployment work, takes the deployment worker's application lock, and checks namespace ownership and absence of PVCs. A changed application, source integration, foreign namespace or persistent data blocks automatic removal. A later application with the same name gets a different ID and is preserved. Kubernetes deletion uses the namespace UID precondition. Removing or cancelling the sample retains a marker so it cannot be recreated by bootstrap.

Real-cluster acceptance verified storefront HTML, public-web-to-private-API catalog access, the expected sample checkout total, absence of an API ingress/PVC, and full owned namespace deletion. Isolated PostgreSQL tests cover fresh-owner scheduling, restart replay, cancellation before acceptance, revision/identity checks, credential scope, existing-install preservation and a same-name replacement. Regression tests inject a failing marker write and terminate an acceptance database connection while cancellation waits; neither leaves an orphan application. Unit checks also reject foreign namespaces and namespaces with persistent data.
