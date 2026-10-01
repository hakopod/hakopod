# ClickHouse public endpoint acceptance source

This is a disposable development probe. Build and execute it only in the
approved VM lane and named development cluster. Passing source tests or
compiling the probe does not qualify a public endpoint. The runtime image
is pinned to the same ClickHouse 26.3.33.24 image as the backend. No Go database
driver is added: native queries use that image's `clickhouse-client`.

Supply `CLICKHOUSE_PASSWORD` as a protected Secret environment variable. The
probe uses user `app` and database `app`. It never prints credentials, SQL error
bodies or subprocess diagnostics. Native credentials are placed in a temporary
mode-0600 client configuration, removed after each subprocess; the password is
removed from the child environment and never enters argv. Mount a bounded
writable `/tmp` (for example, a 16 MiB memory-backed volume) when using a read-only
container filesystem. Do not enable shell tracing or debug command logging.

Example:

```
/probe --host database.example.test --port 15432 --ca /acceptance/ca.crt \
  --expect-address 192.0.2.10 --purpose native --check verified --shards 2 --replicas 2
```

`--purpose` is `native` or `https`. `--shards` is 1, 2 or 8;
`--replicas` is the actual number of members per shard, 1, 2 or 6. A value above
one for either means the fixture has the backend's Replicated `app` database.
These flags select the fixture table layout; they do not independently prove
physical shard or replica inventory. The runner must check that inventory.

Supported checks:

- `verified`: authenticate with strict private-CA and public-hostname validation,
  then require `currentUser()` and `currentDatabase()` to return `app`.
- `crud`: replace the two fixed `app.public_endpoint_acceptance*` fixture tables.
  Use ReplacingMergeTree (ReplicatedReplacingMergeTree for clusters), write an
  initial row per shard, append a newer binary payload and insert then tombstone
  scratch rows. Verify visible data through `FINAL`. Cluster inserts use a
  Distributed table with `distributed_foreground_insert=1` and `id % shards`.
  Updates/deletes here mean versioned replacement and logical tombstone deletion,
  not ALTER mutation execution or physical data erasure. The original data stays
  in the fixture for later checks. Run only against a disposable acceptance DB.
- `data-preserved`: read those existing binary rows without recreating tables,
  then verify new writes and tombstones. Do not run `crud` between the original
  write and the recovery/replacement assertion.
- `plaintext-rejected`, `wrong-hostname-rejected`, `wrong-ca-rejected`, and
  `bad-password-rejected`: require the specific expected transport, certificate,
  or authentication refusal. Each check is bracketed by successful authenticated
  queries. Negative clients use invalid passwords where plaintext is attempted.
- `unreachable`: resolve DNS successfully, then require TCP refusal/reset/timeout
  or remote closure during a correctly configured TLS handshake. DNS errors,
  certificate failures and malformed protocol responses cannot satisfy it.
  This is not by itself proof of a CIDR rule: outages look similar. The runner
  must execute it from the denied source and bracket it with successful queries
  from an allowed source. The denied probe cannot perform that cross-source
  bracket itself. The selected IPv4 address must match the reviewed allocation.

Success emits `PASS <check>`. Failures exit nonzero with a fixed message.
The harness supplies `--expect-address` with the reviewed public allocation or
the owned private Service ClusterIP. Before any query or refusal check, the
probe requires every DNS result to match that IPv4 address; additional IPv4 or
IPv6 destinations fail the check. It pins the approved numeric destination for
all steps while retaining the hostname for TLS verification. DNS results are
bounded to 16 entries. Direct examples may omit the flag to select and pin the
first IPv4 result; that mode does not prove the full DNS set was reviewed. The
example hostname and address above are documentation placeholders.

For an existing-session revocation check, use `--check verified` and
`--expect-revocation-within 4m`. The probe prints:

```
READY tls=TLSv1.3 transport=native
REVOKED existing ClickHouse session closed
```

TLSv1.2 and `transport=https` are also valid. Wait for READY before revoking the
endpoint. There is no `PASS` line for a revocation invocation.

HTTPS uses one explicitly opened TLS connection and sequential HTTP/1.1 queries.
It never redirects or reconnects. Native uses a one-connection loopback TCP relay
and the pinned client's end-to-end TLS. The client authenticates, emits its
identity, and runs one bounded streaming query. The relay permits no second
connection, so client reconnection cannot masquerade as an existing session.
Revocation requires a native transport error and independently observed remote
EOF/reset before the deadline. Timeouts, auth failures and query errors do not
count. This source still requires native execution to verify the pinned client's
stream flushing and exact error text. If those differ, it must fail rather than
weaken the revocation assertion.

`--expect-fingerprint` accepts a SHA256 leaf digest. HTTPS pins that digest on the
actual query/session connection. Native checks it in a separate verified TLS
preflight; the actual client independently verifies CA and hostname. Native does
not claim an exact same-session fingerprint match: TLS 1.3 encrypts the leaf,
which the transparent relay cannot inspect. READY's native TLS version comes
from the actual client's bounded plaintext ServerHello. Backend acceptance must
separately verify the expected served leaf on both ports and all members.

`--mode hold` idles until SIGTERM/SIGINT or 24 hours, for bounded `kubectl exec`
probes. The Dockerfile expects a Linux `/probe` binary built in the approved VM
lane. From repository root in that lane, compile this package to the example's
`probe` path, then build the Dockerfile and record the resulting image digest.
Run the source tests there before any acceptance claim. No local build artifacts
or qualification gate changes are included here.

## Running the complete harness

The planner, runner and probe are separate parts of one acceptance attempt:

- `scripts/plan-development-clickhouse-public-endpoint-acceptance.py` reads the
  named development cluster, Docker and cgroup limits, current workload
  reservations, the installed HAProxy controller, and the actual local-path
  storage backing filesystems. It renders the pinned HAProxy chart without
  installing it. It writes a protected plan only when the selected fixture fits.
- `scripts/run-development-clickhouse-public-endpoint-acceptance.py` consumes that
  plan, takes the shared native-acceptance lock, and repeats the capacity and
  ownership reads immediately before its first mutation. A changed host,
  source tree, configuration, binary, grant, route allocation or storage identity
  invalidates the plan.
- This directory supplies the pinned-client probe used inside three owned pods:
  allowed and denied host-network sources, plus a private application source.

Use an isolated Linux amd64 source snapshot below the approved scratch root.
The planner and runner require a bounded non-root systemd lane registered in the
shared host budget, a current grant from both coordinators, and context
`k3d-hakopod-dev`. They do not provision a VM, resize a node, install an operator,
change the shared HAProxy deployment or delete its TCP CRD. The control PostgreSQL
fixture and the pinned ClickHouse operator must already be available. No native
run is authorized by this document.

The production ClickHouse publication gate stays closed. A coordinator must
prepare and approve a separate qualification snapshot with that gate enabled,
build its server and probe on the VM, and record their exact hashes. The planner
hashes the source, embedded template assets, probe, configuration, server and fault proxy binaries,
host budget and lane grant. Changing the snapshot after planning requires a new
plan. A successful mocked test is not a reason to open the production gate.

The protected runner configuration shares the PostgreSQL/MySQL harness fields:
`schema_version`, `postgres_admin_url_file`, `psql`, `kubeconfig`, `kubectl`,
`server_binary`, `fault_proxy_binary`, `probe_image`, `probe_command`, `app_domain`, `public_address`,
`public_domain`, `public_port`, `tls_issuer`, `ingress_class`, `proxy_namespace`,
`proxy_configmap`, `proxy_release`, `allowed_node`, `denied_node`,
`server_environment`, `docker`, `helm`, `haproxy_chart`, and `host_budget_file`.
It adds `fixture_shape`. Inputs must be owned mode-0600 regular files. Use secret
file references; never paste credentials into commands or evidence.

`public_port` is 15432. Native and HTTPS are tested sequentially on this reserved
port. `public_address` must be the development server node's private IPv4 address;
`database-15432.<public_domain>` must resolve to exactly that address from the
probe pods. The runner passes it as `--expect-address`. The private probe instead
pins the owned database Service's actual ClusterIP. The allowed and denied nodes
must be the two distinct named development nodes.

Choose one shape per attempt:

| Shape | Data members | Keeper | Requested PVC space |
| --- | ---: | ---: | ---: |
| `standalone` | 1 | 0 | 2 GiB |
| `cluster` | 2 shards with 2 members each | 3 | 11 GiB |
| `maximum` | 8 shards with 6 members each | 3 | 99 GiB |

Each data member has 500m CPU, 2 GiB memory, a 1 GiB data claim and a 1 GiB backup
claim. Each Keeper has 250m CPU, 256 MiB memory and a 1 GiB claim. Each probe has
250m CPU and 256 MiB memory. The plan reserves one extra data member, one extra
Keeper for clustered shapes, and two probes on each eligible node. Because the
API does not pin individual members to individual nodes, each node must fit the
whole fixture peak. This is deliberately conservative; the planner does not
assume an unforced balanced placement. Storage must retain the complete PVC
allocation plus 16 GiB of free space on its real backing filesystem.

`maximum` is a separate optional capacity qualification. It is not part of an
ordinary cluster run. Results identify their exact shape; a standalone or
four-member result says nothing about 48-member runtime deadlines. None of these
single-development-cluster cases proves multi-cloud latency or zone failover.

After the coordinator has prepared the configuration, budget, grant and snapshot,
run these commands only inside its approved VM lane:

```sh
python3 scripts/plan-development-clickhouse-public-endpoint-acceptance.py \
  --source /approved-scratch/clickhouse-public-endpoint-source \
  --root /approved-scratch --config /protected/config.json \
  --lane-grant /protected/lane-grant.json --attempt 1
python3 scripts/run-development-clickhouse-public-endpoint-acceptance.py \
  --source /approved-scratch/clickhouse-public-endpoint-source \
  --root /approved-scratch --config /protected/config.json \
  --lane-grant /protected/lane-grant.json \
  --plan /approved-scratch/clickhouse-public-endpoint-v1.plan.json --attempt 1
```

The runner creates its own control database, scoped key, managed database and
probe namespace. It moves the owned database to the recognized legacy identity
projection, removes the unused client leaf, and proves that ordinary maintenance
recreates the client identity and replaces the legacy data pods. It checks the
private native and HTTPS endpoints afterward. Public SAN changes must preserve
the internal certificate, CA bytes, Keeper template and Keeper pod identities.

For each public transport, it checks data and negative TLS/authentication cases,
source allowlisting, same-key CA renewal with the client's original CA, data-node
replacement and Keeper replacement. A fixture-only trigger in the isolated
control database delays `next_attempt_at` at the real `accepted`, `identity`,
`tls`, `publishing` and `observing` phases. The runner kills and restarts its own
API at each checkpoint. It never writes a replacement operation phase. The final
checkpoint must have an actual acknowledged HAProxy route.

Revocation holds a real client connection open. An owned loopback Kubernetes API
proxy can temporarily refuse the HAProxy master-socket call or hide the TCP CRD
from this runner alone. The allocation must stay reserved until normal access is
restored and closure succeeds. A separate restart check interrupts revocation
after the route closes. Another case pauses a real acknowledged publication,
replaces a data pod, then makes HAProxy closure temporarily unavailable. The
worker must retain `identity_closing`, close the held session after restart,
reject the stale topology review and keep the allocation until explicit
revocation. No shared HAProxy process or CRD is killed to simulate these faults.

Cleanup uses the supported endpoint and database APIs, checks route and allocation
removal, and waits for every data, backup and Keeper volume to disappear. Probe
namespace deletion is fenced by the observed UID and resource version. If endpoint
closure fails, the runner preserves the database and its allocation instead of
manually freeing the port. Incomplete cleanup makes the attempt fail. The owned
control database and protected `recovery-runtime.json` remain for the coordinator
to recover that fixture; this file contains credentials and must never be printed
or attached to a report. It is deleted after successful cleanup.

Keep the protected event log, structural evidence, migration identity inventory,
exact source manifest and image digests. The evidence marks this as a qualification
fixture and names its shape. Publication still needs real results for the desired
scope, review of failures, and explicit release qualification.

## Fault proxy prerequisite

Build the shared `examples/public-endpoint-fault-proxy` helper in the isolated
source snapshot and set `fault_proxy_binary` to its absolute path in the planner
configuration. Build it on the development VM:

```sh
go build -o ./fault-proxy ./examples/public-endpoint-fault-proxy
```

The plan records the helper source and executable hashes. The runner checks them
again before starting a fixture and before launching the helper. The helper
forwards Kubernetes exec upgrades through the owned loopback kubectl proxy.
Exec traffic is limited to the fixture's database and HAProxy namespaces.
Its mode is fixed at startup, and shutdown closes both owned processes.
