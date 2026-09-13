# Hakopod contributor instructions

Build complete, tested vertical slices. Never show invented cluster state, logs, or metrics. Go owns orchestration and authorization; dashboard and CLI share `/api/v1`. Keep the API and reconciler in one process by default, backed by PostgreSQL durable operations. Bound concurrency, list sizes, stream buffers, caches, and timeouts. Do not introduce extra infrastructure without a measured reason.

Use strict versioned TOML, immutable revisions, optimistic concurrency, digest-pinned images, scoped bearer keys, and owned Kubernetes resources. Never log credentials or secret bodies. No application may receive cluster credentials. Preserve other contributors' work. Record implemented versus verified behavior accurately.

Run `go test ./...`, the dashboard build, and real-cluster acceptance tests for Kubernetes behavior. Development fixtures must be explicitly marked; the dashboard uses real API data. All development infrastructure must use the named Hakopod development cluster/context; never mutate an existing operator cluster.

Use plain human English in product copy, documentation and comments. Do not use emojis. Forms with more than four inputs belong on dedicated nested pages with concise help, clear navigation and a review step for consequential actions. Preserve entered values when a request fails.

Keep the dashboard simple and compact. Use one desktop header row, narrow scope selectors, short page-heading rows and closely grouped controls. Avoid repeated context, decorative hero spacing and oversized cards. Keep text readable and touch targets usable; move secondary details into inspection panels or concise help.

Balance page-heading padding above and below. Active navigation and tabs use the shared theme-aware red for text and icons, with no selected underline, border or background fill; retain visible keyboard focus. This includes Settings and preferences navigation. Primary actions use the configured accent, reserving destructive button styling for destructive actions. Use a named trash icon for deletion.

The default dashboard view lists all accessible projects. Project application lists carry their project and environment in the URL. Missing or inaccessible scopes must never fall back to another project. Resource detail navigation uses the loaded resource's scope, not a stale browser preference. Reuse shared queries rather than fetching each project's applications to decorate the overview.

Treat user design feedback as a global rule. Inspect every route and shared layout, not only the annotated page. The shared page container owns a 24px horizontal inset, reduced to 16px below 640px; headings, summaries and content align to it. Page-heading and page-level tab-row dividers extend to the viewport edges while their contents retain that inset. Nested page wrappers fill the remaining width without adding another inset, centered margins or width caps. This replaces the earlier zero-gutter rule. Keep necessary spacing inside cards, controls, dialogs and between layout columns. Page and section headers contain a title, necessary actions and compact help; do not add decorative heading icons or visible descriptive paragraphs. Put descriptions in accessible help that works with hover, keyboard and touch. Keep errors, warnings, status facts and field instructions visible where they are needed.

Use Tailwind utilities for layout and spacing, directly in JSX or through `@apply` in semantic CSS classes. Use `@variant` for shared responsive rules. Keep raw CSS for component-specific behavior and tokens where it is clearer; do not add duplicate per-route rules or inline padding fixes. Tailwind v4 is compiled through the existing Vite plugin.

For every UI review, delegate to an independent UI/UX reviewer using [the Hatch review checklist](docs/ui-ux-checklist.md). The reviewer must check affected route families, both themes, desktop and mobile, keyboard/touch interaction and real element bounds, then inspect rendered screenshots critically. Resolve findings before sign-off. Passing tests or checking document overflow alone is not visual approval; finish screenshot review before declaring source frozen or publishing. Record coverage and any unverified states accurately.

Keep catalog-style lists free of outer panel borders and use compact inline summaries. Cap application/service grids at four desktop columns and five on wider displays. Navigation actions should use the shared Button with asChild so links and buttons keep matching sizes. Make service variables and secret bindings directly discoverable on the service page.

Use the centralized `SelectField` from `web/src/components/ui/select.tsx` for dashboard selection controls. Supply `label`, `value`, `onValueChange`, and declarative `options`; preserve empty values, disabled choices, required fields, and accessible labels. Do not add native selects or a second select wrapper.
