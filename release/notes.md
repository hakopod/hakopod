Hakopod 0.1.0-alpha.39 improves installer upgrade checks and recovery guidance.

## Installer upgrades

- Check the installed version against the release compatibility manifest during `--upgrade --dry-run`. An incompatible path now fails the check instead of reporting success before validation.
- Report supported source versions and, when a compatible intermediate release is found, print the next upgrade command. The bounded release lookup checks up to eight candidates so installations several releases behind can find a supported step.
- Check compatibility before downloading server and dashboard archives. The upgrade helper verifies those archives once during the actual upgrade.
- Keep download and execution commands joined with `&&` in the maintenance guide, so a failed download cannot execute an older local script.

Existing privileged maintenance services remain unchanged by binary upgrades. Use the bootstrap from this release for the new preflight behavior and guidance. Application pods, K3s and PostgreSQL versions remain unchanged.

## Installation and upgrade

```sh
curl --fail --location --proto '=https' --tlsv1.2 \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.39/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.39
```

Direct upgrades are supported from alpha.37 and alpha.38. Older installations must use supported intermediate releases; the updater never bypasses the compatibility manifest. Each step backs up PostgreSQL and configuration and restarts the management API and dashboard. Retain backups: swapping binaries does not undo database migrations.

The website installer redirect is maintained separately. Its GitHub API outage fallback is a website change, not part of the server binary.

## Validation

Publication requires source tests, packaged smoke checks and the native systemd/K3s fresh-install and upgrade matrix on amd64 and arm64, including managed, local and external PostgreSQL modes. Installer regression tests cover rejected dry runs, intermediate-release discovery, invalid manifests and bounded candidate searches. Public ACME, physical-host reboot and restore drills remain outside these checks.
