# Accounts and registration

The person installing Hakopod chooses the first owner’s name, email and password.
The setup credential only authorizes that first claim. Public registration never
creates an installation administrator.

These startup options can also use [operator TOML](operator-configuration.md).
For example, set `signup_enabled = true` under `[auth]` to enable public registration.

Set `HAKOPOD_SIGNUP_ENABLED=true` on the management server to enable public
registration. It is disabled by default. Existing accounts can always sign in,
and a valid paid invitation can create an account while public registration is
closed. Turning registration off also prevents unfinished public verification
links from creating accounts.

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
it cannot be shared, invited into, or attached to a team. Paid team creation,
invitations and shared project roles retain their license checks. Installation
administrators retain their existing installation-wide access.

GitHub, GitLab and Google use the same configured OAuth clients for sign-in and
registration. Provider email verification, browser-bound state and PKCE are
required. Registration requires the environment switch or a valid invitation
for that verified email. A provider account never gains an installation role.
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
