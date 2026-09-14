# Installation SMTP settings

Self-hosted installation administrators can configure account and notification
email at `/api/v1/installation/smtp`. These settings are available on the Free
plan. Only an unscoped human browser administrator can read, change or test
them. Hakopod Cloud uses operator SMTP configuration and rejects customer access
to these endpoints.

`GET` returns the current revision, enabled state, host, port, security, username,
from_email, source (`operator` or `settings`), password_set and encryption_ready.
It never returns the password or its encrypted value. Revision 0 means no saved
override exists. Operator TOML or environment configuration remains the fallback
until the first save; a saved disabled configuration prevents delivery.

`PUT` requires expected_revision and every nonsecret field. Security must be
`starttls` or `tls` (implicit TLS). Both modes validate the server certificate
and require TLS 1.2 or later. An omitted or empty password retains the current
password, including the operator password on the first save. Set clear_password
to true to remove it; do not also supply a new password. Passwords are limited
to 4096 bytes and cannot contain NUL, CR or LF. JSON requests are capped at 16 KiB.
A stale revision returns 409 without changing settings.

Saving requires the installation authentication encryption key. Passwords are
stored in authenticated encrypted envelopes using that key; back up the key
with the database. A missing or incorrect key prevents saved credentials from
being used. Database or decryption failures never silently restore operator
SMTP configuration.

`POST /api/v1/installation/smtp/test` accepts only expected_revision and sends a
fixed test message to the signed-in administrator's email using the saved
configuration. It does not accept a recipient, message or unsaved connection
settings. Transport errors are redacted. A success means the SMTP server accepted
the message, not that it reached an inbox. No test is sent automatically on save.

Invitations, registration, password recovery and alarm deliveries all resolve
the effective configuration for each delivery. Alarm scopes still require their
own email_enabled setting. Delivery shares two concurrent slots and bounded
network timeouts with account email.

Operator SMTP supports `smtp.security` / `HAKOPOD_SMTP_SECURITY`: `starttls` is
the default; `tls` selects implicit TLS. See
[operator configuration](operator-configuration.md) for the remaining fields.
