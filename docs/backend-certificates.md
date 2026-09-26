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
owned ingress. This supports an uploaded ingress certificate or one issued
by cert-manager. The hostname must appear in that ingress's TLS hosts. Callers
cannot select another service, namespace or arbitrary Secret. Services with only
public TCP can also upload a certificate managed outside Hakopod.

The import endpoint and `--from-ingress` command still create pinned snapshots.
To follow renewals automatically on a self-hosted installation, deploy the
service with its HTTP TLS ingress first, then review this opt-in configuration:

```toml
[services.smtp]
image = "example/smtp:1"
port = 8080
public = true
healthcheck = "/health"
certificate_mounts = [
  { source = "ingress", hostname = "mail.example.com", mount_path = "/certificates/smtp" },
]
```

Keep your existing private SMTP port, public TCP mapping, environment variables
and filesystem settings in the same service. `mail.example.com` must be a domain
of this service and already appear on its active TLS ingress. The service needs
an explicit TLS configuration or the installation's default TLS issuer. A mount
has either `certificate` or `source`, never both. Automatic sources are disabled
on Hakopod Cloud. A service published only over public TCP can use an automatic
source too; see below. Pinned uploads remain available for any service.

The existing reconciliation loop checks up to 50 applications per 15-second
cycle, rotating one automatic-certificate service per application on each full
scan. Each maintenance step has a five-second budget so a large application
cannot starve later services. Maintenance runs only after a successful accepted
deployment and pauses while another deployment is queued or running.
It validates the current source and
creates an immutable service-owned copy only when certificate bytes change.
Changing that mount restarts the service using its configured update strategy
at the same application
revision. No upload, deployment approval or persistent sidecar is needed after
opting in. Applications that load TLS at startup reopen the new certificate.
The normal readiness gate controls when a replacement pod receives traffic.
For singleton queue runtimes such as Xem, use `replicas = 1` and
`update_strategy = "recreate"`; renewal then includes a brief stop-first outage.

Missing, expired or invalid source certificates keep the last valid mount and
report unhealthy/pending delivery status. Check application observations and
alarms; a rollout can still stall because of scheduling or application failures.
Maintenance shares the deployment lock and yields when a deployment is queued.
An automatic-source rollback follows the current valid ingress certificate;
a pinned-upload rollback uses its original reference while valid.

Automatic copies are separate from uploaded history and are deleted only when
no Deployment, ReplicaSet or Pod in the application namespace refers to them.
Cleanup reads at most 256 of each workload kind, fails closed on partial lists,
and keeps at most 64 automatic versions per service. It never deletes uploaded
references. An operator must retire obsolete workloads if that bound is reached.

Use [SMTP readiness](readiness.md) to combine HTTP health with a real listener
or STARTTLS check. Certificate renewal does not prove public routing, AWS access,
mail authentication or outbound deliverability. Verify those separately before
switching production traffic.

### Services published only over public TCP

`source = "ingress"` no longer requires `public = true`. A service with no public
HTTP but at least one `public_tcp` listener and a `source = "ingress"` mount gets
a certificate-only ingress, so it can follow renewals instead of having PEM files
uploaded by hand forever. This fits a service that terminates TLS itself, such as
an SMTP submission server or a tunnel server.

```toml
[services.tunnel]
image = "example/tunnel:1"
port = 7000
public = false
certificate_mounts = [
  { source = "ingress", hostname = "tunnel-1a2b3c4d5e6f.apps.example.com", mount_path = "/certificates/tunnel" },
]

[[services.tunnel.public_tcp]]
port = 7443
target_port = 7000
source_cidrs = ["0.0.0.0/0"]
```

That hostname is the service's generated hostname, not a custom domain. A
TCP-only service can use only its generated hostname for an automatic
certificate, because `domains` maps a custom hostname only to a service that is
public for HTTP, so a custom hostname never reaches such a service's ingress TLS
hosts and the deployment is refused with a message that the automatic
certificate hostname must remain on this service's configured TLS ingress. The
generated hostname has the shape `<service>-<twelve hex characters>.<the
installation's application domain>`; Hakopod shows it as the service's URL on the
dashboard service page and in the service status the API returns. To serve a
custom hostname on a TCP-only service, upload a pinned certificate for it as
described above; that path is unaffected by this constraint.

The ingress Hakopod then owns carries a hostname and TLS but no HTTP backend:

- `spec.rules` is exactly one entry with only a `host`, and no `http` block. The
  ingress therefore routes no HTTP traffic and creates no backend. A rules-less
  ingress is not an option; the API server rejects a spec with neither `rules`
  nor `defaultBackend`, which is why the shape is a host-only rule.
- `spec.tls` names the service's hostnames and the secret `hakopod-tls-<service>`,
  with the `cert-manager.io/cluster-issuer` annotation, so cert-manager's existing
  ingress-shim issues into it. The mount and renewal behavior described above is
  then unchanged.
- The ingress is created before the workload and is not deleted on each release,
  so the certificate is not re-requested every deployment.

Because `tls` itself still requires a public HTTP service, a TCP-only service
takes its issuer from the installation's default TLS issuer. With no default
issuer configured the ingress gets no TLS block and the mount reports that the
hostname is not covered by the service's configured ingress TLS.

Verified on a development cluster with cert-manager 1.21.2 and the HAProxy
ingress controller 3.2.15: the API server accepts this shape; the controller
creates no backend, does not restart, and logs one benign warning that HTTP rules
for the host do not exist; cert-manager's ingress-shim issued a certificate for
the shape in about twelve seconds with a matching SAN, and HAProxy loaded and
served it. That run used a self-signed issuer, deliberately, to separate the
ingress-shim question from ACME validation.

Not verified: ACME HTTP-01 validation against an ingress with no `http` block.
This is the plausible failure point, because the HTTP-01 solver works by
injecting a temporary route for `/.well-known/acme-challenge/`. Hakopod only ever
creates HTTP-01 solvers; there is no DNS-01 path. So for a public hostname the
hostname's DNS must resolve to the HTTP ingress on port 80 for issuance to work
at all, which is an operator DNS fact the platform cannot assert. Per-SNI
certificate selection was also not discriminated in that run, because the probe
ingress was the cluster's only TLS ingress and so doubled as the default
certificate. Confirm issuance and SNI selection on your own installation before
relying on automatic renewal for a public TCP hostname, and keep an uploaded
pinned certificate as the fallback.

Two operational notes. The first TLS ingress in a cluster with no existing
certificate list triggers one full graceful HAProxy reload rather than a hitless
runtime certificate update; later ones should use the runtime path. And on the
very first release that introduces such a mount, if cert-manager has not finished
issuing by the time the workload resolves the mount, that release fails with a
message about the ingress certificate not having been issued or uploaded and
succeeds on retry. This matches the existing behavior of the public HTTP path.

`GET` on the same endpoint returns up to 64 historical certificate entries plus
any active references outside that page, including the mount path, readiness,
expiry and a validation message. It never returns PEM
data. Missing, expired, foreign or mutable certificate references block deployment.
Only nonsecret references enter TOML, revision history and audit records. Enable
Kubernetes Secret encryption at rest and protect cluster backups as usual.
