# Threat model

The initial deployment is for one operator organization and trusted developers.
Namespaces and NetworkPolicies are authorization boundaries for ordinary
container traffic; they are not a guarantee against hostile tenants, kernel
exploits, host-network bypasses, or compromised cluster administrators.

## Credential boundaries

API keys contain a random 256-bit secret and lookup ID. PostgreSQL stores a
SHA-256 verifier and a short nonsecret prefix, not the key. This is appropriate
for uniformly random tokens; it is not a password-hashing scheme. Browser keys
are encrypted inside a server-authenticated, HttpOnly, host-scoped session cookie.
The UI server validates session origin and forwards requests to one configured
API. It does not implement orchestration. Do not share dashboard origin with
untrusted application origins.

The CLI requires verified HTTPS except loopback development. Environment
credentials take precedence; missing CI context fails without prompting. Current
human login uses an explicitly documented mode-0600 file fallback. OS keychain,
password authentication, invitations and full member administration are later
milestone work.

Machine keys are named, expiring and scoped to project/environment, optionally
application. Deployment/log keys cannot administer nodes or keys and cannot
read secret values. The owning identity is rechecked, including disabling and
permission reductions. Executing application code can expose that application's
injected secrets: developers with deployment permission remain trusted for those
bindings. This milestone rejects secrets until authorized bindings exist.

## Workload boundaries

Containers run without service-account token mounts, privilege escalation,
host networking or host paths. Capabilities are dropped; seccomp defaults apply.
Resource requests and limits, rollout deadlines and a bounded management work
pool reduce accidental exhaustion. Image resolution rejects private/link-local
registry destinations, validates digest payloads and checks Linux architecture.
Private registries are intentionally unavailable until scoped credential support
lands; unknown configuration fields are rejected.

DNS and declared network ports are explicitly allowed. External egress excludes
private, management and cloud-metadata ranges. `internal=true` removes ordinary
external egress for services that have only internal network membership. Ordinary
NetworkPolicies have host-traffic limitations; see ADR 0002. Image-pull, node
administration and cluster API traffic are platform operations, not workload
egress privileges. There is no claim of encrypted east-west application traffic.

## Remaining release risks

Local K3s kubeconfig is an operator credential; keep `.local` out of version
control. The local sample exposes HTTP only on loopback. Public DNS, ACME,
custom-domain ownership, private registry credentials and secret synchronization
are not claimed operational by local tests. Arbitrary application logs can contain
secrets; users with logs:read must be trusted accordingly. Audit and deployment
retention, backup restoration, disk exhaustion, host installation and platform
upgrades require dedicated verification before a production release.
