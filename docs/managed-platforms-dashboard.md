# Manage a platform from the dashboard

Managed platforms give a related set of services one configuration, revision
history and recovery workflow. Supabase includes PostgreSQL, authentication,
storage and application APIs. Neon separates PostgreSQL compute from its
storage services. They have different resource and recovery requirements from
a standalone managed database.

Open **Databases**, then **Platforms**. The list shows resources you can read.
Choose a project and environment before creating one. The server controls which
platforms can be created: a release must pass its runtime checks, and the
installation must have its images, storage, capacity and secret references
configured. A platform that has not passed those checks is not a selectable
option. The dashboard does not enable a runtime by itself.

## Create a platform

The setup pages keep your entries while you move between steps:

1. Choose the platform and give it a name.
2. Set its application URLs, database settings or object-storage connection.
3. Choose the configured nodes. The server checks their identity and capacity.
4. Select immutable secret references. These are names and revisions; the
   dashboard never reads their values.
5. Review each component's CPU, memory and storage. Open **Edit** to change an
   allocation on its own page.
6. Request the server's plan, inspect it and confirm creation.

The review expires. A changed resource or capacity policy requires a new
review. If submission fails, your entries stay in the form. An accepted request
appears in the platform's activity; it is not shown as ready until the server
observes the required components.

The current Supabase deployment uses one node because some components share
ReadWriteOnce storage. Neon separates pageserver and safekeeper members across
the selected nodes. Selecting different node names does not establish that
they run in different physical zones or cloud providers. Those availability
claims need their own infrastructure and failure tests.

## Read the platform state

The detail page has four tabs:

| Tab | What it shows |
| --- | --- |
| Overview | The observed revision, ready component count and pending work. Neon also shows observed tenant, timeline and compute information. |
| Recovery | Backup and restore operations, their status and cancellation controls. |
| Activity | Accepted lifecycle operations, attempts and server-reported results. |
| Configuration | Requested component resources, node placement and secret references. |

Requested resources and observed readiness are shown separately. Missing
observations say **Not observed**. If a refresh fails, the page labels the
retained state. A running operation does not imply that every component is
ready, and the page does not generate metrics or logs that the server has not
reported.

## Change Supabase settings

Open **Configuration → Configure platform**. The form starts from the loaded
revision. Another edit cannot silently replace that revision while you work;
the server rejects a stale update and requires another review. Applying a
change can restart services and interrupt existing connections.

Database credentials change as one bundle: the owner password, role bootstrap
and all six client references must agree. Database TLS can change separately.
Gateway TLS must change with its Envoy configuration. JWT signing keys, API
keys, encryption keys and object-store credentials remain disabled in this
form until their rotation procedures are supported. The JWT expiry setting
is separate from signing-key rotation.

## Back up and restore

Choose **Recovery → Back up**, select an encrypted destination and review the
source revision and destination. The confirmation explains that writes pause
while Hakopod captures a consistent backup. Follow the operation to completion;
an accepted job alone is not a usable archive.

To restore, create a separate empty platform of the same kind. Choose
**Restore**, select a completed backup for the current source revision and
select the recovery target. The server checks compatibility and emptiness.
Type the target's name to confirm. Recovery keeps the source in place and does
not change your applications' connection settings.

Cancellation is a request to stop safely. It may wait for cleanup, and the
operation remains active until that cleanup finishes. Check the final state
before reusing a recovery target. If cancellation fails, the page keeps the
operation and displays the error.

For the underlying services, storage layout and security boundaries, read the
[Supabase architecture](managed-supabase.md) and [Neon architecture](managed-neon.md).
Their qualification documents distinguish source tests from complete native
runs and record limits on public endpoints and availability across zones.
