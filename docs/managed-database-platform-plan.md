# Managed database platform expansion

Status: implementation plan and reconciliation snapshot, 7 October 2026. This document does not announce
shipping support for additional engines, public endpoints or new TLS guarantees.
The supported PostgreSQL and Redis lifecycle is described in
[Managed databases](managed-databases.md).

## Product structure

The database detail page separates Overview, Monitoring, Connections & security,
Backups, Activity and Settings. Overview contains observed topology, essential
health facts and member inspection. Monitoring contains measured resource
samples. Connections combines routing, client instructions and security state.
Backups contains captured recovery points, schedules and restore inspection.
Activity records durable operations. Settings contains configuration and deletion.
The selected tab belongs in the URL and must survive reload and browser history.

Avoid making an overloaded console look authoritative through invented data.
Unknown health, missing telemetry and unverified certificate state must remain
unknown. Desired capacity and observed resources are different facts.

## Engine contracts

| Engine | Standalone | Cluster design | Connection routing | Required recovery coverage |
| --- | --- | --- | --- | --- |
| PostgreSQL | One CNPG instance | CNPG primary with streaming replicas | Separate primary and replica services; optional PgBouncer is a separate pooling concern | Authenticated logical capture, empty separate target, same-major restore and supported upgrade |
| Redis | One persistent instance | Native Redis Cluster with primary/replica groups and complete slot ownership | Cluster-aware discovery; a generic TCP balancer cannot hide MOVED/ASK routing | Every primary shard, binary data and expiry, topology fencing, integrity failure before writes |
| MySQL, native development acceptance passed | One member and one Router | Native Group Replication with 3, 5 or 7 voting members; two Routers | MySQL Router primary and secondary routes | Read-locked logical capture, least-privilege restore, verified rejoin and ordered deletion |
| MongoDB, native development acceptance passed | One-member replica set with no HA claim | Native 3/5/7-member replica set through MongoDB Kubernetes Controller | Driver discovery and read preferences; advertised members must be reachable | One snapshot timestamp, raw BSON and metadata, separate empty target, session revocation and inspection-gated ingress; no continuous oplog recovery |
| ClickHouse, native development acceptance passed | One server | Replicated shards with an explicitly allocated Keeper quorum | Native/HTTP query endpoint; distributed tables retain their engine semantics | Table metadata plus data, replication paths, each shard and Keeper-dependent recovery |
| Vitess, native and HTTP development acceptance passed | One managed MySQL shard with its required control components | Vitess tablets, vtgate routing and a topology service; replicas and shards explicitly allocated | MySQL protocol through vtgate; shard routing follows the VSchema | Per-shard archives, topology metadata, separate-target restore and tablet reseed |
| Oracle Database Free, operator acceptance in progress | Oracle Database Free 26ai, version 23.26, through a namespace-scoped Oracle Database Operator | Free has one instance. Enterprise Data Guard has separate source and licensed-image acceptance requirements | Private PDB service over verified TCPS; Enterprise source uses native roles and reviewed graceful switchover | APP-schema Data Pump recovery; final operator lifecycle and recovery acceptance remain open, along with physical RMAN, archived-redo recovery and native Enterprise acceptance |

A supported engine needs the complete vertical slice: strict versioned
configuration, approved digest-pinned images, credential ownership, bounded
controller operations, accurate observation, application bindings, quotas,
backup/recovery, import compatibility rules, deletion/reclamation, CLI/API/UI
parity and named-development-cluster acceptance. Adding an engine to a dropdown
is not implementation. Operator selection and versions must be verified before
these planned engines are enabled. Router, coordinator, agent and backup sidecar
resources must be included in reservations and displayed in allocation reviews.

## Oracle implementation work

Managed Oracle Database is part of the requested scope, including standalone
and clustered deployment. Free now uses Oracle Database Operator 2.2.0 to manage
one instance. Creation remains disabled in the API and dashboard until this
operator path passes native lifecycle, security, renewal, recovery, controller
loss and HTTP/API acceptance on the final source.

Earlier development tests covered a Hakopod-managed StatefulSet. They verified
schema recovery, target quotas, privilege restrictions and session revocation,
but do not qualify the new controller. The edition and customer-image validation
contract does not enable Enterprise deployment.
See [Managed Oracle](managed-oracle.md) for the current
contract and remaining evidence. Data Guard primary/standby orchestration now has
a source implementation. Its runtime gate remains closed until the hardened
operator, licensed image, role changes and recovery pass native acceptance.
RAC uses a different shared-storage and coordination model and must have its own
support decision and acceptance evidence. A generic replica count must never
silently select RAC, Active Data Guard or an extra licensed option.

The chosen product model is Oracle Database Free for standalone use, plus
customer-supplied Enterprise images for licensed deployments. Oracle Database
Free is proprietary software available without a database license fee; it must
not be described as open source. Free does not provide the Enterprise Data Guard
cluster offering. The create flow must separate those editions and explain their
limits before review. Enterprise customers supply an immutable image reference,
a project/environment-scoped registry credential reference when needed, and an
explicit confirmation that their license covers the requested deployment and
options. Passwords, registry tokens and license documents do not belong in TOML,
operation logs or application bindings. An image reference or confirmation is
not evidence that an Enterprise deployment has passed native acceptance.

The upstream [Oracle Database Operator](https://github.com/oracle/oracle-database-operator)
documents [single-instance, Data Guard and TCPS workflows](https://github.com/oracle/oracle-database-operator/blob/main/docs/sidb/README.md).
These are inputs to evaluation, not proof of Hakopod support. Before selecting
an operator release, verify its immutable source, CRDs, database images,
supported Kubernetes/storage combinations and native failure behavior.

- [ ] Define supported editions, versions, architectures and customer-supplied
  license/image entitlements. Keep registry credentials in owned Secrets;
  never accept third-party terms or redistribute restricted database images as
  part of an automated install.
- [ ] Implement the strict configuration, durable create/update/delete reviews,
  quota accounting and owned controller resources. Account for database members,
  Data Guard observers, routing, persistent storage and backup work explicitly.
- [ ] Create least-privilege application schemas and service bindings. Separate
  PDB/service identity from the container database and administrative users.
- [ ] Enforce client and replication transport requirements, TCPS hostname and
  issuer verification, scoped wallet distribution and certificate rotation.
  Track TDE and wallet recovery separately from transport encryption.
- [ ] Observe the real primary/standby roles, transport/apply lag, protection
  mode, health and native telemetry. Check feature entitlement before collecting
  metrics that depend on separately licensed diagnostic or tuning packs.
- [ ] Implement reviewed role-aware endpoints, graceful switchover, failover
  fencing and former-primary reinstatement. Read-only standby access needs an
  explicit supported entitlement and operating mode.
- [ ] Implement bounded RMAN capture and restore, archived-redo retention and
  wallet dependencies. Restore into an isolated target, verify native integrity
  and require inspection before allowing application ingress or cutover.
- [ ] Complete CLI/API/SDK/dashboard parity, real Oracle identities in the
  guided catalog, placement review, resource accounting and operator guides.
- [ ] Run named-development-cluster acceptance for standalone and primary/standby
  lifecycle, privileges, bindings, TLS refusal/renewal, node loss, partition,
  failed failover, recovery/reinstatement, resize, patching and complete cleanup.

Multi-zone or multi-provider availability needs physically independent failure
tests, measured replication latency and an explicit recovery-point objective.
Two nodes on the current development VM cannot establish that claim.

## Placement and failure domains

A node may run in another zone or provider only if it belongs to the same
connected Kubernetes cluster. Initial placement uses strict one-member-per-node
or one-member-per-zone separation, with optional eligible nodes and trusted host
allocation restrictions. Redis's initial strict zone policy requires one zone
per member; it does not implement a more economical per-shard placement policy.

Observe node, zone, region and provider separately. Node labels do not prove
physical independence. Tests with development zone labels prove scheduling,
not cloud-zone failure tolerance. Independent Kubernetes clusters need a
separate replication and disaster-recovery design.

Do not promise zero data loss from replica count. Review asynchronous replication,
commit acknowledgement, election quorum, latency, partition behavior, persistent
storage locality and control-plane availability for each engine. Cross-provider
operation also needs encrypted private transport, reliable DNS, MTU validation,
identity, firewall rules and an explicit cost/latency budget.

## TLS and certificates

Every supported engine should eventually provide TLS for client and replication
traffic, reject plaintext where the configured policy requires encryption, and
supply client examples that verify hostname and issuer. Certificate presence is
not proof that the running process loaded it or that a client verifies it.

The security view should report desired policy and observed enforcement
separately: certificate SANs, issuer, validity dates, public CA fingerprint,
rotation status, native TLS ports, minimum protocol and the last verification.
Private keys must never appear in API responses, logs, revisions or browser
storage. CA download exposes public trust material only. Rotation requires an
overlap window and readiness checks before retiring the old certificate.

The existing application certificate validators and owned immutable Secrets
provide useful primitives. They must be adapted to database ownership and
native protocol handshakes; HTTP ingress TLS is not database TLS. Database
application bindings need the correct CA trust and hostname-verification
configuration, including scoped trust mounts when the CA is private.

## Dedicated public endpoints

Public access is an opt-in capability, not a database default. A dedicated
endpoint needs a reviewed hostname, protocol, route purpose, certificate,
source CIDRs, exposure allocation and connection budget. The backend must reject
unavailable provider support or incomplete TLS rather than create a misleading
public-looking hostname. Existing databases stay private until an explicit
reviewed operation is accepted.

The operation review should identify exactly what becomes reachable, by whom,
and the added resources/cost. Acceptance must fence database revision, endpoint
reservation, certificate identity and permissions in durable state. Enforce
source restrictions at the actual ingress, accounting for NAT and source-IP
preservation. Restrict access by default; do not prefill an internet-wide CIDR.

A generic public TCP proxy is insufficient for all engines. PostgreSQL can route
to its primary service, but needs native PostgreSQL TLS negotiation or a
protocol-aware pooler. MongoDB requires externally reachable advertised replica
members, often through TLS split horizons. Redis Cluster requires reachable
advertised shard members; one ordinary TCP balancer does not replace cluster
client discovery. ClickHouse exposes distinct native and HTTP protocols.
Document these differences instead of claiming one universal cluster URL.

The current self-hosted application TCP ingress has bounded HAProxy listeners,
source filtering, durable port reservations and reload acknowledgement. The
existing managed-cloud profile rejects public application TCP. Database public
access therefore needs an explicit operator/Cloud capability and deployment
policy; it must not bypass the existing mode check. A certificate or a created
Service alone does not prove external reachability.

Acceptance must include an allowed client, a denied source, hostname/CA mismatch,
expired certificate, plaintext rejection, primary loss, stale review, duplicate
reservation, certificate renewal, endpoint removal and removal acknowledgement.
Run exposure tests only in the named development environment; verify real
internet routing separately before any production availability claim.

## Education and release gates

Publish task-oriented guides for create, connect, choose placement, understand
read/write routing, verify TLS, enable/revoke public access, back up, recover and
inspect before cutover. Include engine-specific connection examples without
credentials, supported version matrices, measured resource accounting and exact
limitations. Draft articles should teach the concepts and link to the current
support matrix; planned work must stay labeled until implementation and runtime
acceptance are complete.

## Delivery checklist

The requested engine scope is PostgreSQL, Redis, MySQL, MongoDB, ClickHouse,
managed Vitess and managed Oracle Database.
Each engine needs the full lifecycle and security contract above. Website
content follows verified release availability, not the existence of a catalog
card or an upstream operator.

- [x] Verify MongoDB standalone and clustered lifecycle, resizing, election/quorum
  behavior, recovery, application bindings and TLS renewal. All five candidate
  cases passed on September 30 against the same controller digest and source
  inventory. This does not establish zone/provider outage resilience.
- [x] Publish the qualified MongoDB controller and verify the version tag,
  manifest and all nine image blobs anonymously. The development installer and
  creation flow use that digest; the platform release remains pending.
- [ ] Rerun MySQL scaling after resolving the development node's DiskPressure
  and ephemeral-storage eviction. The prior run reached 5, 7 and then 3 members,
  but Router replacement timed out; that run does not prove a replica-rejoin
  defect. Rerun routing,
  scaling, recovery and quorum acceptance with stable development capacity.
- [ ] Rerun the latest recovery-ingress gates for PostgreSQL, Redis and MySQL
  against native database processes in the development cluster.
- [x] Implement and verify ClickHouse standalone and replicated shards with
  Keeper quorum, native/HTTP TLS, bindings, monitoring and recovery.
  Native standalone recovery and two-shard, two-replica recovery have passed
  in the named development cluster, including binary data, independent source
  and target writes, invalid-input refusal, inspection gates and owned cleanup.
  The multi-shard run also verified the dedicated gVisor compatibility profile
  on both development nodes. Application bindings, trust renewal, Keeper
  faults and Distributed queries have also passed. The final multishard run,
  `clickhouse-recovery-live-v11.log`, passed against the final table-engine
  grants on September 29 in 860.91 seconds, including owned cleanup. It also
  restored Distributed-table routing, verified independent source/target writes,
  and found no staged archives after capture or restore. This is development
  evidence from two nodes on one VM, not production or independent-zone
  availability.
- [ ] Implement and verify managed Vitess, including its ownership and recovery
  boundaries.
- [ ] Verify Oracle Database Free lifecycle, TCPS renewal, application bindings,
  isolated schema recovery, controller loss and HTTP/API operations through the
  Oracle Database Operator in the named development cluster.
- [ ] Verify Oracle Enterprise and Data Guard against the hardened controller
  and a licensed customer image before opening the runtime gate.
- [ ] Complete PostgreSQL public-endpoint native acceptance for engine-specific
  discovery, enforced source restrictions, TLS, durable exposure and revocation.
  The integrated source and synthetic dashboard review do not establish native
  operation or public exposure; other engines and Cloud allocation/DNS/firewall
  lifecycle remain implementation gaps.
- [ ] Verify placement and failure behavior on independent zones/providers;
  record measured recovery objectives and limits per engine.
- [x] Refresh the website from `origin/main` while preserving existing work.
  The September 29 refresh is based on `c969754`; the website repository records
  its preservation details and work in `docs/managed-database-expansion.md`.
- [x] Update the database page, homepage/Platform previews and support matrix
  for the expanded scope. Task guides and article publication have their own
  remaining coverage; website source is unpublished.
- [x] Keep website engine status explicit: released, under validation or
  planned. Do not describe unreleased runtime behavior as available.
- [x] Build and test website v8 on the VM (152 generated pages and 21 passing
  tests) and complete independent rendered review of the database guides and
  articles across themes, viewports and no-JavaScript reading. Native touch
  remains unverified; this does not publish the website or establish runtime
  availability.
- [ ] Rebuild and repeat the affected website rendered review for the latest
  MongoDB, MySQL and “CA certificate” wording source changes, then record merge,
  deployment and release evidence separately.
