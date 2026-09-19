Hakopod 0.1.0-alpha.18 records external CI build provenance alongside deployments.

## Commit-to-image mappings

Plan and deployment requests now accept a per-service provenance map containing the exact image digest, full Git commit SHA, provider, repository and optional branch/workflow URL. Claims are validated against the selected service images, persisted atomically with the immutable release and included in the idempotency hash.

Application provenance and MCP expose these records as ci_reported, with the reporting identity and deployment ID. A unique mapping exposes commit_sha; conflicting mappings remain ambiguous. These are authenticated CI assertions, not independently verified attestations. Runtime image IDs and readiness remain necessary to establish deployment convergence.

Existing unknown mappings are not guessed or automatically backfilled. Update the CI workflow to submit the actual commit that produced the image. See [the CI provenance example](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.18/docs/ci-provenance.md).

## Upgrade

This release adds a PostgreSQL column for immutable deployment provenance. Declared upgrade candidates are alpha.8, alpha.9, alpha.10, alpha.12, alpha.13, alpha.14, alpha.15 and alpha.17. Alpha.16 has no published installable release.

Download this release's installer.sh, then run:

```sh
sudo sh installer.sh --upgrade --version 0.1.0-alpha.18
```

The installer backs up PostgreSQL and configuration, migrates state, replaces the API/dashboard and checks readiness. Publishing does not upgrade customer VMs.

## Verification and limitations

The implementation passed the full Go suite with disposable PostgreSQL, Go vet, API provenance integration tests, TypeScript checking, 43 server tests, 83 UI tests and the production dashboard build. Coverage includes exact digest validation, four-service deployment, retries, application-scoped authorization, historical mappings and conflicting source claims.

The tag is created before candidate CI completes, following the requested release workflow. Asset publication remains gated on the release build, native smoke tests and installation/upgrade acceptance.

The separate HTTP MCP logs error (unexpected end of JSON input) remains unresolved. This is an alpha prerelease.
