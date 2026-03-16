# ADR 0002: Namespaced Services with enforced, additive policy

Status: implemented. Runtime verification is reported separately in the acceptance record.

Each application/environment has a deterministic namespace derived from its immutable application ID, rather than its mutable display name. Short service DNS names are therefore scoped naturally. Only an explicit public HTTP service receives an Ingress. A declared port creates a private ClusterIP Service, never a NodePort.

The local profile uses K3s Flannel VXLAN plus K3s's embedded kube-router NetworkPolicy controller. Flannel alone is not the enforcement mechanism. `--disable-network-policy` must not be set. IPv4 is the supported profile; IPv6 installation support requires separate policy tests. No east-west encryption is claimed.

The reconciler establishes a default deny policy before any workloads, then adds per-service ingress/egress for shared named networks and declared destination ports. Omitted membership becomes `default`; an explicit list replaces it. Only namespace-scoped authorized membership labels can match. DNS exceptions permit UDP/TCP 53 to CoreDNS pods in `kube-system`. HAProxy pods with the expected release labels may reach public service ports. Policies are additive: a source's egress and a destination's ingress must both permit a flow.

Services with any non-internal membership may reach external IPv4 destinations, excluding private, loopback, link-local, CGNAT, multicast and reserved networks. This prevents the internet exception from granting access to K3s pod/service networks or cloud metadata. Services attached only to `internal = true` networks have no general external egress. Attaching an egress-enabled network restores that egress. Cross-application sharing is unsupported.

The development host's UDP DNS upstream was unreachable from containers. A K3s-supported `coredns-custom` override forwards public names over TCP to operator-editable resolvers and bounds concurrency to 64. No additional DNS proxy or dependency was added. This changes CoreDNS's upstream transport only; application DNS traffic still uses the scoped CoreDNS policy exception. Both external and internal resolution were verified afterwards.

Kubernetes applies policies asynchronously, and an existing connection can outlive a policy change. A short bootstrap settle delay reduces exposure but is not a security proof. Standard NetworkPolicy has host-traffic limitations; local-node traffic and host-network workloads need host firewall/CNI-specific controls. This is a trusted-team platform, not isolation for hostile public tenants. Kubernetes identity restrictions and no application service-account token remain independent defenses.

`scripts/network-acceptance.py` checks actual generated resources: service DNS, direct pod-IP access, a private service with no Ingress, denied outsider ingress, denied application egress to another namespace, metadata/control-plane exceptions, and stable Service DNS after a pod replacement. It uses an unrestricted outsider to prove that a denial is not merely the source's egress policy. Those checks passed on the arm64 local cluster on 2026-09-12. `scripts/cross-node-acceptance.py` separately passed permitted and denied Service/pod-IP traffic between explicitly placed server and worker pods, including DNS after replacement. The worker and fixtures were removed. These are two containerized nodes on one host; physical node connectivity and host firewall behavior remain unverified.

The public web service proxies browser requests to the private API. `http://api:8080` is for server-side code; a browser cannot resolve cluster-private DNS.

The opt-in Go live tests additionally passed shared-network service/pod-IP access, disjoint-network denial in both directions, internal-only internet egress denial, mixed-network internet egress permission, and external DNS resolution from an internal-only service. These exercise the actual Go-generated policies on the local cluster. HPA ownership was verified separately by retaining two runtime replicas despite a canonical count of one and preserving a reload annotation.

Sources:

- https://docs.k3s.io/networking/networking-services#network-policy-controller
- https://docs.k3s.io/networking/basic-network-options
- https://docs.k3s.io/advanced#coredns-custom-configuration-imports
- https://coredns.io/plugins/forward/
- https://kubernetes.io/docs/concepts/services-networking/network-policies/
- https://kubernetes.io/docs/concepts/services-networking/dns-pod-service/
- https://docs.docker.com/reference/compose-file/networks/
