# Testing managed Vitess

The Vitess adapter stays disabled in release source until the same pinned
runtime and controller images pass native acceptance. Tests run in
`k3d-hakopod-dev` on the dedicated development VM. They do not use customer
database credentials, storage or Kubernetes contexts.

This page describes the test setup and what each check proves. Passing it on
one VM does not establish availability across physical hosts, zones or cloud
providers.

## Isolated native backup storage

Run `scripts/setup-development-vitess-storage.py --provision-only` on the development VM. Set
`HAKOPOD_DATABASE_FIXTURE_ROOT` to its acceptance scratch directory when it
differs from the script's default. That directory must contain the named
development kubeconfig and the reviewed `bin/kubectl` executable. The helper
requires Python, boto3 and botocore.

The helper creates one owned namespace with digest-pinned SeaweedFS. A
ResourceQuota limits it to one CPU, 1 GiB memory, one pod and one 3 GiB volume.
Connect that pod's port 8333 to a temporary HTTPS transport, then run the helper
with `--https-endpoint https://<fixture-hostname>`. The endpoint must have an
ordinary publicly trusted certificate so native clients use their normal trust
configuration. Record the provider, hostname, certificate issuer, process IDs
and any disposable transport identity separately from database credentials.

The transport must preserve every signed request header. Native AWS SDK requests
sign `Accept-Encoding: identity`; a proxy that changes that value breaks S3
authentication. A direct Cloudflare quick tunnel changed this header during
development. A temporary localhost.run tunnel preserved it but disconnected
before a native test finished, so it was not retained for qualification.

The current fixture uses a separate, bounded Caddy process behind Cloudflare. It
restores `Accept-Encoding: identity` only for header-signed SigV4 requests whose
signed-header list contains that exact field, whose method is HEAD, GET, PUT,
POST or DELETE, and whose raw path belongs to one of the six fixture buckets.
The matcher rejects duplicate or malformed Authorization headers and lookalike
bucket paths. It leaves query-signed requests unchanged. SeaweedFS still checks
the original signature and bucket permissions. Caddy listens only on loopback,
disables upstream compression and returns `Cache-Control: no-store`. This
fixture adjustment does not change the product's S3 client, TLS verification,
public endpoint implementation or production proxy configuration.

Check the public transport with the exact AWS SDK used by the candidate before
starting native tests. The current probe covers binary and special-character
object keys, complete multipart byte comparisons, range reads, explicit aborts,
and refused path, query and checksum-header tampering. It also verifies that a
warmed object URL refuses a foreign identity and an anonymous request, and that
an overwrite is visible immediately. Ordinary PUT requests use
`UNSIGNED-PAYLOAD`; the separate checksum test supplies a signed SHA-256 checksum
and proves that changing the body is rejected. Do not describe an unsigned
payload as cryptographically bound to its signature.

Keep the probe source, proxy configuration, execution times, checksums and
sanitized response metadata with the acceptance evidence. The observed
`no-store`, uncached responses apply to that endpoint and run. Passing these
checks establishes that the fixture can carry the native clients' requests;
it does not qualify Vitess lifecycle or recovery. Bound the tunnel, proxy and
localhost-only Kubernetes port forward, and verify that they remain active
before each native case.

Each test database receives a different S3 key and bucket. A key may administer
only its own bucket. The helper verifies signed HTTPS head, put, get, list and delete
requests, a multipart upload with a range read, and rejected reads and writes to
another fixture's bucket. Every probe includes the signed identity encoding
header. It saves credentials in a mode-0600 file and prints
only structural results and the public fixture hostname.

The native fixture config contains these separate entries:

| Entry | Purpose |
| --- | --- |
| `standalone` | One MySQL tablet behind a gateway |
| `cluster` | Two shards with one replica per shard |
| `recovery-source` | Two-shard logical archive source |
| `recovery-target` | Separate empty two-shard recovery target |
| `reseed` | One shard with two replicas for native member recovery |
| `revocation` | Storage approval removal, restoration and deletion |

Set `HAKOPOD_VITESS_NATIVE_FIXTURE_CONFIG` to the protected file path. Do not
put its contents in a command, issue, log or repository. Each test run selects a
fresh prefix inside its assigned bucket, so retained native backups cannot
initialize a new fixture with old data. The test refuses an existing database
namespace. A failed fixture can be retained for inspection with
`HAKOPOD_KEEP_DATABASE_FIXTURES=1`; remove only that owned fixture before retrying.

The tests permit only `k3d-hakopod-dev-server-0`,
`k3d-hakopod-database-worker-0` and `k3d-hakopod-database-worker-1`.
They require `HAKOPOD_DATABASE_RECOVERY_TEST=1`,
`HAKOPOD_DATABASE_VITESS_TEST=1` and `HAKOPOD_TEST_KUBECONFIG` pointing to the
named development context. Coordinate their resource use with other acceptance
work before starting them.

## Native checks

`TestManagedVitessLive` creates standalone and two-shard databases through the
same runtime path as normal provisioning. It checks binary and Unicode data,
queries through every gateway, primary and replica routing, and the exact rows
stored on each physical shard. It also checks wrong-host and wrong-issuer
certificate failures, plaintext refusal and application SQL privileges. Cluster
testing removes an owned primary process, waits for recovery, then verifies
reads and writes. That is process replacement evidence; it does not test an
unreachable physical host.

Certificate renewal must produce a new observed leaf and issuer, preserve data
and keep the previous CA working during its overlap period. The refusal and
privilege checks run again after renewal.

`TestManagedVitessRecoveryLive` captures a logical archive, changes the source,
then restores the captured data into a separate empty target. It checks both
shards, rejected corruption and truncation, existing-session failure, independent
source and target writes, and nonempty-target refusal. Application ingress stays
closed until both recovery and inspection are recorded. The archive records
per-shard read-locked captures, not one transactionally consistent instant across
all shards.

`TestManagedVitessNativeReseedLive` disables automatic recovery on the exact owned
vtorc process, stops one replica, and makes new writes through the healthy replica
set. It creates a native backup, purges the primary's older binary logs and
proves that the stopped replica is missing transactions from that purged range.
It then pauses the database's namespace operator, deletes the stopped tablet and
its owned disposable data claim, and verifies that Kubernetes removes the old
PersistentVolume object before the operator resumes. The operator must recreate
the same native tablet alias with new pod, claim and volume identities and
restore it from the latest backup. Every original row and the later transaction
must be present on that replacement before automatic recovery resumes, and on
every member after the cluster is healthy again.

`TestManagedVitessBackupRevocationLive` removes the trusted storage approval and
checks that backup credentials, storage egress, native backup Jobs and their
processes are gone. Remaining schedules must be suspended or deleting, and the
namespace operator must be stopped. Database tablet identities and application
data must remain intact. Restoring approval must restore the schedule and its
owned credentials and egress. The fixture is finally deleted with approval
removed, exercising ordinary garbage collection in that state. The namespace
operator must remain paused after deletion is accepted so it cannot recreate
children under the deleting database.

Removing a Secret does not invalidate a key at its storage provider. Running
tablets may retain native recovery credentials. The revocation test verifies the
documented platform boundary and does not claim that all copies of a key have
been destroyed.

## Evidence and cleanup

Run these tests against an isolated candidate source snapshot with the candidate
image digests. Only that snapshot may lift the runtime gate for qualification.
Keep the shipping-source gate closed until the release verifier has matched the
successful native test events to the exact source, patches and image identities.
Skipped tests and package compilation do not count as native acceptance.

`scripts/run-development-vitess-acceptance.py` runs one case at a time against
the `vitess-source-check` directory under the supplied scratch root. Pass the
qualified runtime and operator image references, including their SHA-256
digests, with `--engine-image` and `--operator-image`. Choose `lifecycle`,
`recovery`, `reseed` or `revocation` with `--case`. Use a new `--attempt` number
for each run; the helper refuses to overwrite earlier evidence. Run it inside
the development VM's bounded systemd unit after coordinating the test lane.
Pass the protected development cluster receipt and its exact checksum with
`--cluster-receipt` and `--cluster-receipt-sha256`. Before starting Go, the
runner binds the live cluster and all three approved node UIDs to that receipt.
It also requires exactly the eight established, names-accepted, namespaced
`planetscale.com` resource definitions.

Warm the Go test cache against the exact candidate source before importing the
images. Import the runtime, operator and etcd images into every selected node,
including their canonical `repository@sha256:…` references. Then measure the
space left. Image imports and compilation can consume several GiB even before
a database starts. Do not lower the kubelet eviction or image collection
thresholds to make a test fit.

Pass a separate `--fixture-budget-gib` for the new data, native backups and
container writes expected during the selected case. Use measured growth from a
previous run, or a conservative allowance for a first run. Recovery keeps its
source and target alive together, so its budget must cover both. Include any
growth expected in the S3 fixture. A PVC request is not a measurement of actual
space used, and local-path storage does not enforce it as a filesystem limit.

The helper checks the executor and every selected node are Linux on amd64,
schedulable and free of disk, memory and PID pressure. It reads nodefs and
imagefs capacity, the kubelet image collection threshold and the exact cached
image digests. Each filesystem must have the fixture budget available in
addition to a reserve of at least 12 GiB. A larger reserve applies when required
by the observed image collection threshold. The protected preflight report
records these measurements even when the space or image check refuses the run.
No Go test or database provisioning starts after a rejected preflight.

The runner also reads allocatable CPU and the effective CPU requests of every
active pod on the selected nodes. It reserves the fixed product envelope used
by each test topology: 8,450m for lifecycle, 16,900m for simultaneous recovery
source and target clusters, 6,350m for reseed and 4,900m for revocation. These
figures include replacement and native backup surge from the production
database capacity contract. The preflight records and rejects any shortfall.

It records the source inventory before and after the run, the image references,
the log checksum and the actual Go test events. Raw output stays in a protected
JSONL file. The evidence file contains structural test events without SQL or
credential output. The runner limits its log to 64 MiB, gives Go 90 minutes,
and stops the process group after 95 minutes. A skip, failure, incomplete event
sequence, changed source inventory or exceeded bound cannot pass qualification.

The source inventory includes the internal packages, their embedded scripts and
data, and the template catalog used by those packages. It rejects symbolic paths
and limits the inventory to 4,096 files and 128 MiB. The runner fixes the Go
target, toolchain selection and module settings. User Go configuration,
workspaces and inherited compiler flags cannot override the candidate. Verify
the module cache with `go mod verify` before warming the candidate test cache.

`release/verify-vitess-runtime.py` checks the combined native results against
the source, upstream patches and binary checksums. It also pulls the published
images without registry credentials and checks the binaries inside them. A
local candidate run alone does not establish that a release image can be pulled
or that a published image contains the tested binaries.

Keep the storage pod running until the tests finish. Restarting its transport can
change the public hostname; recreate the protected fixture config and its exact
approved endpoint addresses before another run. Retained backups consume the
fixture's bounded storage, so inspect and clean up only the owned fixture data
between qualification rounds.

After recording results and deleting all owned database fixtures, stop the
recorded transport processes and remove their disposable identity. Then remove the
`hakopod-vitess-s3-acceptance` namespace and the two protected fixture config/state
files. Verify that the namespace and its volume have been reclaimed. Do not
delete a similarly named namespace without matching its recorded UID and
`hakopod.io/development-fixture` label.

See [the Vitess stack and limits](managed-vitess.md) and
[managed database architecture](managed-database-architecture.md) for the product
contract these tests are intended to qualify.
