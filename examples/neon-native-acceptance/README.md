# Neon native acceptance

This directory contains the source-bound evidence producer for Neon. `evidence.py` records only fixed live observation schemas. It does not accept event names, pass status, cluster identity, resource ownership, image identity, or cleanup claims from command-line flags.

The producer binds the active `k3d-hakopod-dev` cluster UID and sorted node inventory, exact source files, digest-pinned images, source and recovery-target namespace UIDs, and successful create operations. It reads every owned pod and process itself to verify image identity, UID/GID, Linux capabilities, no-new-privileges and seccomp. Finalization independently requires both namespaces and their persistent volumes to be absent.

The native runner currently fails closed before mutation. Shipping code keeps Neon unavailable, and the repository does not yet expose a reviewed in-process development-only capability injection that can run the real API and reconciler without changing shipping runtime source. The harness must not patch the API, normalize a candidate tree into the shipping inventory, accept handwritten passed events, or operate on an existing operator cluster.

The remaining driver must invoke the fixed producer boundaries after real checks for ownership refusal, mutual TLS and plaintext refusal, tenant/timeline/compute creation and deletion, fenced backup with all three safekeepers and every pageserver, restore into a separate empty target, storage and compute restart recovery, authority revocation, closed target readers, restore-token cleanup replay, and preservation of foreign resources. Qualification remains false until that complete run and independent release verification pass.
