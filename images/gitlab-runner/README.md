# Managed GitLab native transport

This candidate builds both the manager and helper from GitLab Runner 19.4.1,
commit `3c39fcebf73d01d464db3dee8a5267155273a6c5`. `source.lock.json`
records the upstream tag identity; `LICENSE.upstream` preserves its MIT notice.
`transport.patch`, `cache.patch` and the Go source templates are the complete downstream delta.
The `.go.in` suffix keeps upstream package sources outside Hakopod's Go module.

No candidate image has been publicly published or enabled in the provider
scheduler. The AMD64 manager and helper were packaged in a temporary development
registry and verified against the qualified executable checksums and embedded
source manifest. Native jobs, ARM64 and load acceptance remain separate gates.

The patched AMD64 binaries passed 25 verification redirect cases and 10 positive
controls, plus 102 native helper upload/download cases, on 29 September 2026.
These used synthetic credentials in a loopback-only namespace with temporary
trusted TLS certificates. The matrix covered denied coordinator redirects,
missing policy, approved HTTPS storage, HTTP downgrade rejection, and removal of
headers, cookies, bodies and client certificates from storage requests. Full
tokens and signed queries were absent from captured logs. The small direct
download exercised range probing and fallback; large parallel transfers, native
cache clients and real provider jobs were not established by this matrix. The
subsequent cache transport changes below require a new build and qualification;
the earlier image pair must not be described as validating them.

The upstream AMD64 verification client was tested with invented credentials in
an isolated loopback namespace. It forwarded `RUNNER-TOKEN` across all 25 tested
redirects; 307/308 also replayed the credential-bearing JSON. This patch gives
coordinator calls a nonredirecting client and rejects calls escaping their API
prefix. Native `verify` returns a failure for transport/coordinator errors before
any optional `--delete` operation can save changes. Inherited HTTP proxy settings
are disabled for these coordinator and artifact clients.

Artifact uploads remain ordinary authenticated coordinator POSTs. A coordinator
redirect, including the upstream helper's Geo-style relocation/retry path, fails
with a canonical-URL error. It is not treated as permission to replay a job token
or upload body to another server. Operators must configure the canonical writable
coordinator endpoint for this candidate.

Artifact downloads retain ordinary and parallel storage paths. Their explicit
redirect handling permits at most five storage GET requests, validates the exact
HTTPS origin at every hop, and creates each request without coordinator headers,
cookies, body, Referer or client certificate. Range headers are retained. A signed
storage query is used for the transfer but is never written to redirect logs.
Debug response logging omits response headers and bodies.

## Optional S3 cache candidate

The `hakopod-s3` cache adapter signs requests with manager-only credentials and
never exports GoCloud or IAM credentials to a helper. Uploads use S3 SigV4 POST
policies binding the exact bucket, object key, expiration (at most 15 minutes),
and `content-length-range`. A normal presigned PUT does not enforce an archive
size bound; it is deliberately not used. This limit is per archive. Bucket
retention and a total storage budget remain administrator responsibilities.

The installation chooses an HTTPS path-style endpoint, explicit region, bucket,
CA and 1 MiB to 1 GiB archive limit. Its application opts in with an application
secret containing `access_key`, `secret_key`, and optional `session_token` JSON.
The stable cache namespace includes application, environment, provider instance,
provider target, service and architecture. GitLab adds the job's project ID and
cache key, allowing fresh runners in the same pool to restore earlier archives.
Use a bucket credential restricted to that namespace; do not reuse an account's
administrative storage key.

The manager and helper share a nonsecret, read-only `cache-policy.json`. The
helper accepts only that policy's endpoint, bucket and object namespace; it
refuses all redirects, proxy settings, unrelated credential headers and GoCloud
transfers. Downloads require a declared bounded size and stop at the archive
limit. Cache transfer concurrency is one, the buffer is 1 MiB, and timeout is at
most five minutes. A separate `cache-ca.crt` supplies storage CA trust.

These are implementation properties pending VM and real-provider acceptance.
The image-pair report must include `cache_protocol_version: 1`, passing cache
coverage with at least 16 cases, and `report_hashes.cache`. An old report cannot
enable storage. Real acceptance must reject oversized direct POSTs, altered
keys/policies, cross-pool access and redirects, then demonstrate save and restore
on distinct native runners. Successful artifact transfer is not cache evidence.

The immutable nonsecret policy is mounted at
`/run/hakopod-provider/transport-policy.json`:

```json
{
  "schema_version": 1,
  "coordinator_url": "https://gitlab.example.com/team/gitlab",
  "artifact_origins": ["https://approved-storage.example.com"]
}
```

Policy input is bounded to 32 KiB, one strict JSON object, and at most 16 distinct
canonical HTTPS origins. It must match the configured coordinator base and path
prefix. A missing file allows no artifact redirect. Invalid policy fails closed.
All three case-sensitive fields are required; duplicate or unknown fields and a
null origin list are rejected. Use an empty array when storage redirects are not
approved.
Workflow environment variables do not select or override the policy path.
The installation's approved trust and egress policy owns which origins may be
written here; this file does not independently authorize private network access.
Native `tls-ca-file` retains certificate and hostname verification. The manager
client certificate is explicitly removed from storage requests.

Both binaries need the policy. The orchestrator must mount this nonsecret file
read-only into the manager and private Docker daemon, and configure native
`runners.docker.volumes` to bind its daemon-side path read-only at the same helper
path. Upstream Docker executor `Binds` are shared by helper, job and service
containers, so this needs no new native configuration field. The policy contains
no runner/admin secret; those remain on the separate manager-only volume. The
official unpatched helper image must not be paired with the patched manager.

Run `build-native.sh` on a bounded Linux VM or CI worker with Go 1.26.8. It requires
a fresh owned source/cache directory, verifies the exact upstream commit, applies
the patch, runs focused transport tests and creates both native binaries. Enforce
CPU, memory, disk and deadline limits outside this script. The development proof
uses transient systemd limits and preserves existing workload disk floors.
The successful AMD64 build stayed within 0.5 CPU, 3 GiB RAM and 3.5 GiB owned
scratch; its measured peaks were 2,330,341,376 bytes RAM and 3,489,247,232 bytes
scratch. These are build measurements, not runner capacity claims.

After native protocol qualification, build the `manager` and matching
`helper-amd64` or `helper-arm64` Dockerfile targets on their native architectures.
The Dockerfile preserves pinned upstream base images and replaces both native
executables. Image publication must verify that extracted binaries match the
qualified checksums, then pin the new manager and helper image digests together.
This directory does not enable an automatic image push.

`source-manifest.py` writes canonical JSON binding the upstream revision, patch,
overlays, build script, manifest generator, Dockerfile, license and exact Go
version/executable checksum. SHA256 of those exact JSON bytes is the transport
source identity. Both images include it at
`/usr/share/hakopod/transport-source.json`; qualification must verify that file
and both executable checksums after extracting the final image layers.

## Installation execution qualification

The server accepts native configuration syntax independently from execution.
An active pool still needs an exact installation-owned project, environment,
application, service, provider target, manager image and architecture binding.
The public capability response lists only those approved bindings whose image
report establishes real native execution. There is no global native enable flag.

An image report's `coverage.execution` records `passed`, `architecture`, exact
`coordinators`, and `runner_scopes` containing only the proven `project` or `group`
modes. It also requires successful checkout, script execution, artifacts,
services, job isolation, credential isolation, drain and cleanup. The same
report's `report_hashes.execution` binds the actual acceptance report bytes.
These fields belong to the report already bound to both platform image digests,
both executable checksums and the source manifest. Never populate them from unit
tests or a report for a previous image pair.

Custom-CA and private-coordinator bindings additionally require their explicit
execution coverage. Cross-architecture builds are advertised only on bindings
with that proof. Cache requires `cache_server_address` matching the configured
storage endpoint and separate save/restore, size-limit and scope-isolation
execution evidence, in addition to the cache protocol qualification above.

Planning, durable deployment validation and native slot startup reject missing
or mismatched proof. Suspension remains possible
when an installation binding is removed; old encrypted slot intent retains the
original trust needed for cleanup and historical reads.
