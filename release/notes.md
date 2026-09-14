Hakopod alpha.5 adds shared templates, deployment lifecycle primitives, Git App connections, and self-hosted installation controls.

## Deployment and catalog

- Shared public [template catalog](https://github.com/hakopod/templates), pinned into this release: 71 entries, including 35 deployable presets and 36 guides. Complex migrations remain guides until their requirements are supported and verified.
- Bounded one-shot deployment Jobs and dependency gates for migrations and initialization. Completed jobs are shown separately from running service replicas.
- Read-only configuration and scoped secret file mounts; derived private PostgreSQL, MySQL, Redis and HTTP connection URLs.
- Additional named HTTP endpoints, public build arguments, and preflight checks for resources, architecture, taints, storage and observed disk headroom.
- Custom domains can be staged before ownership verification; routing activates only after verification.

## Connections, settings and operations

- Named GitHub/GitLab connections across imports, sources and builds. GitHub App registration and installation use GitHub's manifest flow, with the App owned by the user or organization. GitLab uses OAuth and still requires one-time application registration. Existing token connections remain compatible.
- Encrypted self-hosted SMTP settings, licensed OAuth login and OpenID Connect SSO. Free self-hosted installations allow one new team; additional teams require a valid multi-team license. Existing teams remain accessible after downgrade. First-admin setup and invitations remain available; public binaries do not enable open signup.
- Owner-only API log explorer with bounded queries and a direct-process fallback; installation setup and upgrade controls; optional local storage and cert-manager module helpers.
- Reviewed resource deletion, compact account/settings controls, and self-hosted HAProxy request body limits.

## Prebuilt artifacts

Linux amd64/arm64 server, CLI and dashboard bundles and macOS CLI archives are included. Installation does not compile Go or frontend sources on the target. The release also publishes the tested multi-platform readiness helper at `ghcr.io/hakopod/hakopod-probe:v0.1.0-alpha.5`; use the immutable reference in `probe-image.txt` when configuring Infrastructure > Setup.

Download and review this release's `installer.sh`, then run `sh installer.sh --help`. The website bootstrap is deployed separately; GitHub publication does not update `hakopod.com/scripts/installer.sh`.

`SHA256SUMS`, dependency inventories, license notices, source provenance, native smoke reports and host acceptance reports accompany the artifacts. Verify publisher provenance with `gh attestation verify FILE --repo hakopod/hakopod`; checksums alone establish consistency. The attestation bundle is excluded from the checksum manifest to avoid a circular digest.

## Compatibility and verification limits

- This prerelease is for evaluation. Preserve database/configuration backups before any manual upgrade. Upgrade support is implemented, but the compatibility manifest advertises no source-to-target upgrade until that exact path has passed live acceptance; alpha.4 cannot upgrade itself through its old dashboard.
- Jobs run again on new revisions and must be idempotent. Rollback does not reverse database migrations. Mounted file changes require deployment; no hot reload is promised.
- Lifecycle behavior and Redis persistence were verified on a real ARM64 development cluster. The PR adds native amd64/arm64 runtime checks. Publication additionally requires packaged runtime smoke and native Ubuntu 24.04 systemd/K3s host acceptance on both architectures.
- The complete catalog is not runtime-certified. Automatic multi-build template orchestration and broad upstream upgrade testing remain open. Shared storage needs an appropriate CSI driver; preflight is not a complete scheduler or image-size forecast.
- Real hosted GitHub App/GitLab OAuth setup and provider-hosted builds with public arguments still need end-to-end verification. SSO implements OpenID Connect, not SAML or SCIM.
- Reboot recovery, public DNS/ACME issuance, physical RDS, restore drills, other Linux distributions and existing-database host modes on arm64 remain outside release acceptance.
- Arbitrary public TCP/SMTP and installation operator controls remain self-hosted features. Managed Cloud retains its HTTP/HTTPS and private-networking boundary.
