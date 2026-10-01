# MySQL public endpoint acceptance source

This disposable probe verifies MySQL protocol TLS negotiation to the public
Router writer and reader endpoints. It trusts only the supplied database CA,
verifies the public hostname, and never prints passwords or driver errors.
Supply `MYSQL_PWD` through a protected Kubernetes Secret. The image containing
this program must be digest-pinned; `--mode hold` supports bounded exec probes.

Example arguments (the password is supplied separately):

```
/probe --host database-15432.database.example.test --port 15432 \
  --ca /acceptance/ca.crt --purpose read_write --check verified
```

The checks are:

- `verified`: authenticate and verify session TLS plus `@@super_read_only`.
- `crud`: writer-only binary insert/update/read/delete; retain one fixture row
  for the subsequent reader assertion. The runner deletes the whole database.
- `data-preserved`: verify the original binary row before any new write, then
  exercise a new insert/delete on the writer.
- `reader-write-rejected`: read that binary row and require MySQL error 1290
  for an attempted write to its existing table.
- `plaintext-rejected`, `wrong-hostname-rejected`, `wrong-ca-rejected`, and
  `bad-password-rejected`: first verify the valid endpoint, require the specific
  negative failure, then verify a fresh valid connection again.
- `unreachable`: require connection failure without accepting DNS or MySQL
  authentication errors as proof. Use only with passing allowed-source checks.
- `--expect-revocation-within 4m`: hold one physical authenticated session and
  require it to close; a timeout does not count as session closure.

Exact stdout is `READY tls=TLSv1.2 read_only=false` (or TLSv1.3/true),
`PASS <check>`, or `REVOKED existing MySQL session closed`.

## Runner

`scripts/run-development-mysql-public-endpoint-acceptance.py` is adapted from
the PostgreSQL runner and retains its durable API publish/revoke, worker crash,
missing TCP CRD, master-socket fault, ownership, allocation-retention, source
hash, and cleanup checks. It creates a separate PostgreSQL **control store**
and a three-member MySQL 8.4 **managed fixture**, with two Router instances.
Writer and reader reuse the approved TCP allocation sequentially after verified
revocation. Backend routes must target `database:6446` and `database:6447`.

Use the same protected configuration fields as the PostgreSQL runner, including
`postgres_admin_url_file` and `psql` for the control store. Supply a new protected
lane grant with purpose `mysql-public-endpoint`, approvals `root` and
`vitess_backend`, cluster `k3d-hakopod-dev`, and a current bounded expiry.
The runner requires an isolated Linux amd64 source snapshot, CPUQuota at most
100%, MemoryMax at most 3 GiB, both named development nodes, and 16 GiB scratch
reserve. The fixture's cluster resource capacity must be separately approved;
the runner's cgroup limits do not reserve Kubernetes workload resources.

The shipping source gate stays false. Before any mutation the runner requires
`const mysqlPublicEndpointsQualified = true` in a separately approved
qualification snapshot. It additionally checks the running server's capability
response before endpoint publication. It never changes the gate itself.

Evidence uses `engine: mysql`, `qualification_only: true`, source hashes,
probe image digest, server binary hash, structured events and cleanup outcomes.
Each publication requires unchanged reviewed database member names, UIDs and
roles after the public SAN rollout. Router UIDs may change. The optional
`--expect-fingerprint` probe flag verifies the exact served SHA256 leaf digest.

The runner also deletes one UID- and resourceVersion-fenced Router pod after
verifying its complete owner chain, then waits for replacement and verifies
persistent data through the same public writer. It uses Python cryptography
42 or later to shorten only the owned fixture CA's expiry to 24 hours, preserving
its key and extensions; a fenced JSON patch sends only the public certificate.
Normal server identity maintenance must renew the issuer and leaf. The public
probe then checks the exact new leaf fingerprint, public SAN, original CA trust
overlap, and original data. No private key is sent to a probe pod or persisted.

Finally, it deletes the verified primary using the same ownership/UID fences,
requires a different elected primary and replacement pod, and verifies the
original row plus new writes through the public writer. The later public reader
must return that original row and refuse writes. This is a primary-loss test;
MySQL has no graceful switchover API in the current server. Subsequent endpoint
reviews deliberately use the recovered topology. Faults never target a shared
operator, controller CR, or other database.

These files are unexecuted acceptance source. They do not establish native
qualification. Router replacement, expiry renewal and primary-loss scenarios
must actually pass on the approved named development cluster before sign-off.

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
