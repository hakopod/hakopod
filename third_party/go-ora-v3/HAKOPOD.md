# Oracle receive limits

This directory contains the production sources of github.com/sijms/go-ora/v3
v3.0.1, with its upstream LICENSE. Upstream module checksum:
`h1:BU9j2MEji/hqprUFvY8DBbnfmVrt5TpVf4ngy9tqpWY=`.
The source came from the verified Go module cache. Upstream database-dependent
tests and documentation were excluded; generated character conversion tables
remain necessary production code.

The Hakopod patch adds `network.Session.SetReadLimit(int)` and
`network.ErrReadLimit`. Zero preserves upstream behavior. The limit checks
packet allocations, read allocations, CLR chunk accumulation, session input
buffer appends and LOB buffer appends before allocating or appending. Arithmetic
checks reject negative and overflowing lengths even when no limit is configured.

Oracle query connections set a finite limit after authenticated connection
setup and before executing a statement. Management connections retain the
upstream default. A rejected query connection is discarded. TLS verification
and listener endpoint restrictions are unchanged. The result JSON limit remains
a separate bound and may be lower than the driver receive ceiling.

This patch is necessary because the pinned upstream decoder allocates advertised
packet/chunk lengths and collects LOB values before database/sql returns a row.
Checking the result after Scan cannot protect those allocations. No public
upstream receive-limit option was available in this version.

When updating the driver, reapply the small decoder patch, inspect all allocation
and accumulation paths, and run these adversarial tests plus real Oracle query
acceptance. The nested module is tested explicitly in CI:

```
cd third_party/go-ora-v3
go test . ./network -run '^Test(ReadLimit|LobReadLimit)' -count=1
```

The tests need no database. Root-module `go test ./...` excludes this nested
module, so the explicit CI step must remain.
