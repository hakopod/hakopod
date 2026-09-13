# Backend certificate mounts

SMTP STARTTLS and other application-level TLS protocols need their certificate
inside the container. A service's `tls` setting still configures public HTTP
ingress. Use `certificate_mounts` for the backend; the two settings are independent.

Upload an existing PEM certificate and matching private key to:

```text
POST /api/v1/applications/{id}/services/{service}/certificates
```

The authenticated request needs `deployments:write` for that application. Its JSON
body contains `hostname`, `certificate_pem` and `private_key_pem`. Read those PEM
values from local files in your API client; do not paste keys into TOML, shell
history or deployment arguments. The application namespace must already exist.
Deploy a new application privately first, then upload and review its public
listener and certificate configuration together.

The CLI reads certificate files without putting their contents in command
arguments:

```sh
hakopod certificate-upload APP_ID --service smtp --hostname mail.example.com \
  --certificate-file fullchain.pem --private-key-file privkey.pem
hakopod certificates APP_ID --service smtp
hakopod delivery APP_ID --service smtp
```

The response returns a `certificate` reference and validity metadata. Uploading
does not modify running containers or create a deployment. Put the returned
reference in the service's TOML and deploy it through the normal review flow:

```toml
[services.smtp]
image = "example/smtp:1"
port = 1587
public = false
run_as_user = 1001
run_as_group = 1001
fs_group = 1001
certificate_mounts = [
  { certificate = "hp-cert-smtp-replace", hostname = "mail.example.com", mount_path = "/certificates/smtp" },
]

[services.smtp.env]
SMTP_TLS_CERT = "/certificates/smtp/tls.crt"
SMTP_TLS_KEY = "/certificates/smtp/tls.key"
```

Replace the image, certificate reference and environment variable names with
those supported by your SMTP server. The example configures private TCP and a
certificate mount; public TCP exposure is a separate explicit setting.

Hakopod checks the hostname, expiry, key pair, server authentication usage and
minimum key strength. It does not prove a public CA trust chain or deliverability.
Verify those with a real SMTP client before switching production traffic.

Each reference belongs to one application and service. It cannot mount another
service's private key. Files are mounted read-only as `tls.crt` and `tls.key`, with
mode `0440` and the service's `fs_group` (which defaults to its runtime user).
Root, system directories, overlapping mounts, host directories and arbitrary
Kubernetes Secret references are not supported. There are at most four certificate
mounts per service and 64 retained uploaded certificate versions per service.

## Rotation and rollback

Uploaded certificates are immutable Kubernetes TLS Secrets. Uploading a renewed
certificate produces a new reference. Review and deploy that reference to roll
the service; containers then reopen the new certificate at startup. Hakopod does
not send an application-specific reload signal. Previous references remain for
rollback while their certificates are valid. Retiring old versions is an operator
task after checking every retained revision that might need them.

The same endpoint accepts `{"hostname":"mail.example.com","from_ingress":true}`
instead of PEM values to snapshot the current certificate from this service's
owned HTTP ingress. This supports an uploaded ingress certificate or one issued
by cert-manager. The hostname must appear in that ingress's TLS hosts. Callers
cannot select another service, namespace or arbitrary Secret. Services with only
public TCP can upload a certificate managed outside Hakopod.

Cert-manager renewal of the HTTP ingress does **not** rotate an existing backend
snapshot. Import again after renewal, then review and deploy the new reference.
Use `hakopod certificate-upload APP_ID --service smtp --hostname mail.example.com
--from-ingress` to make that snapshot from the CLI.
Automatic backend renewal or reload is not implemented. Keep expiry monitoring
and the existing SMTP deployment in place until this complete rotation path and
external STARTTLS behavior have been verified for your installation.

`GET` on the same endpoint returns up to 64 historical certificate entries plus
any active references outside that page, including the mount path, readiness,
expiry and a validation message. It never returns PEM
data. Missing, expired, foreign or mutable certificate references block deployment.
Only nonsecret references enter TOML, revision history and audit records. Enable
Kubernetes Secret encryption at rest and protect cluster backups as usual.
