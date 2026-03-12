# Hakopod contributor instructions

Build complete, tested vertical slices. Never show invented cluster state, logs, or metrics. Go owns orchestration and authorization; dashboard and CLI share `/api/v1`. Keep the API and reconciler in one process by default, backed by PostgreSQL durable operations. Bound concurrency, list sizes, stream buffers, caches, and timeouts. Do not introduce extra infrastructure without a measured reason.

Use strict versioned TOML, immutable revisions, optimistic concurrency, digest-pinned images, scoped bearer keys, and owned Kubernetes resources. Never log credentials or secret bodies. No application may receive cluster credentials. Preserve other contributors' work. Record implemented versus verified behavior accurately.

Run `go test ./...`, the dashboard build, and real-cluster acceptance tests for Kubernetes behavior. Development fixtures must be explicitly marked; the dashboard uses real API data. All development infrastructure must use the named Hakopod development cluster/context; never mutate an existing operator cluster.
