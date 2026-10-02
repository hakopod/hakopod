# Managed Supabase development evidence

Supabase remains unavailable. These tools can record evidence from the named development cluster, but they do not qualify a cluster or enable the runtime. The manifest keeps `development_cluster_qualified`, `cluster_qualified`, encrypted storage, public endpoint and physical-zone qualification false.

The committed evidence producer is `examples/supabase-native-acceptance/evidence.py`, invoked by the native runner. Handwritten reports and the legacy final receipt are rejected. Schema version 2 binds one random run ID to the exact producer and runner hashes, source inventory before and after the run, embedded assets, digest-pinned images, image identities, bounded timestamps, the cluster UID and sorted node names, source and recovery resource IDs, namespace names and UIDs, and creation operations. Each required case has a contiguous sequence, the same run ID, elapsed time and an evidence hash. The final report also binds hashes for the bounded sanitized log and event stream. Protected raw logs can contain credentials and must never be copied into qualification output or surfaced by the verifier.

Identity evidence has two independent sides. The native producer records the observed process UID and GID through a per-component observation hash. Release verification anonymously pulls each Linux AMD64 digest, reads `Config.User`, creates a network-disabled container without starting it, and copies `/etc/passwd` and `/etc/group` to resolve numeric or named users and groups. The resolved non-root UID/GID must equal the native report.

The cleanup receipt uses the same run ID and context. Both source and recovery entries repeat their exact platform ID, namespace, namespace UID and creation operation from the report, add the deletion operation, and prove namespace and persistent-volume absence. The recovery target must be a separate resource.

The normal create API generates the platform ID during review. Database certificates must cover that platform's namespace. Request the review without an ID, save its response in an owned mode-0600 file, then prepare certificates for the returned namespace before accepting the review. Set `HAKOPOD_ACCEPTANCE_CREATE_REVIEW_FILE` to that file when running the native harness. The harness checks its scope, full specification, create operation, revision zero, namespace, authority fields and expiry. The API still validates the persisted review when it accepts the operation. A changed specification or expired review requires a new review.

A test build that changes an availability gate is a candidate build. Record its changed files separately from the shipping inventory. A successful candidate run does not qualify the unchanged shipping source or enable Supabase.

The producer must emit the exact required case set. Missing cancellation recovery or ownership-fencing tests keep the record closed; labels or arbitrary passed events are not substitutes. Failed, skipped, duplicated, invented or missing cases are rejected. The development `local-path` StorageClass is recorded explicitly and cannot prove encryption, public routing or physical-zone behavior.

`behavior-checks.sh` supplies bounded live checks for signup redirect refusal, image transformation, Edge Function authentication, and Studio administrative authentication. It writes only compact structural evidence and removes temporary response bodies and transformed objects. `pooler-bounds.sh` checks transaction-listener client limits and observed database backend limits. `network-isolation.sh` tests allowed and forbidden TCP targets from the owned Studio workload. `ownership-check.sh` runs the native namespace adoption-refusal test and verifies cleanup. These checks still need a successful complete native run; missing or failed events prevent finalization.

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

Production qualification requires separate evidence from the approved encrypted StorageClass and a reviewed schema update. Enabling Supabase requires a separate reviewed source change after production qualification.
