# Self-hosted installation maintenance

Infrastructure includes API logs, Setup and Updates for the installation owner.
These routes also enforce owner access in the API. Cloud users do not receive
these controls. Ordinary administrators, scoped keys and CLI credentials cannot
use the host-maintenance API.

This work adds the upgrade protocol after alpha.4. Alpha.4 itself only supports
resuming the same installation. Do not use `--resume` to upgrade or edit its
saved configuration to force a different release.

## Upgrades

Once a release with upgrade support is published, download its bootstrap and
review the upgrade before running it:

```sh
curl --fail --location --proto '=https' --tlsv1.2 \
  https://hakopod.com/scripts/installer.sh -o installer.sh
sudo sh installer.sh --upgrade --version RELEASE_VERSION
```

Use an actual published version without `v`. The website bootstrap defaults to
its stamped release. `--upgrade` cannot be combined with `--resume` or `--config`.
It asks for `upgrade RELEASE_VERSION`; `--yes` accepts that plan unattended.
`--dry-run` verifies downloaded artifacts without stopping services; runtime
compatibility and backups are checked during the actual upgrade.

New installs include `hakopod-maintenance`, a root-owned service accessible only
through a local Unix socket. The API service can request fixed log/status/upgrade
operations; it cannot choose a shell command, unit, repository or download URL.
On alpha.4, a supported bootstrap upgrade installs this service before starting
the new dashboard. A running maintenance helper is not replaced by a binary
upgrade. Helper and runtime migrations need a separate reviewed procedure.

Updates checks the official GitHub release API and caches results for 15 minutes.
Stable installs stay on stable releases; alpha installs include prereleases.
Checks inspect at most 30 release records and three candidate manifests, newest
first, within the current major/minor series. A release must provide an
`upgrade.json` compatibility manifest and checksummed binary/dashboard assets.
The manifest must explicitly name the installed source version and match the
supported runtime pins. The updater also checks installed K3s and the retained
Helm/Node download checksums. Missing cache or changed runtime pins require
administrator inspection. A network failure is shown as unavailable.

The upgrade:

1. Takes the installer lock, verifies ownership and compatibility, and downloads
   release files from the fixed Hakopod repository over HTTPS with size/time bounds.
2. Checks SHA256SUMS, archive paths, version and dashboard runtime. It requires
   3 GiB free for backup/staging and 2 GiB on the release filesystem.
3. Stops only the API and dashboard, then writes a PostgreSQL custom-format dump
   and an archive of `/etc/hakopod`. Backups remain root-only under
   `/var/lib/hakopod/maintenance/backup-*`. The dump is bounded by available disk
   space, with an 8 GiB ceiling; larger installations need a manual upgrade.
4. Atomically switches the release symlink and starts the new API and dashboard.
   It checks database readiness and the actual local dashboard listener, including
   certificate verification for HTTPS installs.
5. Records progress outside the API process so the dashboard can reconnect after
   restart. The header shows a link when an eligible update is available.

Application containers, K3s, PostgreSQL's server version, accounts and secrets are
preserved. Checksums over HTTPS establish consistency with the repository release;
they are not independent release signatures. Releases must be published by the
trusted Hakopod maintainers.

If failure happens before the switch, the updater attempts to restart the old
management services. After the switch, new migrations may already have committed:
it reports that administrator recovery is needed and does not automatically run
older binaries or restore a database. Inspect:

```sh
sudo systemctl status hakopod-maintenance hakopod-api hakopod-dashboard
sudo journalctl -u hakopod-maintenance -u hakopod-api -u hakopod-dashboard --since -15min
```

Retain backups and verify a restore on a separate machine. Do not rerun setup or
remove application volumes to recover a management-service failure.

Release builders opt in only after testing an upgrade from each supported source:
`python3 release/build-installer.py --version TARGET --upgrade-from SOURCE`.
Repeat `--upgrade-from` for each tested source. The default compatibility list is
empty. No production source-to-target upgrade is advertised solely because the
unit tests pass.

## Optional modules

On releases containing the maintenance helpers, enable node-local volumes with:

```sh
sudo python3 /opt/hakopod/maintenance/modules.py storage
```

This installs the pinned provisioner and default `hakopod-local-path` storage
class. It refuses foreign resources or another default class. Pending claims
without an explicit storage class can then bind. Local volumes stay on their
node; they are not replicated storage or backups. PostgreSQL's installer-owned
static volume is separate and unchanged.

For automatic certificate management:

```sh
sudo python3 /opt/hakopod/maintenance/modules.py cert-manager
```

The module installs the pinned cert-manager chart and refuses foreign ownership.
Create an issuer under Infrastructure > Certificates. Start with Let's Encrypt
staging before production. HTTP-01 requires public DNS pointing at the host and
inbound port 80. Local `.test` or `.localhost` names cannot receive public ACME
certificates. Installing the controller does not itself configure a public issuer.

For alpha.4 without these helpers, use the optional modules from a verified
installer kit with an explicit kubeconfig/context, or have the operator add a
storage provisioner. Do not modify the saved resume fingerprint. Optional module
state is recorded separately in `/etc/hakopod/modules.json`.

## SMTP listener readiness

Infrastructure > Setup shows the configured helper and its installation steps.
SMTP and combined HTTP/listener checks require the digest-pinned probe helper.
New releases publish a prebuilt amd64/arm64 image from `Dockerfile.probe`; copy
its complete reference from the release's `probe-image.txt`. Older releases
without that asset require a local build. See the verification and systemd steps
in [listener readiness](readiness.md#helper-installation).
The image remains an operator setting, not an application-controlled executable.
Review the deployment again after restarting the API with the configured image.

## API logs

The log view reads only `hakopod-api.service`, at most 200 recent entries and
512 KiB per request, with a five-second journal deadline. It refreshes every
five seconds while visible and unpaused. Credential-related entries are omitted;
this is defense in depth, not a promise to recognize arbitrary secret text.
Do not log credentials in application code. Setup tokens, environment files,
arbitrary journals and host paths are not exposed by this endpoint.

## Verification

The implementation has unit/integration coverage for owner and Cloud boundaries,
log response limits, storage preflight, version/channel selection, manifest and
runtime checks, failure before and after the release switch, and concurrent
upgrade rejection. Live upgrade acceptance on an installed source/target release
is still required before populating a release's compatibility manifest.
