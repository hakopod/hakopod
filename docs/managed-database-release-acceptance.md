# Managed database release acceptance

## Alpha.58 native qualification

Vitess, MyDuck and Oracle Free qualification passed on October 8, 2026, in the named
`k3d-hakopod-dev` development cluster.
Installer acceptance, publication and production deployment are separate gates.

The five native cases used source `e8164b49f5e2cac321309d7dc3a72da4f5656ec5`.
The HTTP case used source `693a72810db0f4b53fc285944d4e566959a8c046` with the same
native source inventory and pinned runtime images. Every case verified its
source inventory before and after execution. Independent checks confirmed that
the fixture namespaces, pods, persistent volume claims and backing volumes were absent.

| Vitess case | Runner seconds | Protected log SHA-256 |
| --- | ---: | --- |
| Lifecycle | 1006.460 | `75d241723579204fe0664ab1ee887c54a90be0a185b42e3236d091d95729f8e6` |
| Recovery | 616.569 | `f150da94f48572069e09cc427dc2046d44b0fc32028e7f5ffba0d6d37e08b5e4` |
| Reseed | 421.230 | `c0ea29c7118ac05a26080654b2d93f8b84e897ba4788d1a4e98d09f8d5f73cd9` |
| Revocation | 279.676 | `c5c1cdcc996fea88fbf4fe3dade3c1f2a94d5928a0225120a59958dcbbfdde91` |
| Scale | 799.157 | `d772bf8e04184cab885d4341c1b2650ceb6ba334a145f5295c7319e1126db8bc` |
| HTTP/API | 911.545 | `ecb9e987b68c6d391e276634e110fb960db6988abaa899f469abec0c02d1dc77` |

The HTTP case verified scoped credentials, SQL, backup and restore, replacement
topology, an application query through a reviewed binding, and scoped deletion.
Its first attempt correctly returned `403` because the test key lacked the new
explicit credential grant. The corrected fixture passed. Failed infrastructure
and fixture attempts remain separate from accepted results.

The [native manifest](../release/managed-vitess/manifest.json),
[native record](../release/managed-vitess/native-acceptance.json) and
[HTTP record](../release/managed-vitess-http/evidence.json) retain the original
accepted evidence. The [candidate compatibility review](../release/managed-vitess/source-compatibility.json)
records separate native and HTTP commits and exact evidence hashes. It permits
only the reviewed source delta: two other-engine HTTP test fixtures and the
verifier's candidate identity format. Production Vitess code, images and native
assertions are unchanged by that delta. The verifier rejects unreviewed changes
and changes to protected runtime, dependency and controller inputs.

The candidate identity format passed 50 focused tests on the development VM.
The existing runtime CI result covers the unchanged credential authorization
policy. These are separate checks. The five native cases were retained after
the fixture corrections, without another native run.

The development workers shared one physical VM. These results do not establish
independent-host, zone or provider availability.

### MyDuck candidate qualification

All three MyDuck cases passed on immutable source
`3f973d47f8a99115f31ea3ed8d3059bfa762da71`. The rebased candidate has the same
MyDuck source inventory. Runtime images, binaries and build receipts are unchanged.
Each case verified source integrity, unrelated resources and fixture cleanup.

| MyDuck case | Go test seconds | Protected log SHA-256 |
| --- | ---: | --- |
| Lifecycle | 101.484 | `c849416018062c739847ecffa071c518f7bd940814cf7d4464297d24eee57132` |
| Recovery | 292.627 | `453bc862a857ad7bab35ffbbfe122534311130fea658a25cb50c48d915ae55fb` |
| HTTP/API | 252.128 | `6bef87b436fa6831e6153ced6b9c12c515810c18d17d4bd6f8b6c6e6d9b53171` |

The first HTTP attempt failed because its temporary backup staging directory
inherited group and other permission bits from the runner's `UMask=0022`.
The production archive verifier correctly refused that directory. The accepted
attempt used `UMask=0077`, and its actual staging directory was verified as `0700`.
The source and assertions did not change. The original failure remains separate
from the accepted result. Set `UMask=0077` for future MyDuck HTTP qualification units.

The [fresh manifest](../release/managed-myduck/manifest.json) has SHA-256
`eb0b3414879f3b49b175b96eed80b3caf406231b0747576d20394f0d61455736`.
The [native record](../release/managed-myduck/native-acceptance.json) has SHA-256
`8faffa1df6d8a25184cc3c1ff42130f623016bf242e8984d9bd0da0cd57c15d8`.
The original build and packaging receipts remain byte-for-byte unchanged.
The current source matches this evidence, so no source compatibility exception
is required. Historical records remain available at their release tags.

### Oracle Free candidate qualification

Oracle Free qualification uses immutable source
`3f973d47f8a99115f31ea3ed8d3059bfa762da71`. The lifecycle, recovery,
controller-loss and HTTP/API cases passed. The rebased candidate has the same
Oracle Free source inventory.

| Oracle Free case | Go test seconds | Protected log SHA-256 |
| --- | ---: | --- |
| Lifecycle | 497.449 | `9714f51922da082dd8bb6928d49b8d8fd557687b46b60a970a1f2266a897e2f0` |
| Recovery | 1229.356 | `6efbbc76117c3c7861442096b3fed3b69ad62cda998e1ce19e22aab9fc73dee3` |
| Controller loss | 516.542 | `c6ccb1215afc0efbb437d448bf59cb5f71a966cd9ffd4c35a2bc55b2ca927f1f` |
| HTTP/API | 1548.666 | `953e010eae03fc5475886ca8663c06f57c8694feb9cfbd16d96fbea6ac42a0c8` |

All four cases preserved source manifest
`abb8257312e917f7e5f8832b0ffc21ece91c3151fdb6e18eac93c564f0c85e44`.
Their reports confirm that fixture namespaces and backing persistent volumes
were absent after execution. Runtime images and original build receipts are unchanged.
The HTTP case passed authorization, lifecycle, encrypted backup and restore,
application binding and revocation, and deletion. Its three fixture namespaces
and backing volumes were absent after execution. The host receipt confirms
that PostgreSQL and S3 fixture containers and credential files were absent.
That receipt has SHA-256
`afe8bff764f9bd6975d33ff5fd387145dc90a7f30f6eb18e02f60c49479cc65e`.

The [fresh manifest](../release/managed-oracle-free/manifest.json) has SHA-256
`409e415aa330e455066980e3ed4761ed2791d05cfa84b737684372046e4f63c9`.
The [native record](../release/managed-oracle-free/native-acceptance.json) has SHA-256
`a2a186bd026184e6085f0ce464bf76cb697d02f0450821b2d663aef601ca3b8a`.
The record uses schema 1 because all four cases share the same source inventory.
The original operator patch, CRD, build assets and packaging receipt are unchanged.
The current release no longer needs the historical source compatibility and
HTTP derivation records. Their historical copies remain available in Git.

A separate source review compared all 166 vendored Oracle driver entries with
the tested commit. Their Git tree is unchanged at
`4313d681405273f04539ce1b7c1fdb70712cd73b`. The native manifests do not individually
list those driver files. The release packaging fingerprint includes that directory.

## MyDuck and Oracle Database Free — alpha.56

### MyDuck

The lifecycle, recovery and HTTP cases passed on `2026-10-07 UTC`
against exact source `b253812df8236905ab4db2015380dae5da2ed7f1`, tree
`e63730e88b3ad76993a293ed2641af3e53ac77d5`, in the named `k3d-hakopod-dev` development
cluster. Acceptance used three Linux amd64 k3d nodes: a server limited to 2
vCPUs and 16 GiB, and two workers each limited to 5 vCPUs and 40 GiB. This does
not establish independent-host, zone or provider durability.

The immutable public runtime is
`ghcr.io/hakopod/managed-myduck@sha256:ad324a97360dea53f9e32cb367666b8fefa6f52377000c084a63c1712ca79873`, version
`0.1.0-hakopod.3`. It is built from upstream MyDuck revision
`6e3427591fd8895df9585969e7256f958fb639bb`.

| Case | Protected log SHA-256 | Cleanup |
| --- | --- | --- |
| Lifecycle | `553daaaee9f1f063629d2c95cf877a1cbb118f321d803e41717866ea6339e1e9` | one namespace and its persistent volumes absent; retained 7 namespaces and 5 volumes |
| Recovery | `7d9371a1a2f68974f70c988eaa0985b86793f2f4b25a3e16cc31b327b43d9223` | five namespaces and their persistent volumes absent; retained 7 namespaces and 5 volumes |
| HTTP/API | `612c932de3c6dec2faa0eb04d90c1e00b71507035378936c53c8aaae646d500b` | two namespaces and their persistent volumes absent; retained 7 namespaces and 5 volumes |

All cases used source manifest `4eb8943af87ce5b925b1cd6e1a95dee10e1a7f7ae1106eb0aff99c8faa91f316` before
and after execution.

The recovery case passed `nonempty_target`, `baseline_replacement`,
`corrupt_restore` and `successful_restore`, followed by five fixture-cleanup
checks. The HTTP workflow passed all six required phases and its cleanup. Every
case preserved its source and cluster baselines and left no helper process
running. The canonical qualification record and release manifest bind these
results to the shipped source and image:

- [MyDuck native acceptance record](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.56/release/managed-myduck/native-acceptance.json),
  SHA-256 `a0b6a0b88ceb2909ff351e7dfa30cb0cd41ea8332a9af9ffd69d60960f207107`
- [MyDuck release manifest](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.56/release/managed-myduck/manifest.json),
  SHA-256 `0fb4844daeb313f4e540e90676aeb92107a3f375136078552bf1b181c4c66930`

The release contract is one persistent instance with no replica, failover,
managed pool or in-place resize. It exposes private TLS 1.2-or-newer connections
on MySQL port 3306 as `root`/`app` and PostgreSQL port 5432 as
`postgres`/`app`. Public endpoints remain unavailable.

### Oracle Database Free

Oracle Free passed its native and HTTP qualification on
`2026-10-07 UTC` against exact source `b253812df8236905ab4db2015380dae5da2ed7f1`,
tree `e63730e88b3ad76993a293ed2641af3e53ac77d5`, in the named `k3d-hakopod-dev` development cluster.
Acceptance used three Linux amd64 k3d nodes: a server limited to 2 vCPUs and 16
GiB, and two workers each limited to 5 vCPUs and 40 GiB. This does not establish
independent-host, zone or provider durability.

The immutable database image is `container-registry.oracle.com/database/free:23.26.3.0@sha256:f988b0c04c4c386cd306a2a914c0d7a9702d83acc31b064a28ad8eb6278a8fba`. The
namespace-scoped operator image is
`ghcr.io/hakopod/managed-oracle-free-operator@sha256:e728b6283d4f27947af96232b9d74dc31d9adf288a62722c026c2ce3b6fe7660`.
The operator has access only to its database namespace. Applications receive no
operator or cluster credentials.

| Case | Protected log SHA-256 | Cleanup |
| --- | --- | --- |
| Lifecycle | `b425665692b7df29e5fc38b609167a0f366cc7444b5c86a99cee6065763110f5` | one namespace and its persistent volumes absent; retained-resource baseline preserved; helper processes stopped |
| Recovery | `d658e5ec3e39c613600c0599ef604812b6c6c5527b3c782dc010abf0fdcc8c58` | two namespaces and their persistent volumes absent; retained-resource baseline preserved; helper processes stopped |
| Controller loss | `67cd658ae95625821e1dfe4084240e3d64a17e27c7a59d13b2e83e60862cfa15` | one namespace and its persistent volumes absent; retained-resource baseline preserved; helper processes stopped |
| HTTP/API | `bef33f25727ce3872c6f0510c360892a749348817a7df45cb815059b42f47c33` | three namespaces and their persistent volumes absent; credential files and PostgreSQL and S3 fixture containers absent; retained-resource baseline preserved; helper processes stopped; host receipt `c146c98a4b39e1c3b43148b3790c7a10c327772df461f806124fe9b5a0372fb7` |

All cases used source manifest `7d896c98d285ffec45609bbb9feb0cbb50f925b5d47f755b758002e76df9a56b` before
and after execution. The lifecycle, recovery and controller-loss native cases
passed. The HTTP/API authorization, lifecycle, backup/restore,
binding/revocation and deletion phases passed.

- [Oracle qualification record](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.56/release/managed-oracle-free/native-acceptance.json),
  SHA-256 `7db0aa74e096f6c90078a949838a1d67dc2f298c790a9f9876bfbd209ce50eb1`
- [Oracle release manifest](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.56/release/managed-oracle-free/manifest.json),
  SHA-256 `b15ba8f99e9f046e8ec63bde1413f800632433de69e747140ed98d6d8f6302fb`

Oracle Database Free is proprietary, no-cost software. The release provides one
private standalone instance, a namespace-scoped operator, TCPS on port 2484 for
the `APP` account and `FREEPDB1` service, and equal-size data and Data Pump
staging volumes. Oracle Free limits the database to two CPUs, 2 GB of database
memory and 12 GB of user data. Enterprise, Data Guard and public endpoints remain
unavailable. Backup exports the APP schema with Data Pump. Restore imports that
archive into a separate empty target; this does not provide RMAN or point-in-time
recovery.

Independent dashboard review covered 232 captures from the current web source,
which matches reviewed source `058c25c`. The database-create critical source had
SHA-256 `5e3fbe953a65898e2daacf4c23fa88c75b9d1b3b6c112e2dcd71f77812f39e40`.
This UI evidence is separate from live cluster and release acceptance. Cloud
alpha.35 is a separate package and deployment. The current production shared
pool cannot fit Oracle or Vitess, so a Cloud rollout needs its own capacity and
placement acceptance.

### Existing Vitess qualification

Alpha.56 retains the Vitess runtime and operator from alpha.55, including their
image digests, build inputs and installer. The original native and HTTP evidence
is preserved byte for byte. The
[source compatibility review](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.56/release/managed-vitess/source-compatibility.json)
records every reviewed source change with its before and after hashes.
Packaging rejects an unrecorded change or a change to Vitess's runtime,
dependency versions, build inputs or controller.

Shared binding options and password handling passed the
[control-plane CI checks](https://github.com/hakopod/hakopod/actions/runs/37656577625).
These are regression results for the shared Go code. The Vitess native and HTTP
results below remain the alpha.55 results.

## Vitess 23 — alpha.55

Five native cases passed on October 6, 2026, in the named development cluster
against source `6e7028e02c52ffb1c982bd421c5a78d249d93d7d`, after rebasing onto
`fe3159aaafa3d80201c94499371df8d8ccecc0f3`. Every case used the same runtime
source and pinned images. The full 2,857-file shipping inventory remained
unchanged throughout each run.

The runtime was
`ghcr.io/hakopod/managed-vitess-runtime@sha256:b62641ca2ce73662b3a16a154d7ab918fae6aa3b7dd0424f897d2e83ade5247a`
and the operator was
`ghcr.io/hakopod/managed-vitess-operator@sha256:275c3ce3794dc5be502450cef38b7ac632a81a4b2f2834f7889b83e28a3cae9b`.
The [manifest](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.55/release/managed-vitess/manifest.json) and
[native record](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.55/release/managed-vitess/native-acceptance.json) bind source
files, binary checksums, image digests and structural test results. Anonymous
pulls of both images passed, and their binaries matched the recorded checksums.
The manifest SHA-256 is
`5c32ebfd3490cd30442a3314a11ec3a5e39aab9a474ba863d628c72811533412`;
the native record SHA-256 is
`082249f911d16010c542545400037d32ac2ecb95abf3e7278b771c7b029eabc2`.

| Native case | Runner seconds | Evidence SHA-256 |
| --- | ---: | --- |
| Lifecycle | 977.016 | `adbcb3006b56535941a2900e3401ea84793d387aa4c319f68fb802b0f7751fef` |
| Maximum topology | 864.212 | `e925766cd1750641ccc76cd7682c71b3ae973b47b87da6d14dcb802435088214` |
| Recovery | 671.812 | `89c423311869c646b58ec45f39a386b7017d119ad757b5dcb484d4cab534c3f4` |
| Replica reseed | 429.218 | `a9409f02642ae12458eceb8570f6edbfa5650483ff272c53ddb5e5ca02cc5630` |
| Backup approval revocation | 284.974 | `a26d07f0a98153e96efeb05a50a5d8bf958bd89be62c827ecd956767a03b685b` |

The maximum-topology case created eight shards with five replicas per shard:
48 tablets behind two gateways and three topology voters. Three consecutive
observations completed in 13,854 ms, 16,113 ms and 15,483 ms. Each observation
used 140 core, 123 grouped API and 157 exec requests. Core and grouped requests
stayed below their limits of 176 and 150. Every observation completed within
25 seconds. Every case passed runtime and label
restoration, and removed its fixture namespaces and persistent volumes.

The official HTTP workflow passed on source
`80ff8546f5a1657a960e1288fbf3124d7ad132d3`, whose runtime and HTTP harness match
the native qualification. The test took 857.14 seconds; the full runner took
1062.224 seconds including cold compilation. It verified creation, scoped
authorization, encrypted backup, separate-target restore, fresh topology,
inspection, a bound application query with TLS hostname verification and scoped
application and database deletion. The wrapper confirmed source integrity,
runtime and label restoration, and no remaining fixture namespaces or volumes.

The [HTTP evidence](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.55/release/managed-vitess-http/evidence.json) SHA-256 is
`f702587ade68b3a2d7c7c511c45d8e68fd6bcef455379ff42ccc6f952fc22f6b`.
The canonical release verifier accepted that report. Its wrapper receipt has
SHA-256 `aaed7b9ee266d88acb1f6581b0ee9b7d949ca98019086eea444df6b88cdb2bac`.

The release targets Linux amd64 workers and fixed standalone or sharded layouts.
Its maximum declared layout is eight shards with five replicas per shard. The
workers in native acceptance shared one physical development VM, so the result
does not establish independent host, zone or provider availability. ARM64
Vitess workers, public endpoints, online resharding, in-place capacity changes,
cross-shard transactions and native latency or replication-lag telemetry remain
outside the release contract.

Independent dashboard review covered the affected route families in light and
dark themes at desktop and mobile widths, including keyboard and touch
interaction and the artificial maximum-topology fixture. That UI evidence is
separate from live cluster acceptance. Cloud packaging and production rollout
require their own release, controller, worker and capacity checks.

## ClickHouse 26.3 — alpha.52

Six native runtime tests and the HTTP API recovery workflow passed on
2026-10-04 in the named `k3d-hakopod-dev` development cluster. They used a
dedicated, lease-owned worker with the ClickHouse gVisor profile, separate
from the preserved development workloads. These results establish the
development qualification below; package publication and production rollout
have separate checks.

The immutable runtime references are:

- Server:
  `docker.io/clickhouse/clickhouse-server:26.3.33.24@sha256:810861a2e2d0188744f5f23b2d3ec9ff95812bcb9ddbb8fed13a377a7f305893`
- Keeper:
  `docker.io/clickhouse/clickhouse-keeper:26.3.33.24@sha256:3fd59d9efb8c9e9136c3c924ceaa004f65c0b14699f34e4eae4eb63e2f860803`
- Operator:
  `docker.io/altinity/clickhouse-operator:0.27.4@sha256:c60c872fedd85017f843167dfdd2b900bb06b668a3deaf02b21782e59af859e3`

Application binding passed on source
`eb32a4fc9c58f83670af4b0aea99e06f5d827d5e`. Subsequent native tests use
`e23dc6fca8b67225fdb4e4449abce5fd41cc49bd`, tree
`f0aaeb0da579064b19d3335712948da6b35f0daf`. The cluster runtime source is
identical; only the quorum test changed between those two revisions.
Durations below are the individual Go test durations, excluding package
startup and the surrounding evidence collection.

| Native test | Seconds | Test log SHA-256 |
| --- | ---: | --- |
| `TestManagedClickHouseBindingLive` | 253.50 | `075db1c0cf1fc233d92f959059a67d73efea7286dabad4bc414c51412a9a976e` |
| `TestManagedClickHouseKeeperQuorumLive` | 484.94 | `98b0fc0c0bcd317f507264e13a3a506e7bd3bfc4616f55e118268e4aae80a454` |
| `TestManagedClickHouseLive` | 532.19 | `57e1b4efa3ca31d308a3df56cd3ae646323152655cf53c9f49c6d878f57d8a0e` |
| `TestManagedClickHouseTLSRenewalLive` | 517.43 | `ee8ae14724afe707cdcc6df217b670404ab3618db76839ee94880bd043fe07af` |
| `TestManagedClickHouseDistributedQueriesLive` | 794.77 | `4b60dfb8d5fe268d74a6fa654c3be51697cda1c73b336095a036fefe2ba6af0b` |
| `TestManagedClickHouseRecoveryLive` | 1938.30 | `16fa297e9eeaca6945d256248038877d2e4d4c3f7a409c1186532813bb0949e3` |

The counted quorum test verifies the exact paused member and its UID before
its assertions. A prior diagnostic pass without that check is excluded:
automatic watchdog recovery could otherwise be mistaken for survival of a
still-active fault.

TLS checks rejected the wrong issuer and hostname, then verified renewed
identities on all data and Keeper members with old-CA overlap. The Distributed
query test created tables through the application account, routed inserts to
both shards and read the combined rows from every member while preserving
the account's privilege boundary.

Recovery passed for standalone, one-shard clustered and two-shard clustered
databases. It preserved binary values, rejected truncated archives and
nonempty targets, kept target ingress closed for inspection, and left source
and target replication independent. The two-shard case also checked restored
Distributed-table routing. Their individual durations were 231.12, 634.01 and
1073.17 seconds.

Independent rendered UI review passed all 90 cases, with no failures or
warnings. It covered the database catalog, detail tabs, capacity page and all
five creation steps in standalone and cluster modes, including failed-request
value preservation. Dark and paper themes passed at 1440, 390 and 320 pixels,
with keyboard and touch interaction. The explicit UI fixture contained two
shards, six data members, three Keeper members and 15 application bindings;
it used the dashboard's real logo assets. Screenshots and element bounds were
reviewed, including the topology's internal scrolling on narrow screens.

The reviewed UI source was `9bd4847c77dbe17cb20e95c1bccd7f62e3dd913b`;
its `web/src` matches candidate `4e3f9734455d17355ce99c304d4e3b8e68dcea49`.
The result JSON SHA-256 is
`e33c7d8b87256508a37d324feeb4099b6bc5fbbd959693d021b39bd429749f36`.
The review receipt SHA-256 is
`05dfd131b4a5ec72bebbdcbda98ad2ad9588c8ab8e76321c6542db6561edb953`.
Typecheck and production dashboard builds passed. All 30 extracted name
patterns compiled under browser Unicode `v` rules. The database name field
was exercised in the form; other repaired patterns had static coverage.
Shared scope-hook probes covered application, platform and build paths,
including tab/hash preservation, replacement history and unavailable-resource
handling. Those probes did not visually review the other route bodies.

The full Go suite, dashboard, SDK, certificates and interface checks passed
on candidate `4e3f973`. Installer packaging, both native architecture smoke
jobs and all 12 fresh-install/alpha.50/alpha.51 upgrade cases passed in
[run 37218106479](https://github.com/hakopod/hakopod/actions/runs/37218106479).
The final tag's release workflow must separately verify the published assets.

`TestLiveManagedClickHouseAPIRecovery` passed in 294.73 seconds on source
`9d691d25b0391da89f231a18c37f7002b77b028d`. It exercised HTTP creation and
idempotency, durable workers, authenticated TLS writes with binary values,
encrypted backup, restore into a separate empty target, a fresh observation
of the replacement member, data verification, inspection and HTTP deletion.
Cleanup verified that the test's PostgreSQL and S3 fixture containers,
database namespaces and persistent volumes were gone; the dedicated worker
remained ready. The log SHA-256 is
`5a732968d100e6272e5ca6e6baca7227a92eb086fee9851fb85fc9143ae263c8`.
The run receipt SHA-256 is
`64391d434957c94ee983df0869cb3774f1cc92f8b50261a9d43ab2c94024cd92`.
The parent source archive SHA-256 is
`1a1d8c942f130c9f7395dd016eb70334fbad48e5789c6a12fcb43358c7dcd2cf`;
the templates commit is `f9e3992e1b86969c30d88a6964c4dfc0d4b3ef34`.

An earlier API attempt finished backup and restore but tried to query the
target with its pre-restore pod UID. The membership guard correctly rejected
that stale identity. The passing test obtains and checks a new API observation
before querying the restored target; the guard remains unchanged. Earlier
failed attempts are excluded from the passing evidence.

The rebased candidate `b4a2e33f5ad28731d01c68e8170a5376a99ea751` retains
the exact cluster runtime from the direct tests and the API recovery test
from the passing run. Its main-branch update adds Slack integration work;
the final candidate CI and release workflow check the combined source.
Public endpoints remain held.
These development workers share one physical VM, so their results cannot
establish independent-zone or cross-provider availability.

## MySQL 8.4

Six native MySQL cases passed on 2026-10-04 against the named
`k3d-hakopod-dev` development cluster, UID
`f63996ad-2bc2-4e6f-9051-26e03a28cf96`. The tested source was
`7b3842cbb93bad934d7c7f4329f1bf19407f9493`, tree
`8987482bbd298b2ab061fea8efcdcad4584b2260`, with templates revision
`44f3bd6c396039920893592b0f93e5927562121c`.

The immutable runtime references were:

- Operator and sidecars:
  `container-registry.oracle.com/mysql/community-operator:26.7.0-2.3.0@sha256:01ecaa57bf952850ff9ffd7caeb231a9c3335764fdb12488f132ea4605d8f364`
- MySQL server:
  `container-registry.oracle.com/mysql/community-server:8.4.12@sha256:7dcc4add9183664de3a214daf85a50c3ba6cccfd7534f700b6561bf5b41885be`
- MySQL Router:
  `container-registry.oracle.com/mysql/community-router:8.4.10@sha256:d704471c2bb78fa833790dc197959c004d09af5aa18f5f153ff40e7268f54043`

The suite checked standalone and clustered lifecycle; authenticated binary
reads and writes; hostname and issuer verification; plaintext refusal; scoped
application accounts; replica write refusal; primary replacement; certificate
renewal and old-CA overlap; separate-target backup and restore; binding and
revocation; scaling from three to five to seven and back to three members;
Router replacement; and quorum-loss write refusal with recovery of committed
data. Router-to-server TLS tests separately rejected an unrelated issuer and
the wrong hostname before verifying normal routing again.

All six tests passed with no skips. Durations are Go test durations, excluding
the surrounding evidence and cleanup commands.

| Native test | Seconds | Test log SHA-256 |
| --- | ---: | --- |
| `TestManagedMySQLLive` | 821.27 | `f93e31a6addd5692917ed569653dc43db51c04aa5c35827842a8b436df99f30a` |
| `TestManagedMySQLRecoveryLive` | 538.90 | `da6341d0528962daa979e5c451569d8a1970e225e69a3dc71149faa4eb22ff2d` |
| `TestManagedMySQLScalingLive` | 665.15 | `9db805983810c4b1c6faf0d2648bfe49cab0535765705a028c819ae7f01cd937` |
| `TestManagedMySQLRouterBackendTLSLive` | 235.08 | `cb441f40a891478111218ae8eeeed7a24db3fae9b6fb96114e8e9bf5caba306b` |
| `TestManagedMySQLQuorumLive` | 297.29 | `1437fb86de0be316764015c000283e6ccf289f2a21d422b5d95fdbed9208fc5e` |
| `TestManagedMySQLBindingLive` | 295.08 | `a46cb8a54c9e90e1144f10ea2967915d8c42922b93026806ce26794d5d6095c5` |

The tracked source archive SHA-256 stayed
`5bbfc502ce7a983e076ea54e22117f424064e79fa4b8c1e57ffc6ff8d25f21e6`.
After cleanup, the normalized namespace inventory matched its baseline hash
`67d297663db8502277c1e10020ef33d754cdf7026558ae4fc6de0c1428d1eea3`,
and persistent volumes matched
`28ed6f31aed97e320fad049b3be83e6e2399f2608db0db6eb92e57e5111613db`.
No fixture database namespace or InnoDBCluster remained. The operator retained
its pod UID and image with zero restarts. Bounded credential and private-key
log scans found no matching markers.

The later reviewed retry API, CLI, SDK and dashboard use this unchanged MySQL
runtime. `TestManagedMySQLResizeRetryLive` passed against source
`2675ccfff534f4d2b05853662314093ba0a44210`, tree
`3257c9e0ca04795cabd32a1d8009b123bbc5c5c4`, on the same cluster and templates
revision. Its Go test duration was 481.39 seconds, with no skips. The test log
SHA-256 is `d1cdf579be3bc25beb20a91b596920194dc71e5fedad72969d4fb3b3c8f075de`.

This test seeds failed operations only in a disposable control-plane
PostgreSQL database through the durable claim and step-recording methods. It
does not patch Kubernetes controller status. A retry from the prior layout
converged at revision 2 with five native members; a retry after the controller
accepted the change converged at revision 3 with three. Both preserved the
committed binary value, original failure and exact-request replay. Deletion
reclaimed the fixture namespace and volumes. Namespace and PV inventory hashes
matched the baselines above; the operator retained its UID with zero restarts,
and the disposable control-plane container was removed. The tracked source
archive SHA-256 remained
`c2b54b69243f2c0d2046062788f6098b7de8bd3e913a9be523dd26fbfac6983a`.

The same candidate passed the full Go suite against disposable PostgreSQL,
Go vet, focused API/store/CLI regressions, 52 SDK tests and generated-contract
checks, and dashboard build, typecheck and 218 tests. Independent rendered UI
review passed 86 cases: both themes at 1440, 390 and 320 pixels, MySQL and
MongoDB retries, invalid or expired reviews, Activity navigation, keyboard and
touch, failed submissions with retained confirmation and idempotency keys,
and a successful submission. UI fixtures use explicitly artificial API data;
they do not replace native acceptance. The UI result SHA-256 is
`a15d3cdea90ec72d64232208380186ada06a1912019bb7df930e358af66a165c`.

Candidate CI passed in [run 37195062244](https://github.com/hakopod/hakopod/actions/runs/37195062244).
The alpha.50 installer package, both native smoke jobs, four fresh installs
and eight alpha.48/alpha.49 upgrade cases passed in
[run 37195062446](https://github.com/hakopod/hakopod/actions/runs/37195062446).
The final tag's release workflow separately verifies and attests the published
archives; candidate checks alone do not establish a published artifact.

These tests ran on AMD64 development nodes sharing one physical VM. They do
not establish independent-zone or cross-provider availability, a production
deployment, point-in-time recovery or public MySQL endpoints. See the
[MySQL guide](managed-mysql.md) for connection and recovery limits.

## Redis 8

Redis was qualified on 2026-10-02 against the named `k3d-hakopod-dev`
development cluster. The final release source was
`34aa7ba12362fa892472f5284d40580cbd62ed97` and the tested controller image was
`ghcr.io/hakopod/managed-redis-operator@sha256:87a426b087355e41247210d176d82812a5c8c462cc2856789513dd00a37ab32a`.

The release runtime is
`ghcr.io/hakopod/hakopod-redis-runtime:8.2.10-opstree@sha256:caa42ea6bb728692da6045aca364f2ae3a33e15242a2e874dfbfa75ecceb5657`.
Its native amd64 and arm64 builds, authenticated and TLS smoke tests, manifest
publication, and anonymous registry inspection passed in GitHub Actions run
`37000780889`. The platform manifests were:

- `linux/amd64`: `sha256:66a38f88f516d50de222e0b14631776851b9ff3adca5a6a4d03f1485e0d4f08a`
- `linux/arm64`: `sha256:f09fa2d4578c2f962c9391b35b4e617571076c17cb95d388101e7ee0d321f8ae`

The image builds Redis 8.2.10 from revision
`86b20b1b9e5f03d86db72c3d59bec432e471fbc9`, whose source archive SHA-256 is
`cc123e28b60ea1cfc6466f9f69dad8880515197a0f4786098b49304540b27b43`.
It uses Opstree runtime files from revision
`8a6d56bd05d42f05ff1f1f6ccfe1a7b9fbc1ef5f`. The image contains the upstream
license texts, the explicit Redis AGPL-3.0-only selection notice, the source
lock, complete source locations, and its build recipe. CI also verified the
Hakopod source, license, and upstream revision labels.

The long behavioral suite ran from source
`0d01cef579bc14759717cca47aa1ded6b240583d` against the same controller and the
earlier runtime index `sha256:a8b5559271d94989b92e3b5fe22c5167bf8ccda83fbac33065b7e1a42dd250cd`.
That runtime has the same Redis binary, entrypoint, configuration, UID, TLS
behavior, and data paths as the final image. The later image changes add
license/source material and correct OCI source metadata. The suite passed:

- standalone binary and Unicode-safe backup and separate-target recovery;
- a six-member TLS cluster with replica-acknowledged writes, primary
  replacement, separate-target shard recovery, and absolute expiry;
- hostname, issuer, and plaintext rejection; and
- automatic certificate renewal with old-CA overlap in standalone and cluster
  modes.

The package completed in 1,846.908 seconds with five pass events, no failures,
and no skips. Its bounded JSON evidence was 20,001 bytes with SHA-256
`a09910bde9ab587477e0e5e9b35f7a46544a7cc670c67578978eaad01bb0461c`;
stderr was empty. The staged source-tree checkpoint and final hashes both were
`40ad18b97bc2f23fd4bc8c0d0b0134809541902ae7332cc20bd27b28d65953a3`.
The checkpoint was recorded during execution after immutable source staging,
so it is evidence that the source did not change through completion rather
than a pre-execution measurement.

A separate final-digest check used exact release source
`34aa7ba12362fa892472f5284d40580cbd62ed97`. Kubernetes reported the final
runtime digest in both the pod specification and runtime image identity. The
standalone Redis test reached ready state, verified TLS and plaintext
rejection, and passed in 31.14 seconds. The package passed in 31.159 seconds.
Its JSON evidence was 2,888 bytes with SHA-256
`868753c05728909ddb18f13ebd22521973b82489ee1964b90b50ac5d8ce57bf4`;
stderr was empty. Cleanup removed the disposable namespace. All six namespaces
created by the longer suite were also absent after cleanup.

The real-cluster jobs were capped to an aggregate two CPUs and 12 GiB of
memory. The longer suite's observed peak was 3,237,474,304 bytes. These results
qualify the named development cluster and the stated immutable artifacts; they
do not claim production deployment.

## PostgreSQL 17 pooling and recovery

PostgreSQL 17 pooling was qualified on 2026-10-02 against the same named
development cluster and templates revision
`40de62d34a9cb5d70cdae2534266372afaf0fe7d`. The pooled application-binding
test passed in the first run from source
`e2d6e3949651eb6bd069d7395bc15b1ea36927c2`. The accompanying recovery test
exhausted the original shared 15-minute parent after provisioning its source
and target, so that run was not counted as recovery evidence.

The isolated recovery retry used source
`b7a34ce550aa75a1799d07f9e0d88543cc0f22ff`, which includes the bounded
30-minute parent for source and target provisioning. It passed authenticated
backup, restore into a separate empty target, retained source independence and
TLS access through the target pooler in 1,013.949 seconds. The retry had one
pass event, no failures or skips, an unchanged source-tree SHA-256 of
`46569283a6d8dd3ec550ec1836b38a6e1a6dc3e8e0e9d290ca8569c622e8efae`,
bounded JSON SHA-256
`4fcc8c107faa4bf8da1e0bbbd696c955f9261d8a1a13e14b445bd0fd97c5dea8`,
and evidence SHA-256
`8ca531955941a19eef55fe1c7c38c539bc66c22e09fa813b677baf74af16b98d`.
Stderr was empty. All three retry fixture namespaces were absent after cleanup.

## MongoDB 8.0 binding

MongoDB 8.0 binding was qualified on 2026-10-02 against the same named
development cluster and templates revision. The first run exhausted its shared
20-minute parent during certificate renewal and was not counted as passing
evidence. The isolated retry used source
`b7a34ce550aa75a1799d07f9e0d88543cc0f22ff`, which gives provisioning,
application rollout and renewal a bounded 30-minute parent.

`TestManagedMongoDBBindingLive` passed in 1,543.877 seconds with one pass event,
no failures and no skips. The source-tree SHA-256 remained
`46569283a6d8dd3ec550ec1836b38a6e1a6dc3e8e0e9d290ca8569c622e8efae`.
The bounded JSON SHA-256 was
`eb1535c89ef5fcdeb7af270a0556fdcc87c530573966ad2b05cebe63c3ca176e`
and the evidence SHA-256 was
`e2c2ceb02160b86523134ad4fb1bb19e812d5f4766f8e8ab6ca53d0034c6c220`;
stderr was empty. Its disposable fixture namespace was absent after cleanup.

These PostgreSQL and MongoDB results qualify the named development cluster and
the stated immutable source and template revisions. They do not claim a
production deployment.
