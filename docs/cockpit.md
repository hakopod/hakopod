# Cockpit guide

Hakopod keeps orchestration and authorization in the Go API. The dashboard and
CLI use the same durable deployment engine. Optional integrations add controls
without adding permanent build, logging, cache or model-serving processes.

## First installation and people

The first installer chooses the administrator's name, email and password. There
is no predefined human email or password. A restricted installation credential
proves access to the server; setup closes transactionally after the owner exists.
For local development, `scripts/local-auth.py` creates this proof and a separate
MFA encryption key under `.local`, without creating a user. `local-up.sh` calls it.
Existing bootstrap administrators can migrate through the same setup screen.

Team creation and email invitations require a valid paid license. Team membership and
project grants are separate: project roles are administrator, developer and
viewer. Role reductions, disabled accounts and revoked sessions affect current
API requests and queued deployments. Browser sessions, CLI sessions and CI keys
have separate lifecycles. `hakopod login --project PROJECT --environment ENV`
opens browser consent for the requested scope; CI uses expiring scoped API keys.

Email/password, GitHub OAuth, GitLab OAuth, Google OAuth, discoverable passkeys, TOTP, recovery
codes and session revocation are implemented. OAuth providers appear when their
client credentials are configured. Passkeys require a secure browser origin
(HTTPS, or loopback development). TOTP secrets are encrypted separately from the
database; recovery codes are hashed and consumed once. Never lose the encryption
key when backing up or restoring the installation.

Public signup is available only in Cloud-capable builds with managed-cloud mode
and `HAKOPOD_SIGNUP_ENABLED=true`. Self-hosted binaries keep signup closed while
allowing first-owner setup and explicit invitations. Email registration verifies
the address before creating an account. New accounts can accept an
invitation or create one private personal workspace without paid sharing. The
forgot-password flow sends a single-use email link and keeps two-factor
authentication enabled. See [account setup and recovery](accounts.md).

Configure `HAKOPOD_WEB_ORIGIN` with the dashboard origin. The Go process accepts
`HAKOPOD_SETUP_SECRET_FILE` and `HAKOPOD_AUTH_ENCRYPTION_KEY_FILE`, pointing at
restricted files. Their equivalent non-file variables are supported, but do not
set both forms. The encryption key must encode 32 random bytes as base64 or hex.
The local script preserves existing secrets on repeat runs.

Fresh installations queue a labelled shop sample after the first owner completes
setup. Its public storefront calls a private catalog service. Checkout is sample
data only. The dashboard can remove the tracked sample after a revision review;
removing it does not cause it to return. Existing installations are left alone.

Profile settings offer initials, DiceBear identicons and gradient avatars. Seeds
are opaque IDs or a chosen value, never the person's email. Team usernames are
unique within a team and may differ between teams. A profile change uses a
revision check so an older browser tab cannot silently replace it.

GitHub/Google/GitLab OAuth use `HAKOPOD_GITHUB_CLIENT_ID`,
`HAKOPOD_GITHUB_CLIENT_SECRET`, `HAKOPOD_GOOGLE_CLIENT_ID` and
`HAKOPOD_GOOGLE_CLIENT_SECRET`, `HAKOPOD_GITLAB_CLIENT_ID` and
`HAKOPOD_GITLAB_CLIENT_SECRET`. Register each callback as
`DASHBOARD_ORIGIN/api/v1/auth/oauth/PROVIDER/callback`.
Email delivery requires `HAKOPOD_SMTP_ENABLED=true`, `HAKOPOD_SMTP_ADDRESS`
(`host:port`), `HAKOPOD_SMTP_FROM` and any required username/password. Production
SMTP requires TLS. Delivery is disabled by default; development validation sends
only to an isolated local SMTP fixture.

## Services, configuration and rollback

Service cards open actual pod, container, event, networking, resource and log
details. Metrics come from metrics-server. Missing/stale samples remain missing;
the dashboard retains only a small rolling sample buffer. This is live monitoring,
not a retained historical metrics database.

Applications and services use compact cards with separate copy and action
controls. Nested pages put their parent link before the header logo. Project
administrators can create development, staging and production environments from
the environment menu; each has independent configuration and network scope.

Each service has an environment editor for ordinary variables and secret
references. It reviews an immutable application revision before deployment,
preserves unrelated settings and keeps the draft when a request fails. Concurrent
edits to the same variable require an explicit choice. Ordinary variables appear
in TOML and history; use secret references for credentials.

[Virtual networks](virtual-networks.md) connect selected services across
applications, with environment-scoped segments, application grants, private
ports and explicit peer allowlists. The dashboard and TOML use the same reviewed
configuration and Go authorization checks.

Edit the canonical TOML, review the diff, then deploy using the expected revision.
Restart, manual scale and TLS attachment also create auditable deployment revisions.
Manual scale refuses a service owned by an HPA. Rollback restores the immutable
artifacts of a successful release as a new revision. Unaffected services retain
their resolved artifacts during a service-specific change.

Application-scoped secrets are write-only. Save a reference under its project,
environment and application name, then use it explicitly:

```toml
[services.api.secrets]
DATABASE_URL = { ref = "database-url" }
```

Values are stored in owned Kubernetes Secrets, covered by K3s encryption at rest.
Only named service references are injected. Values never become TOML, deployment
history or secret-list responses. Updating a secret requires a service restart or
deployment to refresh its process environment. Deleting a reference does not erase
a running process's environment; future deployments using it fail.

## GitHub and GitLab source builds

The new-application flow can read a TOML file from GitHub or GitLab before an
application exists. Choose the provider, repository, branch and exact relative
path. Review the commit and normalized configuration, then import. The signed
review lasts 15 minutes and belongs to the current login credential. The first
release, repository binding, scoped automation grant and audit entry commit in
one transaction. Importing makes no repository writes. Initial repository
approval requires a platform administrator; an existing application uses its
Source page. Add custom domains after the application exists so DNS ownership
can be verified against that application.

An administrator configures the GitHub repository token and webhook secret. The
secret is shown once when generated. Subscribe the repository webhook to push
events for TOML-based deployments and workflow-run events for completed source
builds. The webhook must be publicly reachable over HTTPS; loopback development
does not receive GitHub callbacks from the internet.

A TOML binding names `owner/repository`, branch and relative `.toml` path. Plans
resolve an immutable commit; deployment requires both the reviewed source revision
and application revision. A global administrator approves the initial repository
and any repository switch; project deployers can change branch, path and automatic
deployment within that approved repository. Automatic pushes enter a bounded PostgreSQL inbox.
Duplicate deliveries and recovery after an accepted deployment reuse the same
operation. Grants are scoped, expire after 90 days and reload their owning user's
current permissions. Re-saving a binding renews its grant and revokes the old one.

Source builds also work before an application has an image. Choose Dockerfile or
a Cloud Native Buildpacks preset, repository, branch, context and architecture.
Review the generated GitHub Actions workflow before explicitly installing it.
Runs occur on GitHub-hosted runners; there is no always-running local build daemon
or privileged builder in an application namespace. The workflow publishes to GHCR.
Private packages require a matching registry credential in the application scope.

Manual runs pin the source commit. Hakopod verifies the workflow and a bounded
result artifact containing the exact build request, commit and image digest before
allowing deployment. Review the canonical deployment diff. Automatic build/deploy
is optional and uses the same verification and current permission checks. GitHub
credentials, runner availability and any provider usage charges belong to the
operator's GitHub installation.

GitLab.com uses the same staged source and deployment model with GitLab CI and
Container Registry. It supports nested group repository paths, one owned
`.gitlab-ci.yml` per repository, and Pipeline events for automatic build results.
Unowned CI and custom CI entrypoints are refused. See
[GitLab build setup, runner prerequisites and verification](gitlab-builds.md).

## Templates and persistent workloads

The catalog includes database, monitoring, development, analytics, secret-management
and AI presets. See [template requirements and verification](templates.md).
Templates produce ordinary reviewable specifications, with resource limits,
required secret references, upstream/license links and prerequisites.

The setup form identifies required variables and credentials before review.
Generate or save required secrets in the target application scope; only their
references appear in TOML. Read the [upstream requirement review](template-requirements.md)
for initialization behavior, key formats and configuration still needed inside
each application.

Persistent services have one replica, no HPA and a Recreate update strategy. An
owned PVC survives service restarts and ordinary service removal. Data updates
can have downtime. Configuration rollback does not roll back database contents.
Existing PVC size/class changes require an explicit storage migration/expansion;
they are not silently performed by a deployment. Database backups are separate
from configuration rollback.

The optional development storage module is installed with
`python3 scripts/local-storage-up.py`. It is pinned to Rancher local-path-provisioner
v0.0.37, limits its controller to 128 MiB, and operates only on the named development
cluster. It is not part of the default idle profile. Data is local to a worker;
losing that worker or deleting its claim is not an HA storage solution.

vLLM requires a real NVIDIA GPU node/device plugin and adequate VRAM, RAM and disk.
The template resolves public Hugging Face model metadata to an immutable revision;
it never downloads weights into the management process or enables remote model
code. Gated/private models require upstream access and explicit secret configuration.
The model's license is independent from the vLLM engine license. GitHub repositories
with custom model-serving code use the source-build flow. No GPU inference was
verified on the local Mac development cluster.

## Registries, TLS, nodes and appearance

Registry credentials are scoped to project/environment and match an exact registry
host. Credential values remain in Kubernetes Secrets; PostgreSQL records versioned
metadata and audit history. Rotation and synchronization are explicit. A referenced
credential cannot be deleted while a workload still uses it.

Services can use uploaded hostname-matching certificates/private keys or an
operator-configured cert-manager issuer. Uploaded keys are service-scoped and are
not exported. Automatic ACME needs cert-manager, an operator email/domain, correct
DNS and reachable challenge ports. Use staging first; a localhost wildcard cannot
receive a public Let's Encrypt certificate. HTTP and HTTPS local ingress use
18080 and 18443 respectively. Public DNS/ACME renewal remains an external validation
gate; a valid uploaded-certificate fixture is not evidence of ACME issuance.

Custom domains belong to an application and target one of its public HTTP
services. Add the supplied DNS TXT record, verify ownership, then review and
apply the mapping. Configure a CNAME or your ingress A/AAAA records separately.
The installation's own domain is reserved. A hostname cannot be claimed by
another Hakopod application. Removed routes keep their reservation for historical
rollbacks. Certificates must cover the generated hostname and all configured
custom names; a managed issuer requests those names together.

The host terminal opens a root shell on a selected Linux node, including a
control-plane node. This can affect every workload and file on that node. Only
the installer owner has this permission by default; ordinary administrator
authority does not include it. The owner can grant a named node or all nodes to
another person with an expiry of at most 30 days. Grants, account state and node
identity are checked during the connection. Terminals require a browser session,
expire after ten minutes, and close after two minutes without input. At most four
pod or host terminals may be open in the management process. Temporary host jobs
have deadlines and are removed on close. Commands and output are not stored in
the audit log.

The optional [certificate-controller module](../deploy/cert-manager/README.md)
vendors cert-manager v1.21.2 with digest-pinned images and a combined 384 MiB
controller memory limit. It is not installed in the default profile. The dashboard
can create HTTP-01 issuers, defaulting to Let's Encrypt staging; production issuance
must be selected explicitly. Wildcard DNS-01 issuance is not implemented.

Worker enrollment uses expiring K3s bootstrap credentials with the cluster CA hash,
never the permanent server token. Set `HAKOPOD_K3S_SUPERVISOR_URL` to an HTTPS address
reachable by the new worker. The development script sets a Docker-network hostname
for containerized peers; a physical Linux worker needs the installation's real
network address. Credentials can enroll workers until expiry or revocation; they
are not single-use. Revocation takes time to propagate through K3s authentication
caches (about 10 seconds observed locally). Cordon/drain expose resource-version
conflicts and PDB/local-data blockers and preserve unrelated workloads.

The HAProxy editor manages 20 validated settings, including timeouts, connection
limits, backend health checks, load balancing, connection reuse and logging.
Its [field reference](haproxy.md) lists supported values and operating limits.
It preserves other controller settings, reviews the observed Kubernetes
version, stores durable intent and checks administrator authority again before
applying. Stale operator edits cause a conflict. Existing application traffic
does not depend on this worker. Arbitrary HAProxy snippets and public admin sockets
are not exposed by this editor.

The console uses fixed Hakopod brand colors. Each browser can choose the Ink or
Paper theme. Browser panels are loaded as needed; no heavy charting library or
code-editor runtime is required.

## Verification boundaries

Automated PostgreSQL tests cover setup races, sessions, role changes, device consent,
TOTP/recovery, WebAuthn signatures, OAuth/SMTP fixtures, durable source/proxy changes
and deployment idempotency. Actual development-cluster tests cover service metrics,
PostgreSQL data retention, Valkey authentication, Uptime Kuma startup, Gitea setup
and administrator authentication after restart, uploaded TLS, HAProxy configuration
and temporary worker enrollment. Updated deployment/rollback/network acceptance
passed with 587 traffic probes and zero errors; management termination/recovery
passed with 100 probes and zero errors. These samples are not an availability guarantee.

OAuth provider production configuration, real GitHub repository writes/builds,
public DNS/ACME renewal, GPU inference, physical Linux installation and off-host
backup restoration require their real external environments. The product exposes
these prerequisites instead of reporting invented success.
