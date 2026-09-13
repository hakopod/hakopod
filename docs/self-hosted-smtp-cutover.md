# Self-hosted SMTP cutover checks

[Issue 9](https://github.com/hakopod/hakopod/issues/9) adds two opt-in controls:
automatic backend certificate renewal and listener readiness independent of an
HTTP health endpoint. Public SMTP remains self-hosted only, with administrator
provisioning of every public port. Managed Hakopod Cloud does not expose it.

Use [backend certificate mounts](backend-certificates.md) to follow a service's
own ingress certificate with `source = "ingress"`. Keep manual uploads pinned
when certificate changes need separate deployment approval. The service must
already have a valid HTTP TLS ingress; a public TCP mapping alone is insufficient.

Use [listener readiness](readiness.md) to require a successful SMTP or STARTTLS
check alongside `/health`. A working HTTP endpoint alone must not make a failed
SMTP listener ready. This controls ready endpoints and rollout completion; it
neither submits mail nor proves the complete sending path.

For Xem's singleton backend, set `replicas = 1` and
`update_strategy = "recreate"`. Keep autoscaling disabled. Stop-first replacement
avoids overlapping queue runtimes and introduces an interruption during renewal
or deployment. Clients need retries, and backups and application migration
behavior still need verification before cutover.

## Verification

The isolated `k3d-hakopod-dev` fixtures verified:

- HTTP success with a stopped SMTP listener remains unready.
- SMTP and HTTP failure/recovery remove and restore ready Service endpoints.
- STARTTLS validates certificates and rejects a wrong hostname.
- Ingress certificate renewal changes the certificate actually served by SMTP
  without changing the accepted application revision.
- A reviewed ingress certificate replacement completes its backend restart
  before the deployment reports success.
- Invalid replacement key data keeps the previous working mount and reports
  unhealthy delivery state.
- Both rolling and Recreate renewal paths preserve nonroot, read-only certificate
  access and do not grant Kubernetes credentials to the application.

The PostgreSQL integration test verifies that runtime maintenance and deployment
workers cannot hold the same application claim, and a queued revision revokes
maintenance eligibility. Unit tests cover ownership, Cloud restrictions, schema
validation, bounded protocol parsing and certificate cleanup references.

These fixtures use a synthetic SMTP server, an isolated certificate source and
explicit fixture trust. They do not request an ACME certificate, send mail, test
production Xem or verify public DNS, firewall rules, AWS identity, inbound
feedback or outbound deliverability. Keep the existing production service until
those installation-specific checks pass.
