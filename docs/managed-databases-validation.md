# Managed database validation

This records development acceptance for the unreleased managed database change. It is not production deployment evidence.

## Durable state and dashboards

The complete engine and Cloud Go suites ran against isolated PostgreSQL databases. Dedicated store checks cover serialized database/application memory and storage reservations, failed shrink accounting, authorization, leases, idempotency, immutable reviews, stale connection replacement and unused recovery targets. Recovery reviews and acceptance reject both saved connections and an older successful deployment left running after a failed replacement.

Both the public dashboard and the composed Cloud dashboard passed their production build, TypeScript check and 93 tests. Cloud's shared Python suite passed 60 tests, including current-worker sandbox attestation and the installation-owned native CSI runtime. The generated OpenAPI document matches its generator.

Independent UI reviewers inspected rendered screenshots and actual element bounds in dark and Paper themes on desktop and mobile. Database lists/details, creation, resizing, connections, backup source selection, recovery, inspection, stored archives and Docker imports were covered. Narrow states were checked at 320 pixels, alongside 390 and 1484 pixels. Keyboard/touch help, confirmation gates, retained form values, scoped navigation, route resets and failures were exercised. The shared inspection flow used a real recovered PostgreSQL 18 database; a read-only query returned the imported row and zero invalid indexes.

The Cloud-only continuation was reviewed at all three widths in both themes using a real nonoperator customer session. An independent development identity approved the import metadata through the actual API. From an approval opened with no workspace selected, Continue archive upload selected the approval's workspace, reloaded and loaded the exact saved import. A failed selection produced a persistent inline alert. The reviewer performed no Cloud archive upload. The shared upload page was separately verified with a real checksum mismatch and successful exact-file upload, followed by reload of the persisted archive.

Viewer-only rendering was not comprehensively reviewed. Permission enforcement was covered by API/store tests and the real Cloud credential check below.

## Development clusters

All Kubernetes mutations used the named `k3d-hakopod-dev` development context. PostgreSQL and Redis controller images and database images were pinned. The VM's separate development worker used default runsc; actual runsc container records correlated PostgreSQL containers with the fixture namespace. Redis recovery uses a binary stream through exec inside the verified container, because host-network port forwarding does not reach runsc's userspace network stack.

The Redis controller recipe built unchanged upstream commit `c5017206e75f7743d79e82db47ec8c39d7410816` for AMD64 and ARM64, passed its credential-handling regressions, and produced images whose manifests, source labels and ELF architectures were checked. The AMD64 image ran in the development cluster; the ARM64 image was built and inspected but was not runtime-tested. The released 0.26.0 controller exposed command passwords in error logs and interrupted a real resharding at its five-minute command deadline. The accepted build uses upstream's password-free command handling and a bounded 20-minute deadline. Inspection of an active Redis CLI command confirmed that its arguments contained no password. Engine regressions reject an unverified source, mutable image, short deadline and incomplete controller rollout. No controller image was published.

Acceptance covers standalone and clustered PostgreSQL and Redis, PostgreSQL streaming replication and primary recovery with a retained row, private endpoints, database-backed application deployment, PostgreSQL same-major recovery and 17-to-18 recovery, Redis value/expiry recovery, and shard growth/reduction. Redis checks inspect real topology and slot ownership. Corrupt archives and stale recovery evidence are rejected before application cutover.

The final Redis resize acceptance completed in 1,153 seconds: three shards and six members grew to four shards and eight members, then returned to three shards and six members. Each change followed fresh verified shard snapshots, and both resulting topologies passed value and expiry checks. The reduction's drain alone took five minutes and 35 seconds, beyond the released controller's former command deadline.

A real retained-volume check changed only its own fixture PV to Retain, confirmed database deletion, and verified the exact PV disappeared before deletion completed. An ownership regression rejects a mismatched claim UID without deleting the namespace or changing a PV. A real PostgreSQL store regression verifies that names remain reserved during deletion and become reusable with a new resource identity only after reclamation completes.

The self-hosted API acceptance used disposable S3-compatible storage to verify encrypted backup/import readback, separate recovery targets and explicit application connection replacement. The VM accepted PostgreSQL 17-to-17 and 17-to-18 logical copies under runsc. Redis recovery checks preserve binary values, absolute expiry, hashes, lists, streams and additional logical databases. The final cluster recovery check verified 90 keys across three shards, retained expiry, rejected a corrupt archive without changing the target, replaced a replica, and restored the resulting three-shard archive into a separate database while preserving both sources.

## Cloud integration

The actual Cloud API, embedded engine and composed dashboard ran with disposable development identities and PostgreSQL state on the VM. A customer requested a Redis database, a separate approver reviewed it, and the customer executed that exact approval. The database became healthy on the trusted worker. Customer credentials were available to the owner and denied to the approver. A second database exceeded the shared allocation and was rejected; releasing occupied compute was rejected. Approved deletion completed. Repeating the workflow reused the deleted name, applied Retain to the exact fixture PV and verified approved deletion completed after reclamation.

The same real approval flow prepared a checksum-pinned Docker archive import and returned its saved import identifier. No Cloud test uploaded to a customer bucket: the UI-only destination was explicitly marked as a development review fixture with invalid credentials.

Development storage used a labeled local-path class, including a class with the name required by the Cloud policy. These checks prove admission, placement, lifecycle and retained-PV reclamation integration. They do not establish new LVM byte-limit or multi-node availability evidence. The hosted installation remains one worker on one VM; production controller installation, worker runtime rollout and opt-in admission require a separate deployment.
