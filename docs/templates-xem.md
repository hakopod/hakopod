# Xem deployment template

Xem is a self-hosted email marketing workspace with a Go API and a Next.js/Bun
frontend. Its template offers independent **Bundled** or **Existing** choices
for PostgreSQL, Redis and media storage, with one public Caddy proxy and private
application services. MinIO is bundled by default; an existing private
S3-compatible bucket remains available as an independent option.

Hosted Cloud deployments need an allocation that supports multiple services,
their resource sizes and persistent storage. Free hosted compute does not
provide the default six-service topology.

The shared [blueprint and setup instructions](../templates/blueprints/xem/README.md)
are the source of truth for configuration, scoped credentials, capacity and
verification. The preset pins public upstream images by digest; it does not
require a local registry or Docker login.

## Upstream images

The self-hosting changes were merged in
[Xem PR #16](https://github.com/mailxem/mail/pull/16), source commit
`63b0809c2a4ba0965047ee20d08b101e0fac7c48`. Xem's release workflow publishes the
portable frontend and backend for AMD64 and ARM64:

- `theboringhumane/xemapp:sudo-self-hosted`: browser API requests use
  `/api/v1`, server authentication uses runtime `INTERNAL_API_URL`, and exported
  public-form HTML retains the installation's origin.
- `theboringhumane/xemgo:sudo`: the preset sets `S3_DISABLE_ACL=true` so uploads omit object ACL headers,
  private uploads and public signed reads use separate endpoints, and only the
  bundled storage mode creates a missing private bucket. A refresh-token fix
  binds PostgreSQL UUID values as parameters.

The regular hosted frontend embeds its configured API origin in browser assets.
A runtime public URL variable cannot change those files. The preset therefore
uses the upstream `sudo-self-hosted` frontend variant, resolved to a digest.
The regular frontend `sudo` and `latest` tags retain the hosted settings. Build
success alone does not prove authentication or external-service connectivity.

## Configuration

Use a stable HTTPS origin and finish custom-domain routing/TLS before login.
Database and Redis mode selectors reveal their own host, port, identity and TLS
fields. Existing PostgreSQL defaults to `verify-full`; existing Redis defaults
to verified TLS. Selecting one existing dependency leaves the other choice
unchanged. Save the existing service's current password in its scoped secret;
the template neither provisions nor rotates an external credential.

Bundled MinIO uses generated `storage-user` and `storage-password` credentials,
a private service and its own
persistent volume. The backend creates the private `xem-files` bucket if missing,
uploads through the private endpoint and signs browser reads for the public
installation origin. Caddy exposes only object GET/HEAD requests, preserves the
signed host and path, strips application credentials, and prevents public caching.
MinIO's console, administration and writes have no public route.

Existing storage reveals `storage-bucket`, `storage-endpoint` and `storage-region`,
with provider-issued bucket-scoped access and secret keys. The full HTTPS endpoint
must be reachable by the API and browser. Selecting this mode removes bundled
MinIO and its public file route and leaves bucket creation disabled. Upstream's
local-storage settings are unused; the bundled option provides S3 through MinIO.

Administrator bootstrap asks for email, name, team and a separate password. The
RSA encryption secret is Base64 of an unencrypted RSA private PEM key, using
2048–4096 bits. Generate and copy it before saving, keep it stable, and include
it with database backups. Bootstrap settings do not change an existing account.
The administrator password limit is 72 bytes, matching bcrypt's input bound.

Managed SES, SMTP submission, managed notifications, MCP, payments and hosted AI
services are excluded. Configure a user-owned sending provider inside Xem when
needed. No messages are sent by template validation or acceptance.

## Verification status

The source contract and upstream fixes are implemented. Planner/API tests cover
all eight bundled/existing combinations. Earlier development builds and browser
checks covered authentication, forms and uploads with external storage. The
final bundled-MinIO cluster matrix stopped because of development disk pressure
before its backend and storage assertions. The shared blueprint's verification
section records image release evidence and these runtime limits; registry
architecture metadata alone is not a runtime claim.
