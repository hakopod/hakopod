# Free and Pro features

Hakopod runs without an activation service or a paid license. Free includes the
installer-selected owner, account recovery and authentication, password
login, passkeys and TOTP, machine keys, application deployments and rollbacks,
source builds and approved source synchronization, logs and metrics, node and
service operations, registries, secrets and TLS. GitLab.com login is supported
alongside GitHub and Google. GitLab build-provider support is described separately
from authentication and source synchronization.

One self-hosted team, member invitations and fixed project roles are included in Free.
Additional teams require `multi_team`; Google, GitHub and GitLab login requires
`oauth_login`. Enterprise OpenID Connect SSO requires `enterprise_sso`.
See [installation access settings](installation-access-settings.md). The
capability identifiers `teams`, `invitations` and `project_rbac` remain stable, but
no paid activation is required. Team roles are owner, administrator and member;
project roles are administrator, developer and viewer. These fixed roles do not
allow custom permissions or installation administration through an invitation.
The authenticated `GET /api/v1/license` catalog is the UI's source of feature state.

Managed Actions runner pools require the explicit `managed_actions` Pro
entitlement and the optional isolated runtime. Open **Catalog → Automation →
Managed Actions** after installing the runtime. Each replica runs one GitHub
job, including Docker actions and service containers. See
[Managed Actions](managed-actions.md) for setup, resource limits and draining.

Advanced custom roles, user audit history/export and team-wide MFA enforcement
are paid capabilities: `custom_roles`, `audit_history` and `team_mfa`. User audit history and CSV export are implemented in Settings → Audit events and
`GET /api/v1/audit/history` / `GET /api/v1/audit/export`. They require installation
administration and an explicit `audit_history` entitlement, with pages capped
at 100 history events or 1000 export rows. Core `/audit` remains Free. Custom
roles and team MFA enforcement are planned and unsupported. Only
implemented capabilities appear in the catalog. The verifier reserves these
explicit entitlement names; accepting a name is not evidence of implemented UI
or authorization. Core security event recording and recent installation audit
inspection remain Free, as do personal passkeys and TOTP.

Existing v1 tokens with `teams`, `invitations` and `project_rbac` remain valid.
They do not imply any advanced entitlement. Newly issued licenses must name the
advanced capability explicitly. A Pro plan label alone never enables a feature.

Public signup is optional in Cloud-capable builds running managed-cloud mode.
Self-hosted binaries allow first-owner setup and explicit invitation enrollment,
including licensed Google, GitHub and GitLab OAuth with a verified matching email. A
verified account can create one private personal workspace. Its ownership is
separate from shared project grants: it cannot accept members or team assignments.
See [accounts](accounts.md).

Expiry, removal, invalid signatures and signed downgrades remove paid capability
authority. Free team membership, invitations, fixed project roles, existing
sessions, worker authorization and core audit logging continue to work. No
users, workloads, roles or stored data are deleted by a license change.

# Verification and activation

The verifier accepts `hl1.<base64url-json>.<base64url-ed25519-signature>`. Signatures
cover the exact payload bytes prefixed by `hakopod-license-v1\0`. The strict payload
contains version `1`, issuer `key_id`, 32-character hexadecimal `license_id` and
`installation_id`, customer name, `plan`, positive monotonic `sequence`, UTC Unix
`issued_at`, `not_before`, `expires_at`, and explicit paid `features`.

Subscriptions require a finite `expires_at` and a higher sequence on renewal.
An owner lifetime grant must explicitly sign `lifetime:true`, `plan:"pro"` and
`expires_at:0`. Omitting expiry never creates lifetime access. Lifetime grants
still enforce signatures, installation binding, not-before time and sequence;
removal and a signed downgrade still revoke access. Their status has a null
`expires_at`. Older releases reject the new lifetime field and must be upgraded.

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
submodule reference. The issuer repository is private and must remain separate
from public release artifacts.

The vendor public verification key `hakopod-2026-09` is compiled into the
verifier by default. The private signing key stays in protected operator storage,
outside Git and customer installations. The private issuer provides explicit key
generation and signing commands; its tests use temporary keys. Trusted
distributors can replace the public verification set at build time using:

```text
-X github.com/hakopod/hakopod/internal/license.ReleaseKeys=issuer-id=BASE64URL_PUBLIC_KEY
```

Comma-separated `key-id=public-key` entries allow a bounded rotation window.
Releases through `v0.1.0-alpha.27` have no vendor trust anchor and require an
upgrade before activation. A configured issuer does not grant Pro features:
activation still requires a valid, installation-bound token with explicit
entitlements. Never embed or configure an Ed25519 private key in Hakopod.
License tokens and keys are never logged by these handlers.

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
existing enabled local account, a matching live invitation, or the Cloud signup policy.
TOTP continuation applies in the same way as other provider logins. OAuth login
credentials and source-integration tokens are separate. No GitLab self-hosted
issuer URL can be supplied by an API caller.

GitLab.com's live discovery document was checked during implementation:
<https://gitlab.com/.well-known/openid-configuration>. Provider tests use local
fixtures; no real account login or external invitation was performed.
