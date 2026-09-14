# Removing services, applications and projects

Service Settings → Remove service opens a draft of the whole application.
Configuration → Edit configuration also lets you remove services, including the
last one. Review and deploy the revision to apply removal. TOML uses an explicit
empty `[services]` table for the final removal; omitting the table is invalid.
New applications still require at least one service.

Domain mappings for a removed service are removed from its draft. Dependencies
and peer rules remain explicit: validation identifies references to adjust before
deployment. Kubernetes cleanup removes owned deployments, service routes and
public TCP bindings. The release waits for retired pods and deployments to stop.
A fresh runtime observation then labels a zero-service application **Empty**.
Persistent volumes, backups and the namespace are retained.

Project administrators can delete an empty application from its Configuration
tab. Deletion requires the exact name and the revision captured when confirmation
opens. Active runtime work, deployments, source jobs, builds or enabled backup
schedules block deletion. A successfully deployed empty revision is required;
zero visible pods or a failed rollout is insufficient.

Application deletion removes its API record, deployment events/history, build
records and Git bindings. Resource-scoped and integration credentials are revoked.
Repository workflow files are not edited. Audit events and backups remain. The
operator retains access to the namespace and persistent data using the original
application ID; deletion does not destroy storage or namespace secrets.

Installation administrators can delete empty shared projects from the Projects
cards. The server checks **all environments**, not the currently selected one.
Applications, build configurations, networks/registries and secret-provider scope
grants must be removed first. Personal workspaces are retained for account access.
The default project cannot be deleted before first-time administrator setup.

Deleted application names and project IDs stay reserved. This prevents old keys,
webhooks and name-based network grants from reaching a different resource. Choose
a new ID when creating its replacement. Deletions and credential revocations are
transactional and audited; concurrent creation and runtime work are fenced.

The dashboard and other clients share:

- `DELETE /api/v1/applications/{id}` with `confirm_name` and `expected_revision`.
- `DELETE /api/v1/projects/{id}` with `confirm_name` (the project ID).

Both return `{"status":"deleted"}`. Failed requests retain the confirmation draft.

## Verification

Real PostgreSQL tests cover authorization, stale review, nonempty resources,
active runtime claims, reserved IDs, history removal and concurrent application
creation. Named-development-cluster acceptance confirms final-service removal,
pod termination, route removal, retained PVCs and idempotent replay. UI tests cover
empty-state freshness, domain removal, retained dependencies and browser proxy
session/origin checks. The account menu now uses a 288px width, 13px item text and
36px rows, increased to 44px for coarse pointers.

Independent source review used `docs/ui-ux-checklist.md`. Browser and screenshot
review remains deferred at the user's request. These changes have not been
published or installed into the Ubuntu VM.

Checks for this change: full Go suite with isolated PostgreSQL, 29 dashboard
server tests and 56 UI regressions, TypeScript, formatting, and the production
build passed. Real-cluster checks passed for final-service removal and declared
body-size enable/reset. The HAProxy check waits for HTTP behavior as well as
configuration generation because file updates can precede reload activation.
