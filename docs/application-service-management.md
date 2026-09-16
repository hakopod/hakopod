# Add and move services

Open an application and choose **Add service**. Choose a catalog template, import
Docker Compose, enter a container image, or reuse an existing service's image.
The image flow opens that service's configuration for ports, commands, arguments
and environment variables, followed by a deployment review. Reusing an image
does not duplicate storage, commands or source-build automation.

Catalog links retain the destination application through filters and the review.
Single-service templates have an editable service name, so another template's
`main` service does not prevent adding a database or web server. Multi-service
templates keep their original names, including their dependencies. Conflicting
service names, volume names, domains or network definitions are rejected.
Template defaults apply to the added services, and unchanged existing services
keep their deployed image digests. Existing secrets can be reused; replacing one
requires the application's Secrets page, where its shared impact is visible.

## Move between applications

Choose **Move to application** from a service's menu. Moves are available between
permanent applications in the same project and environment, with deployment
permission on both. Both applications must have successful current deployments.
Pause automatic deployments and update source configuration before moving.

1. Choose the destination and a unique service name, then review both changes.
2. Deploy in the destination. The original stays running. The move retains the
   deployed image digest, run settings and effective environment. Local secrets,
   including secret file references, are copied under new names without exposing
   their values or replacing destination secrets. Additional destination defaults
   also apply.
3. Check the destination and update clients to its new default URL and private
   address. Both copies can process work until the handover is finished.
4. Return to the original application's **Services → Service moves** and choose
   **Finish move**. Confirm removal of the original. This queues a normal immutable
   deployment; the destination remains running.

The move record survives closing the page. Failed or cancelled destination
deployments cannot remove the original. Finishing checks both application
revisions again and refuses stale requests. If either application has changed,
inspect the two services before using the ordinary reviewed service deletion.
Source builds, source configuration and historical deployments remain attached
to their original application. Update them before resuming automatic deployment.

Persistent volumes, custom domains, jobs, public TCP, certificate bindings,
workload identities, external secret providers, shared virtual networks and
service dependencies require an explicit migration. The move review explains
these blockers. It never provisions an empty volume as a substitute for data.
For data-backed services, use a backup/restore and an explicit client cutover.

Hosted Free compute keeps its existing one-service limit. Additional services
require a BYO server. The first screen explains this before configuration.

## API and verification

- Template plan/deploy configuration accepts `application_id`,
  `expected_revision` and optional `service_name` for a single-service template.
- `POST /applications/{id}/services/{service}/move-plan` reviews a move.
- `POST /applications/{id}/services/{service}/move` starts its destination release.
- `GET /applications/{id}/service-moves` returns the latest 20 moves.
- `POST /applications/{id}/service-moves/{move}/finish` submits original removal.
- Mutating move requests use `Idempotency-Key`; the start input includes both
  expected revisions. Both dashboard proxy layers permit these exact routes.

PostgreSQL/API tests cover authorization on both applications, scoped secret
copying without values in responses, revisions, failed/pending destinations,
retries and catalog merging. Pure specification tests cover unsafe resources,
name collisions, inherited configuration and unchanged inputs.

`TestLiveServiceTransferAndCatalogAddition` passed against the explicitly named
`k3d-hakopod-dev` context. It verified two running applications, destination
readiness, original retention until finishing, original removal, unchanged peer
pod templates, inherited environment, private DNS and an additive catalog shape.
The catalog shape used a fixed Python fixture image; this does not certify every
upstream template image. No customer service was moved for verification.

An independent UI reviewer approved the shared dashboard and composed Cloud UI
in both themes at desktop and mobile widths, including 320px dialogs. Coverage
included catalog target retention, custom template names, image reuse, move
review and completion, pagination, permission failures, stale revisions, Hosted
Free limits, keyboard focus and screenshots. Browser verification used isolated
fixture responses; Kubernetes and API evidence above are separate.
