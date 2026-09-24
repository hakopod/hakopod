Hakopod 0.1.0-alpha.24 adds explicit retained-data reclamation and browser-authorized npm deployments.

## Reclaim retained data

Deleting an application preserves its persistent data by default. Opt in to permanent data deletion during application confirmation, or reclaim an already deleted application's data from its project page. Both require the application name. Backups are kept. Current authorization is checked during cleanup, and storage reservations are released only after owned Kubernetes volumes are reclaimed. Failed reclamation remains visible and retryable.

Compute allocations remain separate from storage reservations. This release does not automatically delete existing retained data or upgrade customer nodes.

## Guided npm deployments

The public `@hakopod/cli` package guides deployment of clean, pushed Git repositories. Browser consent binds its session to one project/environment or Cloud workspace. Framework detection, build commands, the generated Git workflow and the deployment plan are reviewed before execution. Interrupted builds resume with persistent idempotency keys. Membership, MFA and Cloud approval policies remain enforced.

The npm package has its own version and publication lifecycle. This server release provides its device-scope and build API support. The existing Go CLI remains available for TOML, logs and infrastructure administration.

## Upgrade

Back up the installation before upgrading. Supported upgrade sources are declared in `release/upgrade-paths.json`, including alpha.23 and its supported predecessors. Download this release's installer.sh and run:

```sh
sudo sh installer.sh --upgrade --version 0.1.0-alpha.24
```

Existing deleted applications are added to the retained-data list without erasing their data. BYOD nodes need this engine release to expose the new reclamation API.

## Validation

The full PostgreSQL-backed Go suite, vet, both dashboard builds/typechecks/tests, npm tests and packed-install checks passed. Independent rendered reviews cover both themes, mobile/desktop and failure states. Named development-cluster acceptance verified ownership denial, namespace/PVC/PV/native-secret cleanup and idempotent replay with local-path storage. This does not claim acceptance of every CSI driver or deletion of customer disks. Publication additionally requires native package smoke checks and the declared installation/upgrade matrix.
