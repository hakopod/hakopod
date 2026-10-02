# Managed Supabase release and deployment checks

Supabase remains unavailable while its release gate is closed. The native runner checks the runtime on the named development cluster. The release verifier checks that the source and images being published are the ones that passed that run. A separate operator approval binds each deployment to its cluster and storage.

The manifest records `release_runtime_qualified` only when the tested source has an open release gate and its image inventory matches the compiled release inventory. Changing the gate or an image after acceptance requires another native run. Candidate evidence with a closed gate can be saved for review, but the release verifier rejects it before pulling images or writing release output. A completed runtime check does not approve another cluster's storage, public ingress or physical zones; those capability fields remain false in the release evidence.

The committed evidence producer is `examples/supabase-native-acceptance/evidence.py`, invoked by the native runner. Handwritten reports and the legacy final receipt are rejected. Schema version 2 binds one random run ID to the exact producer and runner hashes, source inventory before and after the run, embedded assets, digest-pinned images, image identities, bounded timestamps, the cluster UID and sorted node names, source and recovery resource IDs, namespace names and UIDs, and creation operations. Each required case has a contiguous sequence, the same run ID, elapsed time and an evidence hash. The final report also binds hashes for the bounded sanitized log and event stream. Protected raw logs can contain credentials and must never be copied into qualification output or surfaced by the verifier.

Identity evidence has two independent sides. The native producer records the observed process UID and GID through a per-component observation hash. Release verification anonymously pulls each Linux AMD64 digest, reads `Config.User`, creates a network-disabled container without starting it, and copies `/etc/passwd` and `/etc/group` to resolve numeric or named users and groups. The resolved non-root UID/GID must equal the native report.

The cleanup receipt uses the same run ID and context. Both source and recovery entries repeat their exact platform ID, namespace, namespace UID and creation operation from the report, add the deletion operation, and prove namespace and persistent-volume absence. The recovery target must be a separate resource.

The normal create API generates the platform ID during review. Database certificates must cover that platform's namespace. Request the review without an ID, save its response in an owned mode-0600 file, then prepare certificates for the returned namespace before accepting the review. Set `HAKOPOD_ACCEPTANCE_CREATE_REVIEW_FILE` to that file when running the native harness. The harness checks its scope, full specification, create operation, revision zero, namespace, authority fields and expiry. The API still validates the persisted review when it accepts the operation. A changed specification or expired review requires a new review.

A test build that changes an availability gate is a candidate build. Record its changed files separately from the shipping inventory. A successful candidate run does not qualify the unchanged shipping source or enable Supabase.

The producer must emit the exact required case set. Missing cancellation recovery or ownership-fencing tests keep the record closed; labels or arbitrary passed events are not substitutes. Failed, skipped, duplicated, invented or missing cases are rejected. The development `local-path` StorageClass is recorded explicitly and cannot prove encryption, public routing or physical-zone behavior.

`behavior-checks.sh` supplies bounded live checks for signup redirect refusal, image transformation, Edge Function authentication, and Studio administrative authentication. It writes only compact structural evidence and removes temporary response bodies and transformed objects. The native runner also verifies that postgres-meta can manage an owned public table while protected schemas and unowned tables remain unavailable. `pooler-bounds.sh` checks transaction-listener client limits and observed database backend limits. `network-isolation.sh` tests allowed and forbidden TCP targets from the owned Studio workload. `ownership-check.sh` runs the native namespace adoption-refusal test and verifies cleanup. These checks still need a successful complete native run; missing or failed events prevent finalization.

The producer is a command-line state machine. `begin` creates protected state and event files and observes the development cluster. `bind-resource` consumes a safe successful-operation projection, then reads the live namespace UID and ownership labels itself. `observe-identity` reads the owned pod, the image `Config.User` and account files, and the live process UID, GID, capabilities, no-new-privileges and seccomp state itself. `event` accepts one fixed case and one safe structural observation file only after that check succeeds. `fail` preserves a safe failure code without recording raw output. After both resources are deleted, `finalize` consumes safe successful-delete projections, independently checks namespace and persistent-volume absence, rejects incomplete evidence, and atomically writes the cleanup receipt followed by the final report. The native runner owns invocation at the real check boundaries.

After the producer has completed a real native run and cleanup in `k3d-hakopod-dev`, record its protected structural outputs from the exact final source tree:

```sh
python3 release/record-supabase-qualification.py \
  --source /path/to/final-source \
  --report /protected/native-result.json \
  --cleanup /protected/cleanup-receipt.json \
  --output release/managed-supabase
```

The recorder rejects duplicate JSON keys, booleans in integer fields, symlinked artifacts or ancestors, stale sources and reused output paths. It builds a temporary sibling directory and renames it only after the complete record validates.

Release verification runs on the validation VM or in CI:

```sh
python3 release/verify-supabase-runtime.py \
  --qualification release/managed-supabase \
  --output /fresh/verification-output
```

Docker output is read with byte and time limits. Failed commands never expose raw registry output. Temporary metadata containers use `--network none`, are never started, and are removed after account resolution. Verification output is also created atomically.

## Production operator deployment contract

Development evidence never authorizes a production cluster. A production
operator configuration must bind the reviewed Supabase release to the live
cluster before the catalog can report it available. The binding records the
`kube-system` namespace UID as the cluster UID, the exact StorageClass name and
UID, its provisioner, and a canonical SHA-256 digest of its parameters. It also
records the complete component-to-image inventory; every image remains pinned
by digest. Startup and every apply re-read those Kubernetes objects and refuse
the operation when any bound value differs.

The operator's encrypted-storage approval is an authorization decision. Its
hashes make that decision tamper-evident; they are not cryptographic proof of
the provider's encryption. The reviewed deployment record must separately cite
provider evidence for the backing medium. For an Azure deployment, that evidence
identifies the managed disk and its encryption setting, then traces the disk's
LUN through the host mount and Kubernetes volume to the workload. A StorageClass name, parameter
such as `encrypted=true`, development `local-path` storage, or a self-authored
digest is insufficient.

The binding is a nested section of the existing managed-platform runtime TOML:

```toml
[supabase_qualification]
schema_version = 1
release = "supabase-0.8.2-linux-amd64"
cluster_uid = "<kube-system namespace UID>"
storage_class_name = "<approved StorageClass>"
storage_class_uid = "<StorageClass UID>"
storage_provisioner = "disk.csi.azure.com"
storage_parameters_sha256 = "<SHA-256 of canonical JSON string map>"
image_inventory_sha256 = "<SHA-256 of the complete sorted component=image inventory>"
provider_evidence_sha256 = "<SHA-256 of the separately verified provider evidence>"
encryption_at_rest = true
reviewed = true
```

The server rejects unknown TOML fields. The storage parameter digest uses the
JSON encoding of the Kubernetes `StorageClass.parameters` string map. The image
digest uses every supported Supabase component in the product's fixed order as
`component=image`, joined with line feeds. Operators should generate these
values from the reviewed cluster and release files, not type them from memory.

The shipping source has an independent Supabase release gate and a fixed image
inventory. The release verifier binds the native evidence to that exact source
and checks each image digest and process identity.
The operator binding cannot override a closed release gate, and a release gate
cannot override a missing or changed live-cluster binding. Only when both pass
may the plan set `available` and `cluster_qualified` true. The supported
deployment still sets `public_qualified` false and makes no physical-zone or
multi-zone claim. Public ingress and zone-failure behavior require separate
evidence and a separate reviewed gate.

The deployment procedure is therefore:

1. Verify the release qualification manifest and exact image digests on the
   validation VM or in CI.
2. Inspect the target cluster UID, StorageClass UID, provisioner and canonical
   parameters, then inspect the provider's backing volume evidence.
3. Write the reviewed mode-0600, strict-versioned operator configuration with
   those exact values and the complete release image inventory.
4. Start the API and reconciler in the same process. Startup must fail closed
   if the live binding differs.
5. Review and apply a platform operation. The planner and reconciler must
   recheck the binding before sealing or using the durable operation snapshot.
6. Record the operation and live workload result separately from the release
   and storage evidence.

No production cluster mutation is part of the development acceptance run.
