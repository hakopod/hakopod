# Recover databases and applications

A recovery workflow needs a clear answer to three questions: what will change, what has already happened, and what is safe to retry. Hakopod records reviews, accepted revisions and operation progress so those answers survive an API restart.

## Follow a database resize

Review a resize from the database page before applying it. The review shows the current and requested resources, the engine's update strategy and any expected interruption. A standalone database can become unavailable while its only member restarts. A cluster still needs enough healthy members and spare capacity for the engine's supported replacement process.

The operation records its observed stages. These can include member replacement, replication catch-up, a primary change and final verification. Stages appear only when the controller or member observations support them. If that evidence is missing or belongs to an older revision, the operation says it is waiting for a current observation.

A primary change uses the engine's supported control path. Hakopod does not force a primary change merely to make a progress indicator advance. Review the current primary and connection endpoints after the resize completes. Some engines restrict which resource or replica changes can be made in place; the resize review keeps those restrictions visible.

## Resume a partially failed deployment

An application can have one failed service while independent services are already healthy. **Resume failed services** retries the failed or incomplete services and their dependents at the accepted revision. It does not create a new revision or resolve newer image tags.

Independent services run in bounded groups of at most four. Dependency ordering still applies. Completed deployment jobs are retained. A failed migration job requires a new reviewed revision; a retry must not silently rerun a migration whose effects are uncertain.

Resume is available only when the latest failed deployment contains a recorded service attempt. Failures before workload execution, a newer application revision, active volume maintenance and conflicting runtime operations can prevent resume. The API checks the caller's current scope and records each attempt separately.

The CLI uses the same operation:

```sh
hakopod resume DEPLOYMENT_ID --revision REVISION --idempotency-key RETRY_KEY
```

Keep the same idempotency key if the request times out. A retry with that key returns the accepted operation instead of creating another attempt.

## Inspect the connection the application uses

The binding inspector shows **saved**, **resolved**, **loaded by container** and **connection verified** separately. A saved password or connection setting does not prove that a running container loaded it. Earlier evidence becomes stale when the container, revision, credential or CA changes, or when the five-minute evidence window expires.

See [binding inspection and private access](binding-inspection-and-private-access.md) for the supported protocol checks, permissions and connection commands.

## Review a restore before cutover

A restore review reports the evidence available for the selected source and target:

- The recorded backup capture time. A missing or future recovery point is shown explicitly.
- The source and target database versions, including whether the selected restore path is supported.
- Captured application image references. Equal digest-pinned references can be compared; equal mutable tags do not prove equal versions.
- The required encryption recipient reference. Matching that reference does not prove that a private recovery key is available. The restore worker authenticates the encrypted archive before changing the target.
- Captured dependency relationships and differences in the target application.
- The capture times of any related archives selected for the review.

Select up to 16 related archives when several services must be recovered together. Hakopod reads their stored capture times and flags a spread greater than five minutes. A shorter spread is a time comparison, not proof of a shared transaction boundary. An omitted recovery set is reported as unknown. Old archives can also lack image or dependency evidence; the report does not invent it.

A blocked report has no executable review ID. Accepted reviews expire and are checked again when used. A changed target application revision or a related archive pending deletion requires a new review.

Managed database recovery uses a separate, unused target. Application database recovery creates a fresh logical database. After restore, inspect the recovered content before changing application bindings. Writes made after the source capture are not part of that backup.

For a managed database, a CLI recovery-set file can contain:

```json
{"related_artifact_ids":["RELATED_ARTIFACT_ID"]}
```

```sh
hakopod database restore-plan DATABASE_ID --artifact-id ARTIFACT_ID --file recovery-set.json
```

The SDK accepts `database.restorePlan(artifactId, { relatedArtifactIds })`. The resulting review contains the compatibility report. Platform archive reviews use their recorded release, images, encryption recipient and volume inventory; they state when no related application recovery set is available.

## Review server cleanup

Self-hosted installation owners can open **Infrastructure → Safe server cleanup**. The preview lists the exact old installer download-cache files eligible for removal. It also shows disk capacity and available space. Disk cleanup does not increase a database's memory limit or diagnose memory pressure.

The current cleanup scope is the installer's download cache. Database volumes, application volumes, backup archives, Kubernetes state, secrets, installed releases and rollback files are outside that scope. The helper checks file ownership, age, link count and identity again immediately before removal.

The review expires after ten minutes and requires the phrase shown on the page. Removal runs under the installer's maintenance lock. The helper records each file before unlinking it and retains progress if the process stops. Retry the same operation to continue. Reading operation status never starts removal.

The receipt separates confirmed removed files, skipped files and files whose outcome became uncertain during an interruption. It also reports the filesystem's measured change in available space. Other processes can write or free space during cleanup, so that measurement can differ from the allocated bytes of confirmed removed files.

Cloud workspace users cannot clean up the shared host. That operation belongs to the installation owner.
