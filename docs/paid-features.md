# Free and Pro features

Hakopod runs without an activation service or a paid license. Free includes the
installer-selected owner, account recovery and authentication, password/provider
login, passkeys and TOTP, machine keys, application deployments and rollbacks,
source builds and approved source synchronization, logs and metrics, node and
service operations, registries, secrets and TLS. GitLab.com login is supported
alongside GitHub and Google. GitLab build-provider support is described separately
from authentication and source synchronization.

The implemented Pro feature identifiers are `teams`, `invitations` and
`project_rbac`. They cover team creation/membership, creating and accepting member
invitations, and granting or using project roles for people and teams. These
features are explicit signed entitlements; a `pro` label alone does not grant them.
The authenticated `GET /api/v1/license` catalog is the UI's source of feature state.

On expiry, removal, invalid signature or a signed downgrade, paid mutations stop.
Existing users can still authenticate and manage their own security. Direct and
team-derived project roles stop authorizing new requests and durable worker
execution. Global administrators retain Free operations and recovery access;
they may revoke memberships and role grants or delete a team to clean up safely.
Existing global administrators are not demoted by license changes. Running
workloads and stored data are not deleted. Renewing the required entitlements
restores retained roles, unless an administrator removed them.

# Verification and activation

The verifier accepts `hl1.<base64url-json>.<base64url-ed25519-signature>`. Signatures
cover the exact payload bytes prefixed by `hakopod-license-v1\0`. The strict payload
contains version `1`, issuer `key_id`, 32-character hexadecimal `license_id` and
`installation_id`, customer name, `plan`, positive monotonic `sequence`, UTC Unix
`issued_at`, `not_before`, `expires_at`, and explicit paid `features`.

Each database migration creates one durable random installation ID. The issuer
signs for that ID. Moving a licensed database retains the installation identity;
an independent installation needs its own license. Activation requires a browser
global administrator and the current license revision. Signatures, installed
identity, validity interval, feature names and sequence are checked server-side.
The highest sequence is durable. A signed Free downgrade blocks replay of an older
Pro license. Removing an activation also retains that watermark: restoring paid
access requires a newly issued license with a higher sequence. Do not remove a
license merely to refresh its status.

Paid store mutations take a shared lock on the license row while activation and
removal take an exclusive lock. Authentication and worker reauthorization verify
the stored signature and expiry again. There is no environment variable or API
flag that enables Pro. The implementation has no background license network poll,
unbounded cache, hardware fingerprint scan or separate licensing service. Tokens
are bounded to 16 KiB and issuer trust is bounded to eight public keys.

# Issuer separation and release setup

The license-generation code belongs to the separate local private repository at
`private/license-issuer`. It is not imported into server/CLI builds and must not be
published with the public repository. The parent repository stores only its
submodule reference after integration. Its local bare origin is `../hakopod-license-issuer.git`; configure the
actual authenticated hosted repository URL when one is available. No hosted
repository has been created and neither repository has been published.

No production keypair has been generated. The private issuer provides explicit
key generation and signing commands; signing files must be private and remain
outside either Git index. Its tests use temporary in-memory/temporary-directory
keys. Public verification keys may be embedded in a trusted release using:

```text
-X github.com/hakopod/hakopod/internal/license.ReleaseKeys=issuer-id=BASE64URL_PUBLIC_KEY
```

Comma-separated `key-id=public-key` entries allow a bounded rotation window. The
current development binary has no production trust anchor and reports
`issuer_configured:false`; a real paid activation requires the vendor's public
verification key in the release. Never embed or configure an Ed25519 private key
in Hakopod. License tokens and keys are never logged by these handlers.

This is enforcement by the published application. Someone who controls the
source, binary, database and machine can modify them; open-source code cannot
guarantee that it is impossible to patch out license checks. The private signing
key protects issuance and tamper detection, not against an operator replacing the
entire application or restoring an old database snapshot.

# GitLab.com sign-in

Register a confidential GitLab.com OAuth application with `openid`, `email` and
`profile` scopes. Set `HAKOPOD_GITLAB_CLIENT_ID` and
`HAKOPOD_GITLAB_CLIENT_SECRET`, and register the exact callback:

```text
https://YOUR_DASHBOARD_ORIGIN/api/v1/auth/oauth/gitlab/callback
```

The callback uses an HttpOnly browser state binding, one-use state, S256 PKCE and
the GitLab.com token/userinfo endpoints. It requires `email_verified:true` and an
existing enabled local account; it does not auto-enroll an unknown provider email.
TOTP continuation applies in the same way as other provider logins. OAuth login
credentials and source-integration tokens are separate. No GitLab self-hosted
issuer URL can be supplied by an API caller.

GitLab.com's live discovery document was checked during implementation:
<https://gitlab.com/.well-known/openid-configuration>. Provider tests use local
fixtures; no real account login or external invitation was performed.
