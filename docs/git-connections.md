# Named Git source connections

Git connections are separate from account sign-in. An installation administrator or scoped workspace owner creates a
named GitHub or GitLab connection and selects its ID when importing a repository,
binding an application's TOML source, or creating a source build. Source and build
reads, workflow writes, run observation, cancellation and automatic work use that
saved connection. Creating or saving a connection never writes to a repository or
starts a build.

The installation stores new credentials encrypted in PostgreSQL using its
32-byte authentication encryption key. Credential bodies and App private keys are
never returned by reads, included in audit metadata, or embedded in generated CI.
A generated webhook secret is returned once on creation. Connection updates use
an expected revision; blank secret inputs preserve existing credentials. Disabling
a connection stops new provider work. Removal is refused while any source, build,
or retained durable job references it. Removal does not uninstall a provider App.

## GitHub Apps

The dashboard's **Add connection → GitHub** flow registers an App through
GitHub's [App Manifest flow](https://docs.github.com/en/apps/sharing-github-apps/registering-a-github-app-from-a-manifest).
The person or organization registering the App owns it. Enter a connection name,
optionally an organization login, and choose whether the App needs Actions build
permissions. Click **Install GitHub App**, approve creation on GitHub, then choose
repositories to finish installation. No personal token, App ID, installation ID,
private key or webhook secret needs to be copied into Hakopod.

The operator must configure a publicly reachable HTTPS dashboard URL. GitHub must
reach `/api/v1/webhooks/git/{connection_id}` at that origin. The UI reads the
operator's configured callback address, rather than assuming the browser's current
address. Localhost and private IP setup are rejected; use a public HTTPS deployment
or a properly configured development tunnel. The URL check is syntactic, not a
public DNS or connectivity probe. GitHub will still reject an unreachable webhook.

Setup saves a disabled connection first. One-use, 15-minute challenges bind each
phase to the initiating repository manager’s browser session, connection revision and
configured origin. The server exchanges GitHub's temporary code, encrypts the
returned App key and webhook secret, and retains them before installation. An
unfinished connection can be reopened to resume. The connection remains unusable
until Hakopod verifies its App, owning account, active installation, required
permissions and secure JSON webhook destination. GitHub callback query parameters
alone never authorize a connection. Expired or failed installation callbacks can
be retried through **Continue on GitHub**. If creation succeeds on GitHub but the
one-time conversion response is lost before being saved, delete that unused App
on GitHub before restarting registration; provider creation and local persistence
cannot be one atomic transaction.

New dashboard connections offer GitHub Apps and GitLab OAuth, with no token
creation option. Both provider cards open this setup with the matching provider
selected. Old integration URLs and direct default-connection links redirect to
the same setup flow. Named connections still open their existing edit/resume page.
Existing token connections and the compatibility API remain
available so existing deployments are not interrupted. Existing manually configured
GitHub Apps retain their current webhook and credential workflow.

For TOML source reads, grant Contents read. Existing source builds additionally
need Contents write and Workflows write to install the reviewed workflow, and
Actions write to dispatch/cancel runs and observe their artifacts. Repository
access is selected on the GitHub installation. Hakopod mints a short-lived
installation token for one repository and only the permissions needed by the
current request. Tokens are not persisted or supplied to workloads.

Manifest-created Apps already have a connection-specific signed webhook configured.
Do not replace that webhook secret or URL manually. Their dashboard edit form
manages the connection's name and enabled state; repository access is managed on
GitHub. Manual legacy App connections use the returned `webhook_path` and share
an App-level signing secret. Events are authenticated before routing by the verified
installation ID. Push events feed TOML sources; workflow_run events feed automatic
builds. Installation deletion or suspension disables the corresponding connection.
Re-enable it after fixing the provider installation; saving verifies its identity.

## GitLab OAuth

GitLab's [Applications API](https://docs.gitlab.com/api/applications/) is for
instance administrators; it cannot create personal or group OAuth applications.
There is no supported user-owned manifest flow equivalent to GitHub's. The dashboard
therefore provides a registration link and exact settings, followed by provider-led
authorization. This one-time registration step is not automated, and the UI says so.

Register a confidential GitLab OAuth application with the dashboard callback URL
`https://YOUR-DASHBOARD/settings/git/callback`. Create a `gitlab_oauth` connection
with its client ID and secret. Source reads use `read_api`; full build installation,
pipeline control and artifact observation use `api`. The provider user's project
permissions still apply. `write_repository` does not authorize these write APIs.
No personal access token is required by this authentication method.

The dashboard starts `POST /api/v1/git/connections/{id}/authorize`, opens the
returned authorization URL, then submits `code` and `state` from the callback to
`POST /api/v1/git/oauth/complete`. The source-specific one-use challenge binds
PKCE, the exact callback, connection revision and initiating browser session.
Completing under another session, even for the same administrator, is refused.
The response contains connection metadata, never provider tokens. Operator account
sign-in settings are neither reused nor changed.

The encrypted access/refresh pair is rotated under a per-connection PostgreSQL
advisory lock. Refresh uses the returned expiry, persists both tokens together,
and increments a separate credential generation so ordinary refresh does not
invalidate source reviews. Interrupted or failed refresh requires reconnection;
it cannot silently replay a potentially consumed refresh token. One authorized
connection per GitLab OAuth App/user pair prevents independent refresh races over
the same delegated grant. Use that connection for its approved repositories.

GitLab OAuth is user-delegated authority, not a GitHub-style repository installation.
Connection and repository approval in Hakopod still restrict which sources/builds
may use the token. OAuth does not create remote project webhooks automatically.
Configure the connection's returned webhook URL and secret for Push and Pipeline
events as needed. New imports capture the connection revision and require a fresh
review after operator configuration changes. Already accepted import retries still
recover their original durable deployment after disabling the connection.

## GitLab and token connections

A named token connection uses its own webhook URL and secret. GitLab token
connections currently authenticate legacy webhooks with X-Gitlab-Token. GitHub
uses X-Hub-Signature-256. Only exact matching connection/provider/repository/branch
bindings receive durable work, and duplicate delivery IDs are scoped to the
connection. Provider payloads cannot select another connection's credential.

GitHub.com and GitLab.com are supported. Self-managed Git hosts and their runner
or registry configuration are not enabled by these APIs. The existing GitLab
one-managed-CI-entrypoint restriction and persistent runtime registry credential
requirements remain applicable; see [GitLab builds](gitlab-builds.md).

## Legacy compatibility

Migration 023 creates `github-default` and `gitlab-default`, referencing the
original platform Kubernetes Secrets. Existing bindings and queued/saved build
records retain that exact default connection. The original provider API and
webhook URLs continue to operate on those defaults only; old dashboard provider
settings now redirect to App setup. Creating an App connection does not migrate
existing source/build bindings or overwrite their credentials. An omitted
connection ID
means that fixed provider default; it never selects the first named connection.
Default connection rows cannot be removed through the new API. An application
repository or named connection change still needs repository-manager approval.

## API and verification

`GET/POST /api/v1/git/connections` and `GET/PUT/DELETE
/api/v1/git/connections/{id}` manage installation connections. Updates require
`expected_revision` in the JSON body, and deletion requires it as a query parameter.
Source/import/build inputs accept `connection_id`; returned bindings/configurations
include it. The checked-in OpenAPI contract documents all fields.

Local fixtures use disposable PostgreSQL databases and HTTP provider doubles.
They verify encryption/redaction, administrator authorization, revision conflicts,
reference-protected deletion, credential selection, same-repository webhook
isolation/deduplication, App JWT signatures, verified installation identity and
repository-scoped token minting. No hosted App registration, installation, webhook
delivery, repository write or provider runner execution was performed by these
tests. The manifest tests also cover same-session/replay/expiry checks, concurrent stale
callbacks, resumable encrypted credentials, owner/permission/suspension/webhook
rejections and verified activation. The dashboard proxies explicitly allow only the
new setup paths and retain session and same-origin checks. A real provider App
creation and webhook round trip still need verification on a public HTTPS instance.

## Scoped workspace management

Cloud owners manage connections within their selected project and environment.
They cannot see or use another workspace’s connections or the installation’s
legacy defaults. Self-hosted administrators retain installation-wide management;
existing self-hosted approved source bindings continue to work.

A BYO controller can delegate `git:manage` and `applications:manage` on an explicit
project/environment key. These capabilities do not grant installation, SMTP,
host or user administration. Provider callbacks additionally bind to the initiating
Cloud browser session. BYO webhooks use the public dashboard’s node-specific
route, retain signed body bytes and provider signature headers, and reach only the
assigned node. The owning engine verifies every provider signature.

## Runtime commands for Git builds

Dockerfile, buildpack and framework builds support an optional runtime command in
**Build settings → Runtime**. Choose **Use image defaults** to retain the image’s
ENTRYPOINT and CMD, **Override runtime command** to replace them, or **Keep service
settings** on a linked build to retain its current deployment command.

For a FastAPI image, set Command to `uvicorn` and Arguments to
`main:app --host 0.0.0.0 --port 8000`. Set the service’s container port to `8000`.
Use the Python module path exported by your own repository. If the Dockerfile
already starts Uvicorn correctly, use image defaults.

Command replaces ENTRYPOINT and Arguments replaces CMD. Quotes group arguments;
there is no implicit shell expansion. Use `sh -c` explicitly when a shell is needed.
The choice applies when the built image is deployed, including automatic releases.
In API inputs, omitted `command`/`args` preserve a linked service’s settings; empty
arrays clear the corresponding override and restore image defaults. These are
runtime overrides, separate from build commands and build arguments.
