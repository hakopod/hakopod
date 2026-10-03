# Operate managed platforms

Neon and Supabase run several services with different jobs. Hakopod keeps each
stack under one platform record, with its own project, environment, revision,
resource claims and operation history. Vitess belongs to the managed database
catalog and uses the database commands instead.

The current published release keeps Neon and Supabase creation, backup and
restore unavailable. The commands and pages below describe the implemented
workflow; they become usable only after the exact runtime images and native
recovery path pass release qualification.

Check the installed release's catalog before creating anything. A platform
appears as available only when its runtime and the installation's operator
configuration allow it. An image download or a passing build does not approve
another cluster's storage, network or capacity.

## Start with the catalog

Open **Managed platforms**, select the project and environment, then choose
**Create platform**. The guided pages collect the platform, resources,
placement and settings before the review. The catalog supplies the allowed
nodes and immutable secret references for that scope. It never supplies secret
values to the form.

The CLI reads the same API:

```sh
hakopod platform catalog --project demo --environment development
hakopod platform list --project demo --environment development
hakopod platform review --file supabase.toml --project demo --environment development
```

Review the component allocations, persistent storage, node placement and
capability reason. Fix a blocked plan before applying it. Resource values are
reservations, not measured CPU or memory use. Start with the platform's sizing
requirements and measure the application workload before adjusting them.

Keep the platform's overall status separate from its component observations.
An accepted operation means Hakopod has recorded the work. A ready component
means that component passed its current readiness checks. Neither establishes
that an application's queries, sign-in flow or recovery procedure have been
tested. After creation, connect with the scoped application credential and
exercise the path the application will use.

```sh
hakopod platform apply --file supabase.toml --project demo --environment development
hakopod platform operations PLATFORM_ID
hakopod platform operation OPERATION_ID
```

Replace the uppercase IDs with the values returned by the API. Apply returns a
durable operation; an accepted HTTP request does not mean that the stack is
ready. Inspect the operation and the platform's observed state.

## Understand what the operator approves

Standalone Hakopod and the Cloud embedding use the same runtime configuration
loader. `HAKOPOD_MANAGED_PLATFORM_CONFIG_FILE` points to a strict versioned TOML
file, owned by the operator, with mode `0600`. It supplies the pinned image
inventory, runtime identities, encrypted StorageClass approval and scoped
secret snapshots. Customers cannot replace this file through platform input.

The release binding identifies the exact Kubernetes cluster and StorageClass,
including its UID, provisioner and parameters. The operator must also retain
the provider evidence that establishes encryption at rest. Naming a class
`encrypted` is not that evidence. Replacing the class or changing its parameters
invalidates its approval.

Cloud additionally requires a current workspace capacity grant. A global
capacity section in the runtime TOML cannot override that grant. The catalog,
planning and execution paths check the approved nodes and scope. Node UID,
operating system and architecture are checked against the live inventory.
One observed node is sufficient for a standalone Supabase grant; ordinary
managed database grants retain their two-node minimum.

The following redacted shape shows every capacity and node field. Replace each
uppercase placeholder with values observed from the installation. A
self-hosted operator includes this block in its complete managed-platform TOML;
Cloud supplies the same fields through its scoped workspace grant instead.

```toml
schema_version = 1
approved_encrypted_storage_class = "REPLACE_WITH_STORAGE_CLASS"
shared_storage_gid = 10001

[capacity]
enabled = true
pool = "REPLACE_WITH_CAPACITY_POOL"
storage_class = "REPLACE_WITH_STORAGE_CLASS"

[capacity.capacity]
cpu_milli = 8000
memory_bytes = 34359738368
storage_gib = 256

[[capacity.nodes]]
name = "REPLACE_WITH_NODE_NAME"
uid = "REPLACE_WITH_NODE_UID"
architecture = "amd64"
operating_system = "linux"
```

The full file must also contain the selected platform's complete digest-pinned
image and runtime-identity inventory. A release-qualified Supabase or Neon
installation additionally needs its platform-specific
`[supabase_qualification]` or `[neon_qualification]` binding, populated from
reviewed evidence for this exact cluster and StorageClass. Do not copy a
binding, node UID or evidence digest from another cluster.

The [complete Supabase operator template](../examples/managed-platforms/supabase-operator.toml)
includes all eleven images and identities, the storage binding and all 27
required secret snapshots. It deliberately uses zero process identities and
unapproved storage, so the server refuses it until the operator fills it in.
Its capacity numbers are an example allocation, not a workload sizing promise.
Cloud operators omit its capacity sections and use the workspace grant.

The [complete Neon operator template](../examples/managed-platforms/neon-operator.toml)
contains the eight image and identity entries, private control-plane trust,
release and StorageClass binding, and all eight secret snapshots. Its image
digests, process identities and approval flags are deliberately unusable. Copy
the release image inventory and identities from the matching qualification
manifest; do not infer them from tags. Replace the public HTTPS CIDR with the
smallest canonical ranges required by the object store and other approved
external endpoints. The control-plane origin must be an exact HTTPS origin,
and its namespace and labels must select only the Hakopod API pods that the
proxy may reach. `neon_proxy_token` authenticates that private control-plane API and
must contain at least 32 random characters.

Set `neon_proxy_control_plane_ca_pem` to the CA certificates that verify that
HTTPS server. Use a TOML multiline string with certificate PEM blocks only.
The server certificate must cover the hostname in
`neon_proxy_control_plane_origin`. The bundle may contain up to 16 CA
certificates and 64 KiB; private keys and ordinary server certificates are
rejected. This is public trust material, separate from the proxy's bearer token.

The proxy receives this bundle through an immutable ConfigMap for the reviewed
platform revision. Changing the operator file does not rewrite an existing
revision's trust. Review and apply a new platform revision when changing the
control-plane CA, with both old and new CAs present during a planned overlap.
Managed platform TLS still needs this setting: Hakopod's per-platform issuer
does not issue the shared control-plane server's certificate.

For self-hosted installations, keep the template's capacity sections and bind
at least three explicit node names to their live Kubernetes UIDs; the selected
Neon platform must name every storage member and therefore may require more.
For Cloud, remove the capacity sections; the workspace grant remains authoritative. In both modes, replace
the qualification values with evidence for the live cluster and encrypted
StorageClass. Hakopod recomputes the image inventory and canonical
StorageClass-parameter digests and compares the live cluster, class UID and
provisioner before startup and again before platform or recovery changes.

Hakopod generates Neon's internal controller and storage tokens for each
platform. The operator supplies the compute configuration, compute-control
credential, proxy credential, controller-database password and object-storage
access key pair. With operator-provided TLS, the snapshots also contain the
service certificates. Review checks the compute template before accepting an
operation. Keep the certificate authority relationships and service hostnames described in the
[Neon security contract](managed-neon.md); placeholder PEM, JSON and token
values in the repository template are documentation only and cannot authorize
a deployment.

Copy the template to a protected location before adding credentials. Set its
owner to the account that runs the API and its mode to `0600`, then set
`HAKOPOD_MANAGED_PLATFORM_CONFIG_FILE` to its absolute path in the service
configuration. Keep the existing persistent authentication encryption key.
The completed TOML contains secret values; do not commit it or paste it into
a support request. These strings are values, not file references or environment
variable substitutions. Use TOML multiline strings for PEM, SQL and YAML.

Each database client needs its own role credential. The role-bootstrap SQL
must set the same passwords used by the Auth, REST, Realtime, Storage,
postgres-meta and Supavisor snapshots. The Supavisor snapshot contains a
`value` URL for the `pgbouncer` role in the `_supabase` database and a matching
`password` value; that role owns only the `_supavisor` metadata schema. Auth, REST and Storage connection URLs
must verify the `db` hostname using the mounted database CA. Database
certificates must cover both `db` and its namespace-qualified Service name;
the gateway certificate must match the configured HTTPS origin. The Envoy
listener snapshot carries the matching API keys, administrator credentials
and TLS configuration. The runtime checks the URL, certificate and listener
requirements before applying workloads. See the [Supabase security contract](managed-supabase.md#security-and-ownership-contract)
for the role, certificate and listener requirements.

The application JWT secret and pooler administration JWT secret must differ.
The publishable and secret API snapshots each include both their `value` and
the Edge Runtime's `edge-json` representation. A secret reference in a platform
spec, such as `{ name = "anon-key", revision = 1 }`, resolves the
`anon-key-r1` snapshot only under that platform's project and environment.
Restart the API after installing a new operator configuration, then inspect
the scoped catalog before applying a platform. A restart does not itself
replace the platform workloads or apply a new desired revision.

Cloud BYO node control reads its per-node operator file, creates a private
digest-named snapshot and mounts it read-only into the API. A changed snapshot
requires an API replacement. It does not replace the database processes or
their volumes. An unsafe file, missing source or mismatched runtime approval
keeps provisioning closed.

## Change an existing platform

Use **Configure** from the loaded platform. The detail's project and
environment stay attached to the operation. The review binds the proposed
change to the current revision; a concurrent update requires another review.

```sh
hakopod platform show PLATFORM_ID
hakopod platform update PLATFORM_ID --file supabase.toml
```

Updates preserve entered values after a request fails. The worker records
ownership before external creation and checks observed resource identities
before changing them. After a process restart, it resumes the durable operation
instead of assuming that a matching Kubernetes name belongs to it.

Neon configuration has two steps: **Resources** and **Review**. It changes CPU
and memory allocations only. Storage size, node placement, compute and storage
member counts, version and secret references are preserved from the loaded
resource. The API rejects attempts to change those fields in place. To change
storage or topology, restore into a separate platform with the desired setup.
An allocation change can restart services and interrupt active connections.
Wait for the operation to finish and check an application query afterward.
If another update wins first, reload the current revision and review the
change again; do not reuse a stale approval.

For Supabase, database password rotation is one complete bundle: the role
bootstrap and all database client references move together. A successful
rollout precedes old snapshot removal. JWT expiry can change through a reviewed
revision. Signing keys, stored-data encryption keys and object-store access
keys have different retirement requirements; unsupported rotations are
rejected rather than treated as password changes.

## Back up the whole stack

Choose **Back up** and an approved destination. The Supabase recovery candidate
captures the database, roles, encryption files, objects, function files and
Studio snippets. The Neon recovery candidate preserves the tenant, timeline and
storage identities that make its data readable. These paths remain unavailable
until their native qualification passes. Consult the platform-specific recovery
contract before choosing a retention policy.

Recovery is a separate durable operation. A CLI request uses strict TOML:

```toml
schema_version = 1
kind = "backup"
project = "demo"
environment = "development"
source_platform_id = "REPLACE_WITH_PLATFORM_ID"
expected_source_revision = 1
destination_id = "REPLACE_WITH_DESTINATION_ID"
destination_revision = 1
```

The placeholder IDs intentionally fail validation. Use the actual IDs and
current revisions from your installation, then review and apply:

```sh
hakopod platform recovery-review --file recovery.toml
hakopod platform recovery-apply --file recovery.toml --idempotency-key nightly-platform-backup-001
hakopod platform recovery-operations PLATFORM_ID
hakopod platform recovery-operation OPERATION_ID
```

Choose a new idempotency key for a new operation and preserve it while retrying
the same request. Check the terminal result and recorded artifact before
counting the backup as recoverable.

## Restore and inspect a separate target

Create a compatible empty target first. In **Restore**, select the artifact and
target, check both revisions, and confirm the target's exact name. The source
and target must share the authorized project, environment and platform kind.

Restore checks the archive before writes. The target stays isolated while
recovery is in progress. Inspect database rows and the platform's application
paths, including uploaded objects or functions where relevant. A restored
target does not include source writes made after the capture point. Plan the
application cutover separately.

For Neon, verify the restored tenant and timeline through the platform's
recorded recovery result, then query known rows through its own endpoint and
credential. For Supabase, check more than the database: sign in, fetch an
uploaded object and invoke an authenticated function if the application uses
those services. Keep the source until these checks and the application
cutover have completed.

Use a restore request with the completed backup's artifact ID, the separate
target's current revision and its exact current name:

```toml
schema_version = 1
kind = "restore"
project = "demo"
environment = "development"
source_platform_id = "REPLACE_WITH_SOURCE_PLATFORM_ID"
expected_source_revision = 1
artifact_id = "REPLACE_WITH_ARTIFACT_ID"
target_platform_id = "REPLACE_WITH_TARGET_PLATFORM_ID"
expected_target_revision = 1
confirm_target_name = "exact-target-name"
```

```sh
hakopod platform recovery-cancel OPERATION_ID
```

Cancellation requests cleanup; it does not erase work already performed. The
worker preserves its cleanup lease while restoring paused source services or
removing owned temporary recovery resources. Runtime approval is rechecked at
review, acceptance and before recovery mutations. If that approval changes,
new recovery work stops while cleanup can still finish under its existing
lease.

## Delete deliberately

The delete page shows the loaded platform and requires its exact name. Keep a
verified backup and inspect any dependent applications before submitting.
Deletion removes owned runtime resources and persistent data according to the
platform's deletion contract. A successful terminal operation follows observed
resource absence; a submitted request alone is not proof of deletion.

See [Managed Supabase](managed-supabase.md), [Managed Neon](managed-neon.md),
[Supabase qualification](managed-supabase-qualification.md) and
[Neon qualification](managed-neon-qualification.md) for each stack's supported
behavior and recorded acceptance limits.
