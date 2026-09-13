# Moving an SMTP service to Hakopod

Hakopod can publish an explicit TCP listener, mount a service certificate and
supply an approved AWS web identity. These are separate from HTTP ingress and
Hakopod's own account/notification email settings. Keep the existing SMTP
service running until the destination passes the checks below.

## Prepare the destination

1. Enable only the required TCP ports in the installation. New installs accept
   `public_tcp_ports: [587]` in installer JSON or the interactive prompt. This
   provisions the pinned HAProxy container, Service and host port, and the API
   allowlist. It does not open a cloud firewall or claim a listener for an app.
   Existing installs need a reviewed Helm update and
   `HAKOPOD_PUBLIC_TCP_PORTS=587` (or `[server] public_tcp_ports = [587]` in
   operator TOML). See [public TCP](public-tcp.md) for source-IP and conflict rules.
2. Deploy your mail application privately first, with its own digest-pinned
   image, secrets and data. Use an unprivileged container port such as 2525. Do
   not change the production MX records or SMTP endpoint yet.
3. Upload a hostname-matching certificate using the service Network tab,
   `hakopod certificate-upload`, or the API. Existing host files can be read by
   the uploading client; the container never receives a host-directory mount.
   The result is a service-owned immutable reference. See
   [backend certificates](backend-certificates.md).
4. If sending uses AWS, create a narrowly scoped role and OIDC trust for the
   exact service account. Register an operator binding for the exact project,
   environment, application and service. Copy the required permission policy
   from the old instance role to the new workload role; do not attach the node
   profile to the application. See [AWS identity](aws-workload-identity.md).

The application configuration then contains references like these. Replace the
image, hostname, certificate reference and binding with your reviewed values:

```toml
schema_version = 1
name = "mail"

[services.smtp]
image = "registry.example.com/smtp:1"
port = 2525
public = false
termination_grace_seconds = 60
aws_identity = "mail-sender"

[[services.smtp.public_tcp]]
port = 587
target_port = 2525
source_cidrs = ["0.0.0.0/0"]

[[services.smtp.certificate_mounts]]
certificate = "hp-cert-smtp-reference-from-upload"
hostname = "smtp.example.com"
mount_path = "/app/certificates"
```

Configure your image to load `/app/certificates/tls.crt` and
`/app/certificates/tls.key`, and to require authentication for submission. There
is no universal SMTP image configuration; Hakopod does not turn these fields
into an open relay. The process and filesystem IDs remain non-root and can be
set with `run_as_user`, `run_as_group` and `fs_group`.

Plan and deploy through the normal TOML, API or dashboard flow. All acceptance
paths, including repository sync and rollback, check runtime ownership and
operator grants. A successful deployment means the workloads and HAProxy
configuration were applied; it does not prove internet reachability, AWS
permissions, or delivery to a recipient.

## Verify before cutover

Run from outside the cluster and its cloud network:

```sh
python3 scripts/smtp-cutover-probe.py --host smtp.example.com --port 587 \
  --username your-test-user --password-file /secure/smtp-password
```

The probe requires trusted, hostname-matching STARTTLS before authentication.
It prints only verification flags. To send one test email, add `--send`,
`--sender` and `--recipient` explicitly, then confirm receipt separately. SMTP
acceptance is not proof that the receiving mailbox got the message.

Also verify:

- An allowed source can submit and a denied source cannot connect. Check the
  source address seen by HAProxy when a cloud load balancer or NAT is involved.
- No private or undeclared port is externally reachable. Test 587 separately
  from 25 or 465; every public port needs its own explicit listener.
- The AWS probe completes the intended operation using the expected role.
  Another service cannot use that identity or node credentials. Check SES
  regional settings, sandbox status and recipient restrictions if applicable.
- A new certificate reference deploys successfully; clients see its new serial.
  Rollback restores a still-valid prior certificate and listener configuration.
- Existing connections drain as expected during normal pod replacement and
  terminate predictably when listener routing or allowed sources change.
- Queue/data backups and application-specific recovery work. Configuration
  rollback does not restore mail queues, databases or remote provider state.

Only after these pass should you move production traffic. Retain the previous
service, credentials and endpoint through your agreed observation period.

## Verification record

The following checks passed on 13 September 2026:

- The full Go suite with PostgreSQL, `go vet`, and command builds.
- Dashboard type checking, tests and production build; generated API contracts
  match their source. The independent [UI review](delivery-ui-review.md) passed.
- Installer tests and the SMTP probe's tests, including rejection of untrusted
  TLS before authentication.
- Named-development-cluster acceptance for STARTTLS certificate/hostname
  validation, source restrictions, port conflicts, private backend ports,
  rollback, explicit listener removal and accurate deployment observations.
- Live certificate mount permissions, rotation and rollback; live AWS token
  projection, audience isolation, metadata isolation and removal.
- A separate local API/dashboard restart preserved application configuration,
  revisions and accounts. All ten concurrent shop HTTP requests succeeded.

The TCP fixture reaches HAProxy through a local port-forward. It proves protocol
routing and source policy, not internet reachability or a cloud firewall. The
fixture resources and temporary ingress port were removed after acceptance.
No AWS API or recipient mailbox was used by these local checks.

An AWS/EC2 installation, its firewall, real IAM/OIDC role assumption and recipient
delivery still need the destination checks above. The existing SMTP deployment
has not been changed. Do not describe local acceptance as a production cutover.
