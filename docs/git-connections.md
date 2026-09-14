# Named Git source connections

Git connections are separate from account sign-in. An administrator creates a
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

A GitHub App can replace a personal access token. Register an operator-owned App,
then provide its App ID, RSA private key and installation ID. Hakopod verifies the
App's identity and checks that the installation belongs to it and is active. The
installation and account are immutable; use a new connection to select another.
These administrator-supplied credentials are not an unverified installation setup
callback. Automatic browser installation onboarding is not implemented here.

For TOML source reads, grant Contents read. Existing source builds additionally
need Contents write and Workflows write to install the reviewed workflow, and
Actions write to dispatch/cancel runs and observe their artifacts. Repository
access is selected on the GitHub installation. Hakopod mints a short-lived
installation token for one repository and only the permissions needed by the
current request. Tokens are not persisted or supplied to workloads.

Configure the App's webhook URL to the returned `webhook_path`, and set its webhook
secret to the value returned once by Hakopod (or the explicit supplied secret).
All connections for the same App share this signing secret and webhook URL. Events
are authenticated before routing by the verified installation ID. Push events
feed TOML sources; workflow_run events feed automatic builds. Installation deletion
or suspension disables the corresponding connection. Re-enable it after fixing the
provider installation; saving verifies its current identity again.

## GitLab OAuth

Register a GitLab OAuth application with the dashboard callback URL
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
records retain that exact default connection. The original provider settings and
webhook URLs continue to operate on those defaults only. An omitted connection ID
means that fixed provider default; it never selects the first named connection.
Default connection rows cannot be removed through the new API. An application
repository or named connection change still needs administrator approval.

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
tests. Dashboard forms and public proxy forwarding need their own implementation
and rendered review before this backend can be called a complete dashboard flow.
