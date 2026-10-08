# Oracle receive limits, diagnostics and NULL decoding

This directory contains the production sources of github.com/sijms/go-ora/v3
v3.0.1, with its upstream LICENSE. Upstream module checksum:
`h1:BU9j2MEji/hqprUFvY8DBbnfmVrt5TpVf4ngy9tqpWY=`.
The source came from the verified Go module cache. Upstream database-dependent
tests and documentation were excluded; generated character conversion tables
remain necessary production code.

The Hakopod patch adds `network.Session.SetReadLimit(int)` and
`network.ErrReadLimit`. Zero preserves upstream behavior. The limit also caps cumulative wire bytes across the session after SetReadLimit,
counting each packet header and body once in readAll. Decode helpers do not
consume that budget a second time. SetReadLimit resets the budget. The query
adapter uses QueryMaxBytes (1 MiB), including protocol metadata overhead.
The limit checks
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

The diagnostics patch removes unconditional output from the connection and
statement code. Server warning text, query identifiers, protocol bytes and
reconnection errors must not enter standard output or standard error.
The driver still consumes those protocol fields and returns errors normally.
Explicit driver tracing retains its upstream behavior. Hakopod does not enable
driver tracing for managed queries or accept tracing options from query callers.

The NULL decoder patch backports upstream commit
`567a5004ee489c33e01f5569420909a2324cad37`, which resolves
[issue 730](https://github.com/sijms/go-ora/issues/730). An untyped NULL uses
Oracle type 0 and has no registered decoder. The patch consumes its CLR field
before processing later columns. It also rejects a nonempty value for this
NULL-only type. Tests call the actual decoder and check subsequent field
alignment, stale value removal and receive-limit enforcement.

The scalar fetched-character candidate handles CHAR/NCHAR columns described
with zero maximum length as NULL without consuming row bytes. It clears prior
values before returning and applies only outside UDT and array paths. The
upstream BasicRead shortcut is restored; positive-length parameter, fixed CLR
and array paths retain their existing reads. Tests distinguish described NULL
columns with no CLR from positive-length character fields carrying a NULL CLR.
This candidate is not a proven decoder fix. Full native acceptance is required
before any Oracle capability is enabled.

Native diagnostic helpers require the `hakopod_native_acceptance` build tag.
They collect only bounded, static categories on an explicitly captured statement.
They do not collect SQL, values, names, raw errors or protocol bytes.
Default-build protocol hooks are empty. Preserve the diagnostic helpers and
privacy tests when updating the driver.

When updating the driver, review and reapply `receive-limit.patch`,
`diagnostics.patch`, `null-decoder.patch` and `scalar-character-fetch.patch`
where upstream still requires them.
Inspect all allocation and accumulation paths. Run these adversarial tests and
real Oracle query acceptance. CI tests the nested module explicitly:

```
cd third_party/go-ora-v3
go test . ./network -count=1
go test -tags hakopod_native_acceptance . ./network -count=1
```

The tests need no database. Root-module `go test ./...` excludes this nested
module, so the explicit CI step must remain.
