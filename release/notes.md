Hakopod 0.1.0-alpha.41 makes managed GitHub Actions runner setup clearer.

- Pool creation and deployment secret entry share a step-by-step guide to creating a fine-grained GitHub token, choosing its owner and repositories, setting permissions and completing organization approval.
- Required permissions stay visible: organization Self-hosted runners or repository Administration with read and write access, plus repository Actions read-only access for job steps and completed logs.
- Managed Actions uses the GitHub icon in the catalog, application cards, service cards and topology. Service icons follow the Actions configuration, including older runner images.
- Runner application and service names retain browser validation with modern HTML pattern rules, including support for hyphenated names.

## Installation and upgrade

```sh
curl --fail --location --proto '=https' --tlsv1.2 \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.41/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.41
```

Direct upgrades are supported from alpha.39 and alpha.40. Older installations need supported intermediate releases. Each upgrade backs up PostgreSQL and configuration and restarts the management API and dashboard. Retain backups because swapping binaries does not undo database migrations.

Publication requires source checks, native package smoke checks, and the complete fresh-install and upgrade matrix on AMD64 and ARM64. UI review covers both themes and editions at desktop and mobile sizes; its scope and evidence are recorded in `docs/actions-token-guidance-ui-review.md`.
