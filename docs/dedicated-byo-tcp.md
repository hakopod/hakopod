# Public TCP on dedicated Cloud BYO nodes

Shared managed Cloud installations reject public TCP. A Cloud operator may bind an isolated worker control plane to exactly one registered node using `HAKOPOD_DEDICATED_TCP_NODE`. The setting is never accepted in application TOML. The engine retains managed Cloud resource/feature limits.

On this explicitly bound control plane, administrators provision ports on the owned HAProxy Deployment and Service. Application plans require those ports to exist and the ingress rollout to be ready. The self-hosted static `HAKOPOD_PUBLIC_TCP_PORTS` allowlist is unchanged; a dedicated Cloud worker uses actual administrator-provisioned ports instead. Reserved platform ports still cannot be assigned.

Each TCP plan and reconciliation checks that the cluster contains only the expected node. An empty agentless cluster may start before its worker joins; it cannot deploy public TCP until that node exists. Additional nodes, a different node or unavailable inventory fail closed. Existing atomic claims, source CIDRs, backend certificate handling and reload acknowledgement apply to BYO traffic.

Verification: full Go suite with an isolated PostgreSQL database and unit tests for exact-node matching, unexpected nodes, shared Cloud denial and administrator provisioning. The named k3d-hakopod-dev acceptance passed real HAProxy forwarding, STARTTLS hostname/certificate verification, source denial, conflicting application rejection, rollback and listener removal under the dedicated BYO policy. Provider firewall/NAT and external SMTP delivery remain environment-specific.
