# Neon native acceptance

This directory contains the source-bound evidence producer for Neon. `evidence.py` records only fixed live observation schemas. It does not accept event names, pass status, cluster identity, resource ownership, image identity, or cleanup claims from command-line flags.

Compute templates must include numeric `spec.format_version`, integer `spec.suspend_timeout_seconds`, `spec.cluster.roles` and `spec.cluster.databases` lists, and `compute_ctl_config.jwks.keys`. Declare every proxy role in the role list before accepting an operation. The pinned provider creates new template roles with administrative privileges; review that list accordingly. Go copies each accepted proxy role's SCRAM verifier into the declared role and rejects undeclared roles. The provider enables login for those roles. Other role options remain as reviewed, and separate platforms can use different passwords with the same operator template. The database list may be empty; settings and other provider defaults may be omitted.

Go also derives compute control authority from the platform identity and control-plane encryption key, replacing the template's JWKS and token. The resulting Ed25519 token has scope `compute_ctl:admin` and audience `["compute"]`; the public key is isolated to that platform. Go owns listener, TLS and replication settings. Optional compute settings and supplied leaf TLS material remain intact. No signing key enters a compute snapshot. Fresh input validation must exercise both SQL and compute-control authentication binding, strict configuration and rendering.

The producer binds the active `k3d-hakopod-dev` cluster UID and sorted node inventory, exact source files, digest-pinned images, source, recovery-target and cancellation-target namespace UIDs, and successful create operations. It reads every owned pod and process itself to verify image identity, UID/GID, Linux capabilities, no-new-privileges and seccomp. Finalization independently requires all three namespaces and their persistent volumes to be absent.

`run.sh` invokes `driver.py` against a loopback server built with the Linux-only `hakopod_native_acceptance` tag. The server must already be running with its reviewed gate configuration, exact protected kubeconfig, and disposable control database. Shipping builds keep Neon unavailable. The driver creates the source and a separate empty target through `/api/v1`, binds their owned resources and process identities, and requires measured provider evidence before continuing. Several provider, lifecycle and recovery receipts are still being implemented. The driver refuses missing evidence; it has not completed a native run.

The complete run must verify provider ownership, client-side certificate-chain and hostname enforcement, the server certificate, plaintext refusal, real lifecycle transitions, backup and separate-target restore, storage and compute replacement, cancellation cleanup, and removal of all three disposable platforms. This transport observation does not prove mutual TLS or application authentication; those remain separate acceptance requirements. A distinct cancellation target is created after the successful restore, so cancellation cannot destroy the restored target used for verification. Finalization checks exact namespace and persistent-volume absence independently.

The driver accepts the API token only through a mode-600 file. Its work directory must be a fresh direct child named `hakopod-neon-native-*` under `/tmp` or `/srv/hakopod-backup-scratch`. The source, recovery target and fresh cancellation target specifications must be strict Neon specifications with distinct names and object-storage prefixes. Source identity comes from the gate attestation's exact file inventory and server binary digest; the runner has no caller-supplied tree labels. The inventory must contain exactly the eight digest-pinned components and matching qualified UID/GID/image records. `KUBECONFIG` must select `k3d-hakopod-dev`, whose identity and at least three nodes are independently bound by `evidence.py` and by the native server gate.

`HAKOPOD_ACCEPTANCE_PROJECT` must equal the project recorded in the gate
attestation. The runner passes that value explicitly to the driver so it cannot
silently use the driver's development default for API operations.

The managed TLS phase uses `managed-tls.py` and the compiled
`runtime-spec-digest.go` helper. It verifies the public trust response, all
seven active Neon certificate snapshots and their durable ownership claims,
then replaces the proxy snapshot with a short-lived leaf through the same
fenced runtime-mutation journal used by production maintenance. The run waits
for automatic renewal, requires stable CA trust and records which individual
snapshots changed. It does not assume that unrelated healthy leaves rotate.
The control-plane bridge uses a private CA, mutual TLS to its loopback relay,
and an exact namespace allowlist. The driver proves that the pinned proxy
client reaches PostgreSQL authentication with that CA and rejects a separately
issued CA with a certificate-verification error.

When trusted placement uses a dedicated pool, set
`HAKOPOD_ACCEPTANCE_SCHEDULING_POLICY` to a protected operator-owned JSON file,
separate from the customer specifications. Its exact fields are
`schema_version: 1`, `gate_sha256`, `scheduling_pool` and `runtime_class`.
`runtime_class` is either null or an object with `name`, `uid`, `handler`,
`node_selector` and `pod_fixed` (the observed `cpu` and `memory` overhead, or an
empty object when no overhead is declared). Bind this file's digest
in final preparation. The bridge verifies the gate digest and expiry, live
cluster and node UIDs, matching pool labels and NoSchedule taints, and the
RuntimeClass UID, handler, node selector and overhead before creating helpers.
Every attested node must satisfy that runtime selector. Both helper
Deployments use the explicit pool selector, required affinity to the attested
node names, one Equal NoSchedule toleration and the optional verified
RuntimeClass. Other blocking taints are refused. Empty policy preserves the default
scheduling behavior. Cleanup does not require an unexpired scheduling gate.

For development clusters on separate hosts, place a protected `host-binding.json`
beside the bridge's TLS files. Use `schema_version: 1`, the host's private IPv4
`address`, `haproxy_path: "/usr/sbin/haproxy"`, its exact `haproxy_sha256`, and
the installed `haproxy` user's numeric `uid` and `gid`. The helper checks the
binary, service identity and assigned address before use. It runs native
HAProxy as that user with a three-hour limit, 128 MiB of memory and half a CPU.
The relay certificate must cover that IP address and chain to the bridge CA.
The bridge uses the same address for routing, certificate verification and its
single-host egress rule. Without this file, the existing Docker-host bridge
continues to use `172.18.0.1`. Include the binding in the protected run inventory.

Qualification remains false until this complete destructive run succeeds on the named development cluster and its output passes the independent release verifier. The driver does not turn a source-only review, a blocked two-node run, or a development fixture into runtime qualification.
