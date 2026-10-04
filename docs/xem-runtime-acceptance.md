# Xem runtime acceptance

`TestLiveXemTemplate` exercises the actual Xem template and upstream images
against PostgreSQL, Redis and private MinIO storage. It requires the named
`k3d-hakopod-dev` development context and an explicitly reserved, disposable
worker. It never selects a production cluster or sends email.

## Recorded run status

The October 4, 2026 final canonical matrix was interrupted by development
infrastructure disk pressure. Kubernetes evicted PostgreSQL and Redis before
the backend or MinIO checks started; the API then timed out. The test ended
after 353.92 seconds. This run does not establish the five runtime combinations,
bundled MinIO downloads, final-image restart persistence or TLS acceptance.
Further local runs were stopped at the user's request. Earlier browser evidence
covers login, same-origin API access and uploads with the preceding backend and
an external S3 fixture; it does not verify the final bundled MinIO route.

Guarded cleanup removed the interrupted run's namespace, eight scoped secrets,
two persistent claims and volumes, the disposable worker and its node record,
the local registry, and all five associated anonymous Docker volumes. No
node-password secret or pod remained for that worker. The shared development
cluster image volume was preserved. The local registry pins are no longer
available. Future runs use the public upstream image digests recorded in the
catalog; the old local compatibility builds are no longer needed.

The sections below describe the suite's intended coverage and an opt-in future
procedure, not additional completed verification.

When the catalog entry is deployable, the suite obtains its production services
and the selected connection modes from `spec.PlanTemplate`, then adds the explicit
fixtures. Before publication it prefers `hakopod.toml` when present and otherwise
uses the disabled `candidate.toml`; the candidate path is reported in test output.

The suite creates a separate namespace for each of the four PostgreSQL/Redis
bundled/external combinations with bundled MinIO, then runs an additional case
with all three dependencies external. Planner/API regressions cover all eight
configuration combinations; the runtime suite covers five. External services
are real dependencies in the same disposable namespace; this verifies connection
configuration without claiming arbitrary provider-network access.

Bundled MinIO uses generated application-scoped access and password secrets and
starts without a bucket. The backend must create `xem-files` using
its explicit opt-in, then uploads use the private MinIO address while signed
reads pass through Xem's public origin. The test uses
`https://xem.example.test:8443` to exercise preservation of the explicit port in
SigV4's Host header. The public object route must reject anonymous reads and
write methods, strip application credentials, and disable caching. No separate
initializer job or MinIO client image is required.

The external S3 case uses an explicitly provisioned private HTTP fixture.
Production external storage requires a browser-reachable HTTPS endpoint.

## Reserve a development worker

Use a unique run identifier, for example `xem-acceptance-<Unix timestamp>`. Record
the run identifier, exact worker container ID and Kubernetes node UID before
running the suite. Do not reuse an existing worker. Verify the
kubeconfig's current context is `k3d-hakopod-dev` before every operation.

1. Verify that the public backend and portable frontend digests in
   `templates/blueprints/xem/hakopod.toml` are available for the worker's
   architecture. Use the upstream release images; no image build, Docker login
   or local registry is required.
2. Create one agent with `k3d node create <run> --cluster hakopod-dev --role agent
   --memory 3072m`. Label its container `com.hakopod.acceptance=xem` and
   `com.hakopod.acceptance-id=<run>`. Give the node
   `--k3s-node-label hakopod.io/xem-acceptance=<run>`,
   `--k3s-arg --node-taint=hakopod.io/xem-staging=<run>:NoSchedule`, and
   `--k3s-arg --kubelet-arg=max-pods=30`. Use `--wait --timeout 180s`.
   The staging taint prevents workloads from using the new node before its
   final pool is assigned. k3d may inherit another pool taint from an existing
   agent; adding a second `hakopod.com/pool` taint at creation can prevent
   kubelet from starting.
3. Verify the worker container ownership label and node
   `hakopod.io/xem-acceptance` label. On that node only, atomically set
   `metadata.labels.hakopod.com/pool=<run>` and replace its staging/inherited
   taints with `hakopod.com/pool=<run>:NoSchedule`. Record its UID. The test
   requires the exact node name, UID and pool and supplies a matching trusted
   workload policy. It checks available capacity before deploying.

The default six-service template requests 2,154 MiB, including 615 MiB for
MinIO. The suite retains those application resource requests and limits;
it does not shrink them to force the candidate onto a smaller node. Check total
VM headroom before running concurrent builds and acceptance.

## Run the suite

From the repository root, supply the recorded values:

```sh
GOMAXPROCS=2 GOMEMLIMIT=512MiB \
HAKOPOD_XEM_TEST=1 \
HAKOPOD_TEST_KUBECONFIG=/absolute/path/to/development/kubeconfig \
HAKOPOD_XEM_TEST_NODE='<owned node name>' \
HAKOPOD_XEM_TEST_NODE_UID='<observed node UID>' \
HAKOPOD_XEM_TEST_POOL='<run identifier>' \
go test ./internal/cluster -run '^TestLiveXemTemplate$' -count=1 -timeout=42m -v
```

The suite checks real administrator login, the NextAuth credential/session
flow and Secure/HttpOnly cookies, host and anonymous-account rejection,
application uploads, private-object denial, and application-signed downloads
whose bytes match the uploaded file. Bundled downloads use the public object
proxy with the original signed Host, path and query intact; direct S3 reads also
verify the stored bytes. It then restarts all services and checks
refresh tokens, account identity, files, Redis data and unchanged PVCs. The
external Redis case deliberately selects database 2.

The external PostgreSQL/Redis case with bundled storage uses temporary
certificate fixtures and the
actual backend image to check trusted PostgreSQL and Redis TLS, PostgreSQL
`verify-full` refusal of plaintext, and independent refusal of untrusted
certificates and wrong hostnames. Redis can otherwise fall back to in-memory
rate limiting when its connection fails; the TLS checks inspect the actual
connection outcome rather than trusting `/health`.

For an independent browser reviewer, optionally set
`HAKOPOD_XEM_BROWSER_EVIDENCE_FILE` to a new absolute path in a temporary
directory. The first combination writes an exclusive mode-0600 handoff file
and waits up to five minutes for `<path>.done`. The file contains only the
owned fixture connection details and generated development credentials. Do
not print or commit it. The test removes it before teardown. A reviewer can
use an isolated browser process with a local TLS proxy and per-process host
mapping for the configured `https://xem.example.test:8443` origin, forwarding the exact host and port to the local
port-forward. Do not change system DNS or the host trust store. This browser
fixture does not verify public ingress issuance or external DNS.

## Cleanup

Each subtest verifies its namespace UID and ownership labels, removes its
scoped test secrets and namespace, and waits for its PVCs and dynamic volumes
to be reclaimed. Failures produce bounded, filtered fixture diagnostics.

After the test exits:

1. Check there are no namespaces carrying `hakopod.io/xem-acceptance`. If a
   failed teardown retained resources, verify their recorded UID/labels and
   finish their own namespace/PV cleanup before removing the worker. Never
   delete unrelated namespaces, volumes, storage classes or nodes.
2. Recheck the exact worker container ID, run label and node UID against the
   recorded values. Delete only that node and its k3d worker. Verify the node
   is gone and check for an orphaned node-password secret belonging to that
   exact newly created node if startup failed before registration.
3. Remove temporary state and browser handoff files once
   they are no longer needed. Record whether cleanup completed.

A successful local run is development verification. It does not establish
AMD64 execution, public ingress TLS, external-provider connectivity, email
delivery, provider bucket policy/CORS or backup restoration.
