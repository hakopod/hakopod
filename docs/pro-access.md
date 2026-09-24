# Pro access controls

Core deployments, rollback, fixed project roles, personal TOTP/passkeys and
backups remain Free. An installation-bound signed license must explicitly enable
`custom_roles` or `team_mfa`; changing a plan label does not grant either feature.

## Custom project roles

Installation administrators create roles in **Settings → Teams & access → Custom
roles**. The editor reviews the role name and permissions before saving. Roles
may grant deployment read/write and log/request access. Deployment write requires
read access. Custom roles cannot grant installation or membership administration.

Assign a role to a person or team using project access, or include it in a project
invitation. Multiple memberships combine their permissions. Changes affect
subsequent authorization checks for existing sessions. Deleting a definition
removes its assignments and pending invitations, with no replacement privilege.

When entitlement expires, custom assignments grant no authority. Definitions
remain available for inspection and deletion. Fixed Free roles continue working
and can be assigned for recovery. At most 100 custom definitions are supported.

The generated API contract exposes `GET/POST /api/v1/roles` and
`PUT/DELETE /api/v1/roles/{role}`. Definition edits and deletion require the current
`expected_revision`; a create uses zero. Names are unique without case sensitivity.

## Organization MFA

**Settings → Teams & access → Organization MFA** shows how many people have an
authenticator or passkey enrolled. An administrator must verify their own session
before enabling the requirement. The confirmation applies it immediately to
human browser and CLI sessions. Machine credentials remain independently scoped.

Unverified sessions can access account recovery and enroll or verify a factor,
but cannot operate workloads. A password-only session can verify its authenticator
or a recovery code under **Account security**. A passkey login requires user
verification. CLI device consent carries the browser's verified assurance into
the scoped CLI session; older unverified CLI sessions must authenticate again.

People cannot remove their final factor while the policy requires it. Removing
all factors when permitted clears existing session assurance. License expiry
never silently disables an enabled policy. An authorized verified administrator
may explicitly disable it after expiry.

The API exposes `GET/PUT /api/v1/organization/security` and
`POST /api/v1/auth/mfa/verify`. Policy edits use optimistic revisions. TOTP and
recovery-code verification retain the existing replay and login-attempt limits.

## Embedding boundaries

Embeddings may narrow runtime permissions and supply an authorization refresh
callback. They can also install a final-factor policy and a deployment admission
callback. Deployment admission runs in the revision transaction before resource
locks, covering manual, rollback, source and build acceptance. Existing accepted
idempotent results remain retrievable without allocating another revision. These
hooks do not expose any new client-controlled trust or grant a Pro license.

Deployment approval workflow is provided by Hakopod Cloud Team, not by the
self-hosted Pro license.
