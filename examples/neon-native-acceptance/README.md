# Neon native acceptance

This directory contains the source-bound evidence producer for Neon. `evidence.py` records only fixed live observation schemas. It does not accept event names, pass status, cluster identity, resource ownership, image identity, or cleanup claims from command-line flags.

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

Qualification remains false until this complete destructive run succeeds on the named development cluster and its output passes the independent release verifier. The driver does not turn a source-only review, a blocked two-node run, or a development fixture into runtime qualification.
