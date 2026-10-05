# Outpost template runtime acceptance

On 2026-10-05, all 16 cases in [Outpost runtime acceptance run 37259478804](https://github.com/hakopod/hakopod/actions/runs/37259478804)
passed on the first attempt of that run. The jobs ran every bundled/external
PostgreSQL, Redis and RabbitMQ combination on native GitHub-hosted AMD64 and ARM64
runners.

The tested Hakopod commit was
[`708e0b1e75b5e69513a856322786519ddc4bc812`](https://github.com/hakopod/hakopod/commit/708e0b1e75b5e69513a856322786519ddc4bc812),
with catalog
[`d6467a12881798a379ba6a71e8951fd88ed38049`](https://github.com/hakopod/templates/commit/d6467a12881798a379ba6a71e8951fd88ed38049).
The image was Outpost v1.6.0 at
`sha256:36ed337bb1edf52b6b0130f87e9eef0b9c03187bfb85d946b4eeab5b7b602a28`.
The three general [CI jobs](https://github.com/hakopod/hakopod/actions/runs/37259176333)
also passed at that same source commit: Go tests, vet and builds; dashboard checks
and build; and SDK tests and generated-source checks.

## Matrix

The workflow's three mode bits select external PostgreSQL, Redis and RabbitMQ,
in that order. A zero selects the bundled service; a one selects a separate
external development fixture.

| PostgreSQL | Redis | RabbitMQ | Native AMD64 | Native ARM64 |
| --- | --- | --- | --- | --- |
| Bundled | Bundled | Bundled | [Passed](https://github.com/hakopod/hakopod/actions/runs/37259478804/job/111603470469) | [Passed](https://github.com/hakopod/hakopod/actions/runs/37259478804/job/111603470636) |
| Bundled | Bundled | External fixture | [Passed](https://github.com/hakopod/hakopod/actions/runs/37259478804/job/111603470585) | [Passed](https://github.com/hakopod/hakopod/actions/runs/37259478804/job/111603470559) |
| Bundled | External fixture | Bundled | [Passed](https://github.com/hakopod/hakopod/actions/runs/37259478804/job/111603470639) | [Passed](https://github.com/hakopod/hakopod/actions/runs/37259478804/job/111603470570) |
| Bundled | External fixture | External fixture | [Passed](https://github.com/hakopod/hakopod/actions/runs/37259478804/job/111603470582) | [Passed](https://github.com/hakopod/hakopod/actions/runs/37259478804/job/111603470637) |
| External fixture | Bundled | Bundled | [Passed](https://github.com/hakopod/hakopod/actions/runs/37259478804/job/111603470564) | [Passed](https://github.com/hakopod/hakopod/actions/runs/37259478804/job/111603470609) |
| External fixture | Bundled | External fixture | [Passed](https://github.com/hakopod/hakopod/actions/runs/37259478804/job/111603470574) | [Passed](https://github.com/hakopod/hakopod/actions/runs/37259478804/job/111603470718) |
| External fixture | External fixture | Bundled | [Passed](https://github.com/hakopod/hakopod/actions/runs/37259478804/job/111603470695) | [Passed](https://github.com/hakopod/hakopod/actions/runs/37259478804/job/111603471497) |
| External fixture | External fixture | External fixture | [Passed](https://github.com/hakopod/hakopod/actions/runs/37259478804/job/111603470642) | [Passed](https://github.com/hakopod/hakopod/actions/runs/37259478804/job/111603471421) |

Each job log records its native node architecture, mode, source checkout and
catalog checkout. All 16 logs were checked for both completed migration revisions,
three successful private delivery observations, the final assertion summary,
the passing test result and owned-resource cleanup. Per-job log hashes are
recorded in the catalog's
[Outpost provenance](../templates/blueprints/outpost/provenance.json).
These hashes identify the retrieved GitHub job logs; log and artifact availability
follows the repository's retention policy.

## What the test exercised

[`TestLiveOutpostTemplate`](https://github.com/hakopod/hakopod/blob/708e0b1e75b5e69513a856322786519ddc4bc812/internal/cluster/live_outpost_template_test.go)
uses the real template planner and Kubernetes deployment client. Every job creates
a disposable `k3d-hakopod-dev` cluster and an owned application namespace. It uses
1 GiB local-path volumes for the three dependencies, rather than the preset's
default 5 GiB volumes.

For each selected external mode, the test deploys a separately named real
PostgreSQL, Redis or RabbitMQ fixture in that same namespace and supplies its
connection through the external template fields. These are private plaintext
fixture connections. The network is internal, and the HTTP receiver stays in
the namespace. API requests use Kubernetes port forwarding.

Each case verifies:

- Migration jobs finish successfully on revisions 1 and 2 before the API,
  delivery and log containers start.
- Missing and invalid API keys are rejected. Tenant JWTs cannot cross tenant
  boundaries or invoke administrator-only publishing.
- A real event reaches the private HTTP receiver with the expected JSON marker.
  The API returns the persisted event and a successful delivery attempt.
- A new revision replaces the actual service and dependency pods. The existing
  tenant, JWT, destination signing secret, event and original attempt ID remain
  usable afterward, and a new event is delivered.
- All three dependency PVCs stay bound with the same identities. A Redis
  checkpoint value survives restart. Initialized API connections use Redis
  database 0 for the bundled service and database 2 for external fixtures.
- Reported workers are healthy. Pod manifests retain the expected digest pins,
  non-root security contexts, disabled service-account token automount and
  read-only root filesystems where configured. Service and ingress resources
  expose only the API through an ingress.
- Cleanup removes the owned namespace, scoped secrets, claims and provisioned
  volumes.

The workflow, pinned dependencies and invocation are preserved at the tested
commit in [`.github/workflows/outpost-runtime.yml`](https://github.com/hakopod/hakopod/blob/708e0b1e75b5e69513a856322786519ddc4bc812/.github/workflows/outpost-runtime.yml).

## Limits

The external cases validate configuration against the supplied same-namespace
fixtures. They do not qualify arbitrary managed providers, private-network
routing, external TLS policies or public HTTPS. The ingress is inspected as a
Kubernetes resource; requests do not traverse public ingress.

The receiver checks the JSON marker, without validating webhook HMAC/signature
headers. Stable destination-secret retrieval after restart verifies credential
persistence with the configured key; stored ciphertext and key rotation are not
inspected.

This run does not validate portal integration, throughput, scaling, high
availability, backup/restore correctness, failure recovery, or upgrades of an
existing Outpost installation. Retaining a PVC across a restart does not prove a
backup can be restored. It also does not constitute production Outpost
acceptance, hosted shared-Cloud qualification, or an end-to-end dashboard/CLI
deployment flow.

Anonymous image-registry inspection remains a separate provenance record. Its
manifest/configuration observations are not used as a substitute for the runtime
checks above.

