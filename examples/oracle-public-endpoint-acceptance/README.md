# Oracle Database Free public TCPS acceptance

This source-only harness qualifies one exact Hakopod release candidate for an
Oracle Database Free `read_write` public endpoint. It is deliberately separate
from the production gate. The checked-in source keeps
`oracleFreePublicEndpointsQualified = false`; the runner never edits that gate
or builds a server.

The coordinator must supply two independently built and approved server binaries
plus the shared fault-proxy helper built from the same isolated source snapshot:

- `qualification_binary` was built from a separately reviewed source snapshot
  where the Oracle Free publication gate alone is enabled. Its protected
  approval names that source manifest, binary hash, reviewers and expiry.
- `withdrawal_binary` was built from the gate-closed candidate in this source
  snapshot. Its protected approval must bind this exact source manifest and
  binary hash. The runner restarts under it before the final revocation.
- `fault_proxy_binary` was built from
  `examples/public-endpoint-fault-proxy`. The planner binds its digest into the
  reviewed plan. It relays Kubernetes API traffic through an owned loopback
  `kubectl proxy`, permits pod-exec upgrades only for the owned database and
  HAProxy namespaces, and injects the selected bounded fault without logging
  request URLs, headers or bodies.

Both approval files use this strict shape:

```json
{
  "schema_version": 1,
  "purpose": "oracle-free-public-tcps-qualification",
  "binary_sha256": "64 lowercase hex characters",
  "source_manifest_sha256": "64 lowercase hex characters",
  "gate": true,
  "reviewed_by": ["root", "oracle_transition_review"],
  "approved_at": "RFC3339 timestamp",
  "expires_at": "RFC3339 timestamp within eight hours",
  "nonce": "32 lowercase hex characters"
}
```

Use purpose `oracle-free-public-tcps-withdrawal` and `gate: false` for the
withdrawal binary. The qualification source manifest must be distinct from the
gate-closed candidate; the withdrawal source manifest must equal the candidate
manifest.

## Safety boundary

The planner is read-only. It checks the exact `k3d-hakopod-dev` context and two
named nodes, current root and Oracle-review lane grant, cgroup-backed host
capacity approval, absence of active managed-database or acceptance workloads,
Oracle replacement capacity, exact Oracle and HAProxy images, a free TCP 15432
allocation, local-path storage for two 10 GiB claims plus reserve, source and
binary hashes, and a fresh external-probe attestation. It renders the reviewed
HAProxy chart but does not install it.

The external provider is mandatory. Host-network pods on the two development
nodes are used only for local allowed/denied-source checks and do not count as an
outside-cluster probe. `external_probe_command --preflight CONFIG` must emit:

```json
{
  "schema_version": 1,
  "provider": "approved_runner",
  "source_cidr": "198.51.100.24/32",
  "outside_development_cluster": true,
  "inventory_observed_at": "RFC3339 timestamp no more than five minutes old",
  "expires_at": "RFC3339 timestamp within two hours",
  "nonce": "32 lowercase hex characters"
}
```

Use the actual routed source `/32`; the documentation address above is only a
shape example. During the run the provider receives a bounded JSON request on
stdin. It must run the supplied probe logic from outside the development
cluster and return structural facts only. It must never echo the password or CA
PEM. The response must prove `APP|FREEPDB1`, the exact served leaf fingerprint,
wrong-CA, wrong-hostname and wrong-password rejection, plaintext rejection,
refusal on raw ports 1521 and 5500, and a valid reconnect after every negative
check. Its response must repeat the exact preflight provider and nonce so the
run cannot be substituted after the reviewed attestation.

The configuration is a mode-0600 JSON object with exactly these fields:

```text
schema_version, postgres_admin_url_file, psql, kubeconfig, kubectl,
qualification_binary, withdrawal_binary, fault_proxy_binary, qualification_approval_file,
withdrawal_approval_file, probe_image, probe_command, app_domain,
public_address, public_domain, public_port, tls_issuer, ingress_class,
proxy_namespace, proxy_configmap, proxy_release, allowed_node, denied_node,
server_environment, docker, helm, haproxy_chart, host_budget_file,
external_probe_command, external_probe_config_file
```

Every image supplied through the configuration must be digest pinned. Protected
files must be owned mode-0600 regular files and may not be symlinks. The server
binaries and HAProxy chart must be inside the isolated source snapshot. The
external provider executable is separately owned and hashed. The runner rejects
any attempt to override its database URL, key, kubeconfig, listen address,
public endpoint settings, configuration file or Oracle gate through
`server_environment`.

## Native checks

The runner creates one disposable Oracle Database Free standalone database with
one owned StatefulSet, one pod, and exactly two PVC/PV bindings for data and
backup. It records hashes of Secret data but never records a Secret body,
password, private key or bearer key. Evidence may contain UIDs, certificate
fingerprints, ordered DNS names and backing-volume fingerprints.

It exercises:

- stale review and concurrent maintenance rejection;
- a real duplicate TCP allocation conflict;
- interruption and restart at `identity_issuing`, `identity_rolling` and
  `identity_converging` without writing operation phases;
- authority revocation before issuance while the actual NetworkPolicy remains
  closed;
- singleton pod replacement with the StatefulSet UID and both PVC, PV, claimRef
  and backing-volume identities unchanged;
- exact `APP` and `FREEPDB1` selection through TCPS with the reviewed CA,
  hostname and leaf fingerprint;
- durable row and privilege checks, negative TLS/authentication/plaintext cases,
  raw 1521/5500 refusal and a valid reconnect after each negative;
- allowed and denied source CIDRs plus the mandatory outside-cluster provider;
- HAProxy route-create/reload failure and route-close/master-socket failure with
  the allocation retained;
- a failed post-convergence recovery that never opens the route;
- certificate renewal with changed CA and leaf fingerprints, retained public
  SAN, data, StatefulSet and two volume identities;
- closure of an existing Oracle session; and
- final revocation after restart under the separately approved gate-closed
  binary.

The runner preserves the database and port allocation if endpoint closure or
identity cleanup cannot be proven. It deletes the database only after every
known endpoint route and claim is gone. It then verifies both owned PVs and all
late bindings have disappeared before removing the control database. Runtime
credentials remain in process memory and are never written to an acceptance
recovery file.

## Probe image

Build the static probe and shared fault proxy only in the approved VM build
lane. Build the helper from the candidate source without rewriting module
metadata:

```sh
go build -trimpath -o /approved-scratch/bin/public-endpoint-fault-proxy \
  ./examples/public-endpoint-fault-proxy
```

Set `fault_proxy_binary` to that absolute executable path before planning. The
planner records its SHA-256, reinspection checks it again immediately before
mutation, and the final evidence requires the qualification, withdrawal and
helper binaries to retain their reviewed hashes.

Set `TEST_KUBECTL` to the absolute path of the same approved `kubectl` binary
when running the helper tests. This enables the integration test that sends
real client-go remote-command SPDY streams through the helper and a real
`kubectl proxy` to an in-process fake Kubernetes SPDY API. A missing
`TEST_KUBECTL` skips that integration case and must be recorded as unverified.

Then place the static probe at
`examples/oracle-public-endpoint-acceptance/probe` and build the accompanying
scratch image. Supply the resulting immutable image digest as `probe_image`.
The repository dependency `github.com/sijms/go-ora/v3` provides the native
Oracle protocol; no Oracle client installation or wallet password is needed.

## Execution

After the coordinator has created the protected configuration, host budget,
lane grant, binary approvals and external-provider input, run these sequentially
inside the granted VM lane:

```sh
python3 scripts/plan-development-oracle-public-endpoint-acceptance.py \
  --source /approved-scratch/oracle-public-endpoint-source \
  --root /approved-scratch --config /protected/config.json \
  --lane-grant /protected/lane-grant.json --attempt 1
python3 scripts/run-development-oracle-public-endpoint-acceptance.py \
  --source /approved-scratch/oracle-public-endpoint-source \
  --root /approved-scratch --config /protected/config.json \
  --lane-grant /protected/lane-grant.json \
  --plan /approved-scratch/oracle-public-endpoint-v1.plan.json --attempt 1
```

Do not run the runner on a workstation, an existing operator cluster, or without
an explicit current lane grant. A source test, mocked test, preflight plan or
successful build is not native acceptance. Qualification requires the completed
protected event log and evidence report from this runner, followed by review of
the exact release candidate.
