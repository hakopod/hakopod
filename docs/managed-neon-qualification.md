# Managed Neon qualification

Hakopod keeps Neon unavailable until a release has complete, producer-bound native evidence. The recorder does not enable Neon or change the runtime capability in `internal/managedplatform/neon_runtime.go`. The committed producer records fixed observations and rejects caller-authored pass events. The runner still exits before mutation because the shipping server does not yet expose the reviewed in-process development-only capability injection needed to exercise the real API and reconciler while leaving shipping runtime source unchanged.

The record binds the upstream Neon repository and commit, the reproducible archive digest, byte size, member count and ordering checks, both reviewed patches and their frozen combined tree, and a complete Hakopod source inventory. That inventory covers `internal`, `templates`, `cmd`, `hack`, `scripts`, the Go module files, and the Neon recorder, verifier and qualification document. Generated qualification output is outside the inventory, avoiding a circular manifest.

The build report is a schema-versioned JSON object. It must identify native `linux/amd64`, the upstream source and candidate tree, archive metadata, proxy, ownership and combined patch hashes, all eight component image references, and the UID/GID bound to each component image. Each of the `storage`, `compute-tools`, and `compute-runtime` stages must provide an immutable published image reference, manifest and config digests, and a complete absolute-path binary hash map. Rejected archives listed in `hack/managed-neon/source-metadata.toml` are not accepted by substituting their digest for the canonical archive.

The native report must come from context `k3d-hakopod-dev` with at least three named nodes and carry identical source inventories before and after the run. It binds the committed runner and producer hashes, a unique run ID, bounded elapsed time, cluster UID, exact images, and observed process identities. Each source and recovery target is bound to its platform ID, namespace UID and successful create operation. Finalization independently checks namespace and persistent-volume absence.

The fixed observations cover ownership capability, mutual TLS and plaintext refusal, tenant/timeline/compute lifecycle, backup/recovery, restart/failure and revocation/cleanup. Neon recovery evidence uses `hakopod-neon-recovery-v1` and requires the ordered parts `tenant.json`, `timeline.json` and `remote-storage.tar`. It binds tenant and timeline generations, a nonzero commit LSN, two to eight pageserver remote-consistent LSNs at or beyond the commit, source object prefix, deterministic inventory hash, bounded object count and bytes, and restored data hash. Revocation evidence requires compute, proxy and readers to remain closed, cleanup replay to remove restore-token-owned resources and the staging prefix, and foreign resources to remain intact.

The development gate is a separate acceptance-server concern. Its attestation must identify the exact shipping tree and acceptance-server tree and prove that only the in-process capability decision differs. Product API handlers, reconciler, renderer, recovery implementation and manifests must come from the shipping inventory. A patched or normalized product tree cannot qualify the shipping source.

Create the record only after the real reports exist:

```sh
python3 release/record-neon-qualification.py \
  --source "$PWD" \
  --source-archive /srv/hakopod-backup-scratch/neon-packaging-v2/output/neon-provider-source.tar.gz \
  --build-report /path/to/build-provenance.json \
  --report /path/to/native-acceptance.json \
  --output /path/to/fresh-managed-neon-record
```

Release verification rechecks the sealed source and evidence, then anonymously pulls every digest for native `linux/amd64` inspection:

```sh
python3 release/verify-neon-runtime.py \
  --qualification /path/to/fresh-managed-neon-record \
  --output /path/to/fresh-verification
```

Run the focused test suite on the build VM with its CPU, memory, time, and output bounded. The suite does not contact or mutate a cluster:

```sh
systemd-run --user --wait --collect \
  --property=CPUQuota=100% --property=MemoryMax=1G \
  --property=RuntimeMaxSec=300 --property=StandardOutput=journal \
  python3 scripts/test-neon-qualification.py
```

A completed development record sets only `development_evidence_recorded`. `cluster_qualified`, `public_endpoint_qualified`, and `physical_zones_qualified` remain separately false. Production qualification requires a reviewed evidence producer and a separate schema change; the product runtime remains unavailable.
