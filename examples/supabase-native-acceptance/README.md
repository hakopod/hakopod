# Supabase native lifecycle acceptance

This is a destructive development-only qualification fixture. It refuses every
Kubernetes context except `k3d-hakopod-dev`, requires digest-pinned expected
images, and requires `HAKOPOD_ACCEPTANCE_DISPOSABLE=1` as an explicit assertion
that the API, JWT and service-role credentials belong only to this fixture.

After the source capability gate is qualified through review, it creates a disposable
managed platform through the real review, accept, PostgreSQL worker and Kubernetes
paths, then exercises its Envoy endpoint: anonymous signup, PostgREST CRUD, a Realtime WebSocket join,
Storage bucket/object CRUD, Edge Functions, and Studio. It then restarts all
workloads, repeats the data checks, performs a no-change revision update, requests deletion through `/api/v1`, and
waits for the exact namespace UID to disappear. Run `TestSupabaseLiveRefusesUnclaimedNamespace` in the same VM acceptance job;
it creates an exact-label namespace without a PostgreSQL UID claim and verifies
the reconciler refuses it without creating child resources.

On failure the harness stops its local port-forward but deliberately retains the
disposable platform, operation rows and namespace for diagnosis. The operator
must delete that exact platform through the API before reusing the fixture; do
not remove its namespace directly because that would bypass UID-claim cleanup.

The harness does not turn on any capability gate. The VM job may apply the
separately delivered acceptance-only gate overlay to its disposable source
copy; that overlay must never be committed, packaged or deployed. Store the JSON report as
acceptance evidence only when every command succeeds on the named development
cluster using the exact reviewed image digests.

## TLS and runtime evidence required by this candidate

The fixture specification must include `gateway-tls-certificate`, resolved to an
immutable Secret snapshot with `tls.crt`, `tls.key`, and `ca.crt`. The leaf must
chain to that CA, be currently valid for server authentication, and cover the
exact hostname in `supabase.public_url`. The separately resolved
`envoy-runtime-config` value must define only the TLS listener on port 8443 and
load `/etc/envoy/tls/tls.crt` and `/etc/envoy/tls/tls.key`; the reconciler rejects
a legacy port 8000 listener before creating the snapshot.

Set `HAKOPOD_ACCEPTANCE_GATEWAY_CA` to the reviewed CA file and
`HAKOPOD_ACCEPTANCE_IDENTITIES` to an object with exactly the eleven component
names, each containing its digest-qualified positive numeric `uid` and `gid`.
The harness refuses to run with incomplete identities. It compares the reviewed
CA digest with the immutable snapshot without writing secret bodies to disk,
verifies the served certificate and hostname, requires plaintext refusal, and
uses HTTPS/WSS for all product checks. The reconciler performs structural LDS
validation before applying its immutable snapshot.

Before traffic, the harness checks the live workload templates and processes
for the exact qualified UID/GID, non-root execution, read-only root filesystem,
no privilege escalation, all Linux capabilities dropped, RuntimeDefault
seccomp, and disabled service-account token mounting. It records the five PVC
UID/PV bindings and storage properties, replaces every pod, requires those
records to remain byte-for-byte equal, and reads both PostgreSQL and Storage
data after replacement.
