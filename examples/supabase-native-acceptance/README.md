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

The run also requires a tested backup destination and a separate empty
Supabase target. It captures the source compound recovery artifact through the
reviewed `/api/v1` recovery operation, restores that exact artifact to the
revision-fenced target, and waits for both durable operations to succeed.

After an accepted create operation, an early exit trap stops local processes and
requests deletion of the exact owned platform through the API. It retains the
protected work directory on failure for diagnosis. Cleanup verifies the scope,
namespace absence and persistent-volume absence; it never deletes the namespace
directly.

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

The pooler must resolve `API_JWT_SECRET` from the independently generated
`pooler-api-jwt-secret` snapshot. Native acceptance sends the service-role JWT
to the controlled Supavisor administrative endpoint and requires a 403, sends
the pooler administrative JWT and requires success, and then confirms the
tenant still requires peer verification for the `db` hostname. NetworkPolicy
must also continue to deny application workload access to port 4000.

This source is a candidate harness. Native execution and qualification evidence
remain pending; source review and shell checks do not qualify the Supabase gates.
