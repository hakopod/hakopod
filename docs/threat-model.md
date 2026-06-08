# Threat model

The initial deployment is for one operator organization and trusted developers.
Namespaces and NetworkPolicies are authorization boundaries for ordinary
container traffic; they are not a guarantee against hostile tenants, kernel
exploits, host-network bypasses, or compromised cluster administrators.

## Credential boundaries

API keys contain a random 256-bit secret and lookup ID. PostgreSQL stores a
SHA-256 verifier and a short nonsecret prefix, not the key. This is appropriate
for uniformly random tokens; it is not a password-hashing scheme. Human session tokens
are encrypted inside a server-authenticated, HttpOnly, host-scoped session cookie.
The UI server validates session origin and forwards requests to one configured
API. It does not implement orchestration. Do not share dashboard origin with
untrusted application origins.

The CLI requires verified HTTPS except loopback development. Environment
credentials take precedence; missing CI context fails without prompting. Current
human login uses browser consent and an explicitly documented mode-0600 file
fallback. OS keychain integration remains release work.

Installation proof is required before claiming the first owner, and the claim
closes transactionally. The installer supplies their own identity. Passwords use
bcrypt with bounded concurrency. GitHub/Google logins require verified provider
identity and bound state; TOTP/recovery or verified WebAuthn ceremonies protect
enrolled accounts. TOTP keys use separate authenticated encryption, recovery
codes are consumed once, and provider MFA challenges have short lifetimes.
Teams grant project roles; disabled users, role reductions and revoked sessions
are rechecked on requests and before subsequent deployment effects.

The installation GitHub token is a shared privileged integration. A global
administrator must approve each application's repository before project deployers
can read its source configuration. Switching repositories requires renewed approval.
Writing a generated build workflow requires explicit browser administrator review;
changing build settings invalidates installation approval before dispatch.

Machine keys are named, expiring and scoped to project/environment, optionally
application. Deployment/log keys cannot administer nodes or keys and cannot
read secret values. The owning identity is rechecked, including disabling and
permission reductions. Executing application code can expose that application's
injected secrets: developers with deployment permission remain trusted for those
bindings. Stored Kubernetes Secrets are copied only through explicit authorized
service bindings; values are never returned by management listing endpoints.

## Workload boundaries

Containers run without service-account token mounts, privilege escalation,
host networking or host paths. Capabilities are dropped; seccomp defaults apply.
Resource requests and limits, rollout deadlines and a bounded management work
pool reduce accidental exhaustion. Image resolution rejects private/link-local
registry destinations, validates digest payloads and checks Linux architecture.
Registry credentials are scoped to project/environment and exact registry hosts.
Token-realm restrictions and stripped cross-host redirect authorization prevent
credential forwarding to arbitrary endpoints. Unknown fields are rejected.

DNS and declared network ports are explicitly allowed. External egress excludes
private, management and cloud-metadata ranges. `internal=true` removes ordinary
external egress for services that have only internal network membership. Ordinary
NetworkPolicies have host-traffic limitations; see ADR 0002. Image-pull, node
administration and cluster API traffic are platform operations, not workload
egress privileges. There is no claim of encrypted east-west application traffic.

## Remaining release risks

Local K3s kubeconfig is an operator credential; keep `.local` out of version
control. Local application ingress binds HTTP/HTTPS only on loopback. Public DNS,
ACME renewal, custom-domain ownership, real private-registry pulls and external
secret synchronization are not claimed operational by local tests. Arbitrary application logs can contain
secrets; users with logs:read must be trusted accordingly. Audit and deployment
retention, backup restoration, disk exhaustion, host installation and platform
upgrades require dedicated verification before a production release.
