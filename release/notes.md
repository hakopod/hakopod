Hakopod 0.1.0-alpha.60 adds reviewed database and application recovery workflows.

This release makes database and application recovery easier to inspect and retry. Reviews show what will change, operations keep their progress after a restart, and the dashboard separates saved settings from evidence collected from a running container.

- Follow database resize stages and review the engine's update strategy and expected interruption.
- Resume failed services and their dependents at the accepted deployment revision. Completed migration jobs stay intact; independent services can make progress with bounded concurrency.
- Inspect a binding from saved configuration through resolution, container loading and authenticated connection testing. Stale evidence is shown explicitly.
- Create a dedicated PostgreSQL database and login for an application, review its grants, and queue the binding deployment.
- Review and repair an abandoned migration lock for the pinned Infisical Knex PostgreSQL profile. Active migrators and unexpected schemas prevent repair.
- Get private connection instructions for a local machine, an SSH session or a Kubernetes client.
- Compare backup versions, image references, encryption-key references, dependencies and selected recovery points before a restore.
- Review and remove eligible installer download-cache files on self-hosted installations. Receipts distinguish removed, skipped and uncertain outcomes and show the observed change in free space.

Application provisioning currently supports PostgreSQL. Migration recovery supports the documented Infisical profile. Cleanup is restricted to installation owners and does not include application volumes, database volumes, backups or rollback files. A successful provisioning operation queues an application deployment; the running application's connection is verified separately.

Read the [recovery workflow guide](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.60/docs/recovery-workflows.md) and [binding and private access guide](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.60/docs/binding-inspection-and-private-access.md) for permissions, commands and supported behavior.

Install this prerelease with:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.60/installer.sh -o installer.sh &&
sudo sh installer.sh --version 0.1.0-alpha.60
```

Upgrade an existing alpha.58 or alpha.59 installation with:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.60/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.60
```

Older installations need a supported intermediate release. The upgrade backs up
PostgreSQL and configuration, then restarts the management API and dashboard.
Keep those backups; replacing binaries does not reverse database migrations.
The OSS release and the Cloud production rollout have separate verification.
