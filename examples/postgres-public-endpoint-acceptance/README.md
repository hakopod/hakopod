# PostgreSQL public endpoint probe

This helper is intended for a disposable, digest-pinned acceptance image. It
connects with PostgreSQL native TLS, verifies the supplied private CA and
hostname, and checks that the endpoint serves the reviewed writer or reader
role. `PGPASSWORD` must come from a protected secret source.

The default check prints a bounded `READY` line after the verified query:

```sh
PGPASSWORD='from-a-protected-source' /probe \
  --host database-15432.database.example.test \
  --port 15432 \
  --ca /acceptance/ca.crt \
  --purpose read_write
```

The `plaintext-rejected`, `wrong-hostname-rejected`, and `wrong-ca-rejected`
checks first establish a verified connection, then require the deliberately
invalid connection to fail. `unreachable` requires the verified connection to
fail and is used only alongside a passing probe from a separately verified
source address. `--expect-revocation-within` keeps the verified session open and
requires an in-place revoke to close it.

`--mode hold` keeps the probe container alive for bounded `kubectl exec` checks.
It performs no network request itself.

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
