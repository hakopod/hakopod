# Installation operations independent review

Review date: 2026-09-14. Source review completed; rendered UI approval is pending.
The reviewer did not implement these product changes or operate a browser.
The user explicitly asked to continue code work and review the UI later.

## Scope

| Area | Source inspected | Rendered coverage |
| --- | --- | --- |
| Infrastructure | API logs, Setup, Updates, owner guards, direct tab links, update notice in the shared header | Pending |
| Deployment setup | Import instructions, shared prerequisite error link, application pending-domain notice | Pending |
| Custom domains | Desired TOML, verification records, activation/removal review, discard controls, ingress and certificate host filtering | Pending |
| Installation API | Human owner/browser requirements, self-hosted mode, fixed maintenance operations, audit record, bounded responses | HTTP fixture tests only |
| Maintenance helper | Unix peer checks, fixed repository and unit, version/manifests, locking, backups, release switch, health and failure handling | Local fixtures only |
| Storage | Default/explicit class checks before deployment, existing bound claims, setup state | Live results supplied by the implementing agent |

The source follows the shared page container and existing tab patterns. New
layout uses Tailwind utilities and shared Button, Dialog, Input, Tooltip, Note
and ErrorState components. Primary actions use the configured accent. The upgrade
confirmation explains the management-service restart, retained backups and manual
recovery after migrations. Errors retain the selected version and confirmation.
No source inspection establishes the final spacing, contrast or keyboard behavior.

## Domain and access safeguards

Custom domains remain in immutable desired configuration and signed source
reviews. Unverified domains do not acquire routing reservations. Production
cluster construction supplies the database approval callback; absent authority
or a failed lookup cannot enable custom routing. Ingress rules, TLS hosts,
uploaded-certificate checks and automatic backend-certificate coverage use the
approved set. Protected-domain checks, per-host reservation locks and historical
ownership remain in place.

Verification alone does not apply a release. A subsequent accepted release can
activate every freshly verified mapping in its configuration; the review states
that scope. Obsolete automatically staged records are cleaned without releasing
historical ownership. Pending configuration does not bypass certificate mounts,
SMTP readiness, public TCP provisioning or AWS identity prerequisites. Xem's
complete production configuration still requires those separate setup steps.

Installation operations require an unscoped human owner browser credential in
self-hosted mode. Ordinary administrators, service/CLI credentials and Cloud
customers cannot call them. The maintenance API cannot select a unit, command,
repository or URL. The root helper admits only root and the API service UID on a
restricted Unix socket. API logs are bounded to 200 entries/512 KiB and a five
second journal read. Credential-related entries are omitted as defense in depth;
this is not a guarantee that arbitrary secret text can be recognized.

## Findings addressed during review

- Pending domains previously would have been shown as active from desired TOML.
  Domain display and routing now consult reservations, and missing authority
  fails closed.
- Setup no longer presents loading or failed observations as absent configuration.
  Paused logs suppress automatic focus/reconnect and interval requests.
- Removed historical domains no longer create a permanent setup banner. Activation
  review explains the routing effect even when TOML itself has no changes.
- Accepted upgrade preflight failures, including installer SystemExit failures,
  become terminal status. Partial stop and backup failure attempt recovery before
  any release switch; failures after switching do not automatically run an older
  server against potentially migrated data.
- Installed version is read from the current symlink, independent of cached
  release metadata. Release checks examine up to three candidate manifests in
  the most recent 30 releases, so one unsupported candidate need not hide the
  next supported candidate.
- Compatibility checks include the declared source version and runtime pins.
  A newer bootstrap also checks installed K3s and retained Helm/Node artifacts,
  preventing target metadata alone from approving changed runtime dependencies.
- HTTPS health checks use the local configured node address, the original Host
  and SNI, and installed certificate trust. SSH mode uses loopback.
- First supported upgrades from alpha.4 can install the maintenance service.
  Running privileged helpers are not silently replaced by API binary upgrades.
- The existing archive extractor already normalizes directory modes after
  extraction. The initial permission concern was withdrawn after inspecting that
  final pass, and an explicit restrictive-umask regression now covers it.

## Verification evidence

The reviewer independently reran the 14 maintenance tests successfully. They cover
mocked upgrade lifecycle, partial stop, backup/health failures, in-process and file-lock contention,
manifest/runtime checks, SystemExit handling, sensitive log omission, extraction
permissions and multi-document kubectl parsing. This is not a real host upgrade.

The reviewer inspected the implementing agent's result logs:

| Check | Result | Local evidence |
| --- | --- | --- |
| Full Go suite with real PostgreSQL | Passed | /tmp/hakopod-operations-full-go.log |
| GitHub/GitLab import and domain proof follow-up | Passed; pending TOML preserved | /tmp/hakopod-source-domains-tests.log |
| Live custom-domain HTTP/TLS test in the named development cluster | Passed in 16.46 seconds; pending exclusion, approval, removal and reapply | /tmp/hakopod-live-pending-domains.log |
| Dashboard tests | 82 passed: 28 server and 54 UI | /tmp/hakopod-operations-dashboard-tests.log |
| Installer discovery | 80 passed, including the final file-lock regression | /tmp/hakopod-all-installer-tests.log |

Dashboard typecheck, build and formatting passed according to the implementing
agent. The Ubuntu VM's Redis claim reached Bound with its pod 1/1 after storage
provisioning; cert-manager's three controllers reached 1/1 and a temporary
self-signed Certificate reached Ready. These VM results were supplied by the
implementing agent, not independently repeated by this reviewer. The temporary
certificate fixture was removed. They do not establish public ACME issuance or
an SMTP production cutover.

The implementing agent also exercised maintenance helper functions on the VM:
installed runtime pins, the current dashboard health endpoint, a bounded 35-entry
journal read, and a PostgreSQL custom-format backup whose contents were listed
with pg_restore. The optional storage module reran idempotently. These checks did
not install the privileged maintenance service, restart the API, switch versions
or restore a database. They supplement the fixtures without claiming upgrade
acceptance.

## Remaining gates and limits

No real source-to-target upgrade, database restore rehearsal or browser reconnect
through an upgrade was performed. Release compatibility defaults to an empty
list; populate it only for versions that pass an installed-host upgrade test.
The public installer and current Ubuntu dashboard do not receive unpublished
source changes automatically. No publication or release is approved here.

The updater is limited to newer compatible releases within one major/minor series.
It does not upgrade K3s, the PostgreSQL server, OS packages or an existing
maintenance helper. Metadata lookup is intentionally bounded and is not an
exhaustive history search. SHA256SUMS over HTTPS is release consistency checking,
not an independent release signature. Backups are retained on the same host and
must be copied and restore-tested separately. A failure after migrations needs
administrator recovery.

Final source follow-ups were fixed and re-inspected:

- The dashboard dispatcher acquires the cross-process installer lock before
  writing queued status, then transfers that lock to the worker. CLI upgrades
  acquire the same lock before state changes. A contending operation cannot
  overwrite the running operation's status; the added regression passes.
- Reusing a historical domain reservation restores its service and proof display
  metadata under the existing count cap. The real-PostgreSQL domain test covers
  activation, removal, discarded setup and re-addition, and passes.

No unresolved actionable source finding remains in this scoped review. The
complete visual checklist below remains unchecked because the user deferred
browser review.
Passing tests does not close it. Render the affected routes in both themes on
desktop and mobile, inspect actual bounds/screenshots, and exercise keyboard and
touch before UI publication.

## Required visual checklist

### Layout and hierarchy

- [ ] The shared page container supplies 24px horizontal padding at 640px and above, 16px below. Nested pages fill its content box without duplicate insets, centered margins or width caps. Headings, summaries and lists align. Settings fill the area beside their navigation; embedded auth does not double its parent's inset.
- [ ] Page-heading and page-level tab-row bottom dividers reach both viewport edges. Full-width row backgrounds and borders do not shift their labels outside the content inset or cause document overflow. Internal card/inspector dividers stay within their component.
- [ ] Shared layout/spacing uses Tailwind utilities in JSX or `@apply` within semantic CSS classes. Responsive rules use the shared breakpoint; raw CSS remains for component-specific behavior. There are no competing page padding overrides.
- [ ] Page and form-section headings have no decorative icon or visible description. One title carries the context; necessary actions remain aligned. Status and data needed for decisions stay visible; explanatory prose moves to help.
- [ ] Help is short, named, keyboard reachable and available on touch. Focus/hover or activation reveals it; Escape dismisses it; it stays inside the viewport. It is not the sole home for essential warnings or field instructions.
- [ ] Pages have one h1, sensible subordinate heading order and no duplicate headings or breadcrumbs. Nested back navigation uses the global header.
- [ ] Projects is the default home regardless of saved scope. Application lists are URL-scoped to a valid project and environment; invalid or unavailable scopes never fall back silently or display another project’s cached data.
- [ ] Page-heading vertical padding is balanced above and below at every responsive layout.
- [ ] Active main navigation, tabs and Settings/preferences sections use the shared theme-aware red for text and icons, with no selected underline, border or background fill. Hover/focus preserve the selected color and visible keyboard focus; ordinary row dividers remain visible.
- [ ] The active section remains visible inside scrollable navigation after deep links, selection changes, font loading and resizing, without scrolling the document.
- [ ] Desktop navigation is one compact row. Mobile reflow and long names do not cause document overflow or clipped actions.
- [ ] Summaries are inline and compact. Catalog lists have no outer panel border. Cards keep restrained surfaces, readable spacing and meaningful hover/focus states.
- [ ] Application/service grids use at most four normal desktop columns and five wide columns. Sparse grids retain card widths.
- [ ] Action links use shared Button with asChild. Sibling actions have equal height and vertical alignment; labels remain readable and targets usable.
- [ ] Application endpoint text and external-link icon stay on one line; long labels truncate without hiding the destination from assistive technology. Alarm links have visible separation from the preceding content.

### Components and interaction

- [ ] Consume Hatch through public exports. Use shared tokens for color, spacing, state and typography; check dark and Paper themes.
- [ ] All selects use SelectField, including disabled/empty options and accessible labels. Do not add native selects or a competing wrapper.
- [ ] Keyboard focus is visible. Cards preserve native links and independent menu/copy actions. Disabled/loading controls cannot double-submit.
- [ ] Forms with more than four inputs use nested pages. Labels, contextual instructions and validation stay associated with inputs. Textareas can expand where useful.
- [ ] Consequential actions show a review/confirmation, and failed requests preserve entered values. Permission failures are clear.
- [ ] Before screenshots, wait for the route’s expected body or control, lazy imports, query completion and fonts. Reject unexpected loading placeholders, rendered error boundaries and fixture schema errors. A page title or clean console alone is not evidence that the page loaded.
- [ ] Empty, loading, denied, failure and stale-data states remain usable without artificial success. Long IDs, URLs, values and translated-length text wrap or scroll within their component.

### Data, safety and cost

- [ ] Display actual backend observations; separate desired, observed, pending and stale state. Never fabricate metrics, logs or deployment success.
- [ ] Secrets are write-only, roles are enforced by Go, and mutation controls respect access. Review shared-secret impact before replacement.
- [ ] Polling, streaming, caches and buffers are bounded and inactive work stops. Do not add dependencies or runtime services for cosmetic changes.
- [ ] Product copy is concise plain English with no emojis. Avoid repeated context and implementation jargon in routine flows.

