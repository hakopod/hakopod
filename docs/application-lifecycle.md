# Application lifecycle and template compatibility

Hakopod supports deployment jobs, versioned file mounts, private connection
bindings, and additional HTTP endpoints in schema v1. These are ordinary
application configuration, available through TOML, the API and Git imports.
Existing specifications keep their current behavior.

## Jobs and dependencies

```toml
[services.migrate]
image = "registry.example.com/app:1"
command = ["/app/migrate"]
depends_on = ["db"]

[services.migrate.job]
timeout_seconds = 300
retries = 1

[services.web]
image = "registry.example.com/app:1"
port = 8080
depends_on = ["migrate"]
```

The image resolver pins tags before reconciliation. A dependency waits for a
service's readiness or a job's successful completion. Failed jobs prevent
dependent workload updates. Jobs have one completion, a 10–900 second deadline,
and zero to three retries. They use the same resource profiles, restricted pod
security, scoped secrets and network policy as services. Jobs cannot expose
listeners or use service readiness checks, autoscaling or replica counts.

Reconciliation reuses a job for the same revision, including its failure. A new
revision replaces the previous job and runs it again, including rollback releases.
One job is retained per service; its logs are available through the service Logs
view until replacement or removal. Cancellation stops the owned active job;
Kubernetes may take time to terminate its pod. A crash, retry or new revision can
repeat external side effects: migrations must be idempotent. This is not an
exactly-once transaction, and rollback never reverses database migrations.

Changing an existing service into a job (or the reverse) requires a separate
removal deployment first. Persistent volumes are retained. Shared mounts still
require an appropriate ReadWriteMany storage class when multiple workloads use
them; dependency order alone does not make a volume safely shareable.

## Configuration and secret files

```toml
[services.web.files.settings]
mount_path = "/etc/myapp/settings.yaml"
content = "log_level: info\n"

[services.web.files.credentials]
mount_path = "/app/credentials.json"
secret = { ref = "app-credentials" }
mode = 288 # 0440; omission defaults to 292 / 0444
```

Files are read-only single-file mounts. They cannot overlap other mounts or
replace reserved runtime paths. Targeted files under `/etc/myapp` are supported;
arbitrary host bind mounts remain unsupported. At most 16 mounts are allowed per
service, with 64 KiB per configuration body and 128 KiB combined plain content.

Plain content enters versioned configuration history. Sensitive content belongs
in an application-scoped secret or an authorized external-provider reference,
never in `content`. All secret sources are read before deployment writes. File
bodies are omitted from diff summaries. Immutable Kubernetes file snapshots keep
old pods consistent during a rollout; changing a file requires a deployment.
Secret rotation alone does not hot-reload these mounted files. Unused snapshots
are removed during reconciliation, while pods and retained jobs keep their files.

## Connection bindings

```toml
[services.web.bindings.DATABASE_URL]
service = "db"
protocol = "postgres"
username = "app"
database = "app"
password = { ref = "db-password" }

[services.db.secrets]
POSTGRES_PASSWORD = { ref = "db-password" }
```

Both workloads use the same secret. Hakopod builds and URL-escapes the connection
string at deployment time; the resolved password and URL never enter revisions.
Supported protocols are `postgres`, `mysql`, `redis`, and credential-free private
`http`. Bindings use the target's primary service port and must obey network
membership and allowed-peer rules. Add `depends_on` when startup requires readiness.
This does not automatically create database users or change upstream application
settings. Templates can generate native credentials through the existing catalog
credential flow; arbitrary TOML references must already exist.

## Additional HTTP endpoints

```toml
[services.web]
image = "registry.example.com/app:1"
port = 8080
public = true

[[services.web.ports]]
name = "console"
port = 9000
target_port = 9001

[services.web.http.console]
port = 9000
domain = "console.example.com"

[domains]
"console.example.com" = "web"
```

An endpoint selects a declared TCP service port and routes HTTP to its target.
Each gets an automatic hostname; an optional custom domain must be assigned to
the same service and pass the existing ownership-verification flow. Pending
domains remain inactive. TLS configuration applies to all service endpoint hosts;
uploaded certificates must cover them. Up to four additional endpoints are allowed,
with names up to eight characters. Public TCP rules and Cloud restrictions are
unchanged. Configure the upstream application's own public/callback URLs as needed.

## Capacity and storage preflight

Planning and reconciliation inspect ready nodes, taints, architecture, other
workloads' resource requests, aggregate application requests including its largest
job, and available node/image-filesystem disk statistics. No eligible node,
insufficient requests capacity or observed disk below the 2 GiB minimum blocks
deployment with an actionable error. Missing disk observations produce an explicit
warning. This is a minimum download/unpack guard, not image-size estimation or a
scheduling reservation. Large images, rolling updates and persistent data need
additional headroom; concurrent changes and placement constraints can still fail.

Storage classes are checked before deployment. Known local-path and EBS classes
are rejected for ReadWriteMany volumes. Kubernetes does not advertise every CSI
driver's supported access modes: administrators must validate other shared storage
classes. Object-storage adapters remain application-specific.

## Public build-time configuration

Build configurations accept `build_args`, a map of up to 32 public single-line
values. Dockerfile workflows pass them as build arguments; buildpack workflows
pass them as build environment values. For example, set `NEXT_PUBLIC_API_URL` to
the chosen backend origin before building the frontend. Values enter the reviewed
GitHub/GitLab workflow and may enter the image. Secret-like keys, credential URLs
and workflow expressions are rejected. Changing values changes the configuration
revision and requires reviewing/reinstalling the workflow before building.

The existing successful-build deployment flow verifies the current configuration
revision and immutable image and preserves other service digests. Automatically
coordinating several remote builds into one template installation is still separate
work; these arguments do not introduce an atomic multi-build transaction.

## Verification stages

Treat disabled candidates as **draft**, enabled presets as **configuration validated**,
and only a specific tested product/catalog commit and architecture as **runtime tested**.
The current catalog's verification text remains authoritative; enabled does not
mean every upstream application or architecture has been exercised.

The `Template runtime acceptance` workflow runs the lifecycle fixture plus Redis
persistence/restart and Nginx readiness on amd64 and arm64. Its artifacts record
both Git commits, architecture and structured test outcomes. Registry failures are
test failures, not successful compatibility results. Broader template coverage,
upstream upgrade tests and version compatibility matrices must be added before
promoting the remaining complex candidates.


## Verification in this change

Verified locally on 2026-09-14 against the named ARM64 `k3d-hakopod-dev` cluster:

- Real job completion, same-revision reuse, bounded failure blocking a dependent
  Deployment, recovery with a new revision, and cancellation of an active job.
- Read-only configuration and application-scoped secret files inside the job;
  a derived database URL with the same password reference.
- Two actual HAProxy HTTP hostnames reaching different ports of one container.
- A configuration-file change rolling out through a new Deployment generation.
- Job log access and fixture cleanup.
- The existing Redis template rejects unauthenticated access and retains
  authenticated data across restart on the same PVC; fixture namespace, PVC and
  dynamically provisioned PV cleanup passed.
- `go test ./...` with an isolated PostgreSQL test database, `go vet ./...`,
  catalog validation, dashboard build/typecheck/tests and the
  [independent UI review](lifecycle-ui-review.md).

The CI workflow is added but has not run on GitHub yet. AMD64 execution,
provider-hosted builds with the new public arguments, and the broader candidate
catalog are not newly runtime-verified. No production installation, template
promotion or release publication was performed by this change.
