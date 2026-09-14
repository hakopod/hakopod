# Configure domains after deployment

Keep custom domain mappings in application TOML, including on first deployment
or Git import. Hakopod saves each new hostname as pending and provides its DNS
verification records under the application's Domains page. The application page
links to that setup when a desired domain is inactive.

Pending names do not reserve a hostname, create custom ingress rules or enter
certificate requests. The generated application hostname remains available.
Reserved installation names and names already owned by another application are
still rejected.

Add the displayed TXT record to prove ownership and the displayed CNAME target
for routing, then verify DNS. For an apex domain, use your DNS provider's supported
alias/flattening facility or route the host's address appropriately. A successful
verification is valid for one hour before first activation. Review and deploy a
new revision to activate verified mappings. Any accepted revision can activate
all verified mappings it contains; unverified mappings stay pending. Domain setup
is not evidence that a rollout, public DNS or certificate issuance has completed.

This behavior is shared by direct deployment, TOML, templates, Git imports,
webhook deployments and builds. The original desired TOML stays intact for source
reviews and idempotent retries. Runtime ingress and certificate host lists use
only the separately approved reservation set and fail closed if it is unavailable.

Remove a pending mapping through a reviewed domain change. Automatically staged
proofs are cleaned up when removed from desired configuration. An unused manual
setup record can also be discarded. Existing activated reservations remain owned
by the application for historical rollback safety even if a route is removed.

A workload that needs a certificate file to start, an AWS identity or an SMTP
probe helper still needs that prerequisite configured. Domain staging does not
remove these requirements or silently change an SMTP deployment into HTTP-only.
See [backend certificates](backend-certificates.md) and
[installation setup](installation-maintenance.md#smtp-listener-readiness).
