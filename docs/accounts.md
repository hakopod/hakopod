# Accounts and registration

The person installing Hakopod chooses the first owner’s name, email and password.
The setup credential only authorizes that first claim. Public registration never
creates an installation administrator.

Public release binaries keep public registration closed, even when an operator
sets `HAKOPOD_SIGNUP_ENABLED=true`, `[auth] signup_enabled = true`, or switches
deployment mode. First-owner setup stays available until the installation is
claimed. Existing accounts can sign in, and explicit invitations can enroll
members when their invitation proofs are valid, including on Free.

Cloud builds require the `hakopod_cloud` build tag, `managed-cloud` deployment
mode, and `HAKOPOD_SIGNUP_ENABLED=true` (or its operator TOML equivalent) to enable
public registration. The `hakopod_selfhosted` tag always disables that capability,
even if both tags are supplied. Public archives use the self-hosted tag. This is
a policy of the built artifact; someone rebuilding modified source controls their
own binary. Invitation validation and project authorization remain enforced.

The same policy controls email signup, verification and OAuth account creation.
Changing to a self-hosted build, changing deployment mode, or turning registration
off prevents unfinished public verification links and provider callbacks from
creating accounts. `/api/v1/auth/status` reports the effective policy so the
dashboard does not offer public signup when the backend disallows it.

Email registration and password recovery use the existing SMTP configuration:
`HAKOPOD_SMTP_ENABLED=true`, `HAKOPOD_SMTP_ADDRESS`, `HAKOPOD_SMTP_FROM`, and any
required username and password. Production SMTP requires STARTTLS. A missing
email service is shown in the form; no temporary password or verification token
is returned by the API.

Registration sends a verification link before creating the account. Links expire
after 15 minutes and can be used once. The email proof stays in a URL fragment so
it is not sent in page requests or referrers. Hakopod stores the token digest and
a password hash, never a plain password. Request responses deliberately do not
tell callers whether an email address already has an account. SMTP failures use
the same response; the person can retry after a minute or contact the operator.

The new account chooses an invitation addressed to its verified email, or a
personal workspace. A personal workspace is one private project with a
development environment. It grants project administration only to that account;
it cannot be shared, invited into, or attached to a team. Team creation,
invitations and fixed shared project roles are included in Free. Installation
administrators retain their existing installation-wide access.

GitHub, GitLab and Google use the same configured OAuth clients for sign-in and
registration. Provider email verification, browser-bound state and PKCE are
required. New accounts require the Cloud signup policy above or a valid
invitation for that verified email. A provider account never gains an installation role.
After authenticating through an invitation link, the person confirms the choice
to join that workspace or create a separate personal workspace.

Forgot-password links can also set an initial password for an account created
through OAuth. Resetting a password retains authenticator settings and passkeys,
revokes all browser and CLI sessions, and denies outstanding device approvals.
The next sign-in still needs an enabled second factor. API keys are separate
credentials and keep their current policy.

Authentication work is bounded: two concurrent password operations, two SMTP
connections, 1000 active challenges, 200 human accounts, and one personal
workspace per account. Email requests allow one delivery per address per minute
and three per hour, with at most 1000 active addresses in that hour. These email
limits are durable and store address digests. Public requests return without
waiting for SMTP, with a short response floor; this keeps mail-server latency
from identifying registered addresses. At most two deliveries run in the
background, each with a 15-second deadline. Failed delivery appears as a redacted
audit event. Lists return at most 100 pending invitations. There is no waiting
queue or additional service.

The acceptance tests use temporary PostgreSQL databases, local SMTP listeners
and local OAuth fixtures. They cover registration gating, verified-email
membership, private-workspace isolation, recovery replay and session revocation.
Live provider credentials and outbound email delivery must be configured by the
operator; those services are not exercised by the test suite.
