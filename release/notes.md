Hakopod 0.1.0-alpha.51 adds scoped host recovery authorization for the embedded Cloud runtime and records completed native Xem acceptance.

A trusted Cloud host can issue a dedicated application-scoped machine key for one internal Actions pool. Each request rechecks the operator, application and retained installation binding before using the existing lifecycle API. Stop, rollback, cancellation and idempotent retries retain optimistic revision checks and durable worker authorization. Revocation stays effective. This helper is limited to the managed Cloud embedding; it adds no public credential issuer or browser authentication bypass. The paired Cloud release supplies the root-only Unix listener and recovery workflow.

Xem's final public images passed all five native AMD64 runtime cases on an isolated cloud VM: all four PostgreSQL/Redis bundled or external combinations with bundled MinIO, plus all dependencies external. The checks covered login, private upload and signed download, restart persistence, Redis database selection, and PostgreSQL/Redis TLS acceptance and refusal. The evidence combines three individually passing cases from R4 with the last two from a scoped R5 retry; it is not claimed as one uninterrupted run. Test cleanup now checks all private Docker volumes, and TLS probe deletion uses immutable UIDs.

Final-image ARM64 Xem runtime, arbitrary external providers, real email delivery, public ACME/DNS, bucket CORS and backup restoration remain outside that acceptance. Mathesar still requires RWX storage and is unavailable on hosted shared Cloud compute. Existing alpha.50 managed MySQL behavior is retained.

Upgrade with the published installer:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.51/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.51
```

Direct upgrades are supported from alpha.49 and alpha.50. Older installations need a supported intermediate release. Each upgrade backs up PostgreSQL and configuration and restarts the management API and dashboard. Retain backups because swapping binaries does not undo database migrations.
