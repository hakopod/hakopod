# ADR 0004: Explicit external prerequisites and deferred privileged workflows

Status: local exposure and K3s encryption implemented; production certificate, secret-provider and enrollment workflows deferred.

## Public exposure and domains

The development cluster has one NodePort Service for HAProxy HTTP/HTTPS. k3d's TCP forwarding container exposes it only on host loopback `18080`/`18443`. This is one local forwarding chain, not multiple Kubernetes load-balancer products. The Kubernetes API binds to loopback `16443`. The PostgreSQL container binds only loopback `55432`. HAProxy administrative/statistical ports are absent from the public Service.

Production needs an operator-owned application domain with a wildcard DNS record pointing at a reachable ingress IP, appropriate NAT, inbound 80/443 and node connectivity. `hakopod.com` is a project name, not an installation entitlement. Management and application origins must be separate. Additional ingress pods do not make a single public endpoint highly available; an external load balancer or tested network-compatible VIP is a separate decision.

Milestone 1 local URLs are HTTP. No custom-domain ownership, conflict-resolution or ACME workflow is advertised yet. cert-manager should own renewal; automated tests must use an ACME test/staging issuer first. HTTP-01 requires a publicly reachable hostname on port 80; wildcard certificates require DNS-01. A localhost wildcard cannot receive a public ACME certificate.

## Secrets

K3s starts with `--secrets-encryption`; `k3s secrets-encrypt status` reports encryption enabled in the tested local cluster. This protects Kubernetes Secret data in the datastore, not application logs or process memory. Applications receive no service-account credentials. Local database credentials and bootstrap keys remain in ignored mode-0600 files under mode-0700 `.local`.

Built-in secret CRUD/bindings, encryption-key backup/rotation and external synchronization are release work. Missing secret references must fail a new deployment, not inject empty values. The intended Infisical integration uses its operator's current `InfisicalConnection`, `InfisicalAuth`, `InfisicalStaticSecret` APIs, scoped machine identities and namespace authorization. No version/CRD combination is pinned or called compatible until actually installed and tested. No paid service is needed for Milestone 1.

## Enrollment

No dashboard worker enrollment is advertised. k3d node operations are local test administration. The K3s server token is permanent bootstrap material and must never be placed in a browser-generated join command. A future enrollment workflow must issue scoped expiring bootstrap tokens, validate cluster identity, implement revocation, and separately test any single-use claim. Node drain must surface PDB blockers instead of force-deleting pods. Worker count does not provide control-plane HA.

Sources:

- https://docs.k3s.io/security/secrets-encryption
- https://docs.k3s.io/cli/token
- https://cert-manager.io/docs/configuration/acme/http01/
- https://cert-manager.io/docs/configuration/acme/dns01/
- https://infisical.com/docs/integrations/platforms/kubernetes/overview
- https://kubernetes.io/docs/tasks/administer-cluster/safely-drain-node/
