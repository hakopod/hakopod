Hakopod 0.1.0-alpha.38 adds a TypeScript SDK source preview and makes managed GitHub Actions runners ready for larger builds.

## TypeScript SDK and scoped keys

- Manage applications, services, private networks and PostgreSQL/Redis databases through `app()`, `service()`, `network()` and `db()` using an API URL and scoped key. Self-hosted Hakopod and Cloud share the API; Cloud also needs its matching workspace authorization integration.
- Review plans before applying, wait for durable operations, inspect real runtime and logs, and reject stale revisions. Database replication, recovery and authorization remain owned by the server.
- Issue, rotate and revoke automation keys in Settings. Cloud keys are bound to their workspace and installation and retain current membership, permissions and approval checks.

The SDK is a source preview at [`packages/sdk`](https://github.com/hakopod/hakopod/tree/v0.1.0-alpha.38/packages/sdk). Its package version is `0.1.0-alpha.1`; this server release does not publish `@hakopod/sdk` to npm. Build and pack it using the documented instructions. Keep API keys in trusted scripts, CI or server code.

## Managed GitHub Actions

- Choose 2–16 GiB of temporary disk per runner. Source, tools, Docker images and build files share the reservation and are removed after the job.
- Keep Docker data on the reserved workspace instead of a separate memory-backed filesystem. CPU, memory, disk and lifetime bounds still apply; Docker's VFS storage can consume more disk than compressed image sizes suggest.
- Accept both canonical representations of the exact approved runner image digest. Image resolution no longer makes a valid runner deployment fail its own validation.
- Compare saved and resolved runner configurations consistently so a healthy runner remains Ready and retains its slot.
- Allow an explicitly configured dedicated Actions node in Cloud, and provide a bounded development acceptance path for real candidate jobs.

Managed Actions requires its Pro capability and a separately configured sandbox runtime. Upgrading does not install that runtime or change GitHub runner-group access. See the [setup and capacity guide](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.38/docs/managed-actions.md).

## Installation and upgrade

```sh
sudo sh installer.sh --upgrade --version 0.1.0-alpha.38
```

Direct upgrades are supported from alpha.36 and alpha.37. Older installations must use a supported intermediate release. Migration 051 adds automation-key scope bindings. Take and verify an installation backup before upgrading; swapping binaries does not undo database migrations or data changes.

The managed PostgreSQL and Redis features from alpha.37 remain included. Their separate controller requirements and recovery limits still apply; read the [managed database guide](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.38/docs/managed-databases.md).

## Validation

The merged implementation passed the Go, SDK and dashboard CI suites and template runtime acceptance on amd64 and arm64. Development-cluster acceptance covered the SDK's PostgreSQL/application/network lifecycle and managed-runner shared disk behavior. The migrated private Cloud and website workflows completed real jobs on the managed runner. These checks do not establish machine-level high availability or compatibility with every GitHub workflow.

Publication is gated on fresh source checks, packaged smoke tests, native fresh-install and upgrade acceptance on amd64 and arm64, probe-image checks, checksums and build provenance. See the [SDK acceptance record](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.38/docs/typescript-sdk-acceptance.md) and [managed Actions verification](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.38/docs/managed-actions-design.md) for scope and limitations.
