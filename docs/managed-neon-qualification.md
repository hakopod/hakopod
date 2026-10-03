# Managed Neon qualification

Hakopod keeps Neon unavailable until a release has complete native evidence tied to its source and images. The recorder does not enable Neon or change the runtime capability. The producer records fixed observations and rejects caller-authored pass events. The native driver creates only admitted development resources and refuses missing provider evidence. Read the qualification manifest shipped with a release for its recorded results; this guide defines the checks that record must pass.

The version 2 qualification manifest records `release_runtime_qualified` only when the tested source has an open release gate and its complete image inventory matches the compiled release inventory. The compiled archive digest, PostgreSQL commit and compatibility patch must also match the build provenance. Changing the gate, image list or source after acceptance requires a new native run. Closed-gate evidence can be recorded for review; release verification rejects it before pulling images or creating release output.

The record binds the upstream Neon repository and commit, the reproducible archive digest, byte size, member count and ordering checks, the reviewed patches and their frozen combined tree, and a complete Hakopod source inventory. That inventory covers `internal`, `auth`, `templates`, `cmd`, `hack`, `scripts`, the Go module files, and the Neon recorder, verifier and qualification document. Generated qualification output is outside the inventory, avoiding a circular manifest.

The build report is a schema-versioned JSON object. It must identify native `linux/amd64`, the upstream source and candidate tree, archive metadata, all reviewed patch hashes, all eight component image references, and the UID/GID bound to each component image. The patches cover proxy timeouts, ownership, PostgreSQL compatibility, storage TLS, compute reconfiguration and committed controller placement. Each of the `storage`, `compute-tools`, and `compute-runtime` stages must provide an immutable published image reference, manifest and config digests, and a complete absolute-path binary hash map. Rejected archives listed in `hack/managed-neon/source-metadata.toml` are not accepted by substituting their digest for the canonical archive.

## Selected PostgreSQL source and rejected prebuilt candidate

Neon publishes compute images with its PostgreSQL fork and extensions. Reusing those layers can avoid rebuilding PostgreSQL and the extensions. A complete Neon deployment also needs its storage services, controller and proxy; a compute image alone does not provide the service.

The selected candidate builds PostgreSQL 17.11 from the pinned source and compatibility patch recorded in `hack/managed-neon/source-metadata.toml`. Its compute runtime and patched `compute_ctl`, `fast_import` and `local_proxy` tools share the source archive provenance required by the version 1 build record.

The official amd64 base `ghcr.io/neondatabase/compute-node-v17@sha256:9b86e3ecb2267fbdeb0fd2478db0e662959ccdfa4526efa4a558410a93c6c46f` was inspected on 2026-10-02 and contained PostgreSQL 17.5. That historical compatibility candidate was rejected: it did not meet the PostgreSQL 17.11 security baseline, and adding separately built tools would produce mixed source provenance that the build record cannot admit. The latest public compute tags found in the [Neon registry](https://github.com/neondatabase/neon/pkgs/container/compute-node-v17) were published in September 2025. See [PostgreSQL's version policy](https://www.postgresql.org/support/versioning/) for current minor releases.

An official base and separately built tools have different source provenance. Any future record must bind the base manifest and config, its source and SBOM statements, the patched tools' source and binary hashes, the final image, and the unchanged base layers. Digest checks establish which statement was read; they do not establish the identity of its signer. Matching repeated overlay outputs also does not prove that the official base can be rebuilt from source.

Reusing a prebuilt base would require a reviewed build-record change, a current security baseline and the same native acceptance checks. Its capture must also check each added binary's ELF interpreter, shared libraries and required symbol versions against the exact base filesystem before assembly. Downloading or assembling an image does not enable a runtime capability.

## Native acceptance

The native report must come from context `k3d-hakopod-dev` with at least three named nodes and carry identical source inventories before and after the run. It binds the committed runner and producer hashes, a unique run ID, bounded elapsed time, cluster UID, exact images, and observed process identities. The source, recovery target and separate cancellation target each bind their platform ID, namespace UID and successful create operation. Finalization independently checks namespace and persistent-volume absence for all three.

The fixed observations cover ownership capability, server certificates, client-side certificate and hostname checks, plaintext refusal, tenant/timeline/compute lifecycle, tenant isolation, scoped SQL authentication, connection limits, primary and replica behavior, WAL quorum fencing, controller recovery, object-store outage handling, backup/recovery, restart/failure and cancellation cleanup. Native TLS inspection includes the HTTPS broker and the PostgreSQL controller database in addition to the storage-controller, pageserver, safekeeper, compute-control and proxy listeners. It requires verified owned hostnames and independently rejects an empty trust pool, a mismatched hostname and plaintext. The SQL checks use distinct protected passwords for the source, recovery target and cancellation target. They require invalid and cross-platform credentials to fail through the proxy, while keeping password values out of command arguments and evidence. These checks do not establish mutual TLS.

The connection probe opens a separate authenticated observer before saturation. It reads the role's superuser and reserved-slot access, both reserved-connection settings, and the exact existing client backend PIDs, including the observer. The held count must equal the resulting available capacity, with the baseline unchanged. Each held client must flush its backend PID marker before sleeping; bounded fresh observations independently match those PIDs to active `pg_sleep` backends in `pg_stat_activity`. Every attempted client must be held or return a specific PostgreSQL connection-capacity refusal. Authentication, TLS and timeout errors cannot count as capacity refusals. The probe closes all clients and requires a successful query afterward. With two computes, `compute-0` must accept writes and `compute-1` must remain read-only and receive the source marker.

The WAL probe scales two exact owned safekeeper StatefulSets to zero. Immediately before the write and after its refusal, it rechecks both StatefulSet UIDs and zero replicas, the old pod UIDs' absence and the absence of replacement pods across the whole namespace. It restores both StatefulSets in `finally`, checks that the refused write did not commit, and verifies a fresh write succeeds. A timeout alone does not prove that a write was absent. The controller probe replaces the storage-controller pod and requires the tenant and timeline identities and generations to remain unchanged. The restart probe asks the controller which pageserver owns the tenant and matches its node ID to an owned registration. It rechecks that attachment before deleting the active pageserver and `compute-0` by pod UID. It accepts only ready replacements with the same components and StatefulSet owners after both old UIDs disappear. An unchanged replica cannot stand in for the restarted writer or active storage. A disposable object-store fault must make a backup fail; a nonterminal operation is cancelled before the store is restored.

Neon recovery evidence uses `hakopod-neon-recovery-v1` and requires the ordered parts `tenant.json`, `timeline.json` and `remote-storage.tar`. It binds tenant and timeline generations, a nonzero commit LSN, the attached pageserver's remote-consistent LSN at or beyond the commit, source object prefix, deterministic inventory hash, bounded object count and bytes, and restored data hash. Secondary pageservers share remote storage but do not serve the unsharded tenant, so capture requires the active pageserver's checkpoint. The restored target then receives a reviewed resource update that adds 100 millicores to compute CPU within the acceptance grant. The operation and platform revisions must advance exactly once. Resubmitting the stale request with a new idempotency key must return a conflict, and reviewing a topology change at the new revision must be rejected without another revision advance. The namespace, tenant, timeline and provider generations must remain unchanged; both ready compute pods must have the new CPU requests and limits; the writer, read-only replica and restored SQL marker must survive. WAL, controller and restart observations bind to that recovery target, while isolation and source lifecycle observations bind to the source.

The migration check moves the restored tenant to another owned pageserver before the restart check. It requires a higher committed generation with the same tenant owner, updated routing for every compute, and the same SQL data on the primary and replica. The callback reads the controller's committed placement through an authenticated endpoint. Its debug view cannot provide that proof: during a live move, the in-memory generation is updated only after the callback returns. Evidence contains no ownership token.

Cancellation evidence requires compute, proxy and readers to remain closed, a terminal cleanup journal, no operation lease, an empty staging prefix, and foreign resources to remain intact. It does not prove credential revocation after platform deletion.

The proxy check also runs an authenticated SQL query through the managed endpoint. It supplies the platform ID in the PostgreSQL startup options and verifies the proxy's certificate and hostname. A separate wrong-issuer probe must fail certificate verification before authentication. Reaching a password challenge alone cannot pass this check. The application password comes from a protected file and never appears in command arguments or evidence.

The `hakopod_native_acceptance` build tag compiles a separate Linux test binary from the same source. A normal shipping binary rejects native acceptance configuration. The tagged binary admits one named development project, one platform kind, and the `k3d-hakopod-dev` context. Before opening PostgreSQL or running migrations, it verifies the protected run configuration, exact binary and source files, cluster and node UIDs, a loopback API address, and a disposable control database named for that run. It stops at the configured expiry, which cannot exceed four hours. Public endpoint qualification stays false.

The run attestation must bind the unchanged source inventory, build tag and binary digest. Product API handlers, authorization, reconciler, renderer, recovery code and manifests use that inventory. An edited candidate tree cannot qualify shipping source, and successful development acceptance alone does not enable a feature in a release.

Create the record only after the real reports exist:

```sh
python3 release/record-neon-qualification.py \
  --source "$PWD" \
  --source-archive /srv/hakopod-backup-scratch/neon-packaging-v2/output/neon-provider-source.tar.gz \
  --build-report /path/to/build-provenance.json \
  --report /path/to/native-acceptance.json \
  --output /path/to/fresh-managed-neon-record
```

Release verification rechecks the sealed source and evidence, reads each immutable registry manifest through Docker Buildx to verify its exact bytes and config descriptor, then anonymously pulls every digest for native `linux/amd64` image, user and binary inspection:

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

A completed record can qualify the release runtime; it does not approve a target installation. `development_cluster_qualified`, `cluster_qualified`, `encrypted_storage_class_qualified`, `public_endpoint_qualified` and `physical_zones_qualified` remain false in that record. Release verification separately checks anonymous image pulls and writes `deployment_qualified: false`.

Each operator must also supply the strict `neon_qualification` configuration described in the [operator guide](managed-platform-operations.md). It binds the release, image inventory, cluster UID and exact StorageClass to reviewed provider evidence for encryption at rest. Startup, planning and execution recheck that binding. Neither a successful release run nor an operator binding can override the other requirement. Public endpoints and availability across physical zones require separate acceptance and remain unavailable.
