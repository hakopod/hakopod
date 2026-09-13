# Hakopod contributor instructions

Build complete, tested vertical slices. Never show invented cluster state, logs, or metrics. Go owns orchestration and authorization; dashboard and CLI share `/api/v1`. Keep the API and reconciler in one process by default, backed by PostgreSQL durable operations. Bound concurrency, list sizes, stream buffers, caches, and timeouts. Do not introduce extra infrastructure without a measured reason.

Use strict versioned TOML, immutable revisions, optimistic concurrency, digest-pinned images, scoped bearer keys, and owned Kubernetes resources. Never log credentials or secret bodies. No application may receive cluster credentials. Preserve other contributors' work. Record implemented versus verified behavior accurately.

Run `go test ./...`, the dashboard build, and real-cluster acceptance tests for Kubernetes behavior. Development fixtures must be explicitly marked; the dashboard uses real API data. All development infrastructure must use the named Hakopod development cluster/context; never mutate an existing operator cluster.

Use plain human English in product copy, documentation and comments. Do not use emojis. Forms with more than four inputs belong on dedicated nested pages with concise help, clear navigation and a review step for consequential actions. Preserve entered values when a request fails.

Keep the dashboard simple and compact. Use one desktop header row, narrow scope selectors, short page-heading rows and closely grouped controls. Avoid repeated context, decorative hero spacing and oversized cards. Keep text readable and touch targets usable; move secondary details into inspection panels or concise help.
