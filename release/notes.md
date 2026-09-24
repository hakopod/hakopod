Hakopod 0.1.0-alpha.26 adds shared missing-secret setup and expands the deployable catalog.

## Set up missing application secrets

TOML, Compose, Git import, Git synchronization and built-image review show missing local secret references before deployment. Enter each value directly into scoped storage or explicitly generate a new random password. Existing values are never overwritten by setup. Catalog-specific credential helpers retain their required formats.

The native CLI and npm CLI offer hidden terminal entry, generation and cancellation. Noninteractive runs stop with actionable missing names; `--yes` never invents credentials. Automated deployment and rollback recheck references before acceptance, and reconciliation reads the full secret snapshot before changing workloads. External secret providers keep their existing resolver.

## Expanded catalog

Dagu, CouchDB, Blinko and Baserow have complete service configurations; Bytebase now includes its required persistent setup. Images are pinned by digest. Baserow uses separate backend, frontend, workers, scheduler, proxy, PostgreSQL and Redis services, with customer-owned private S3 storage for uploaded media.

Cal.com remains unavailable until its non-root compatibility image can be pulled publicly. Autobase remains unavailable because its upstream console requires unsupported host Docker access. Managed Actions is not included in this release.

## Upgrade

Back up the installation and run the release installer:

```sh
sudo sh installer.sh --upgrade --version 0.1.0-alpha.26
```

Supported upgrade sources include alpha.25 and its supported predecessors. This release does not resize or delete existing volumes. Cloud and BYOD runtimes must be upgraded to expose the new secret-setup API.

## Validation

The Go and dashboard suites cover deployment planning, authorization, creation conflicts, unavailable storage and noninteractive cancellation. Independent rendered review covers both dashboard editions and themes at mobile and desktop sizes. Pseudo-terminal tests exercise hidden entry, explicit generation and cancellation in both CLIs using synthetic API credentials. Catalog acceptance checks supported presets on AMD64 and ARM64; real development-cluster secret tests verify scoped values reach a job without leaking into deployment configuration.
