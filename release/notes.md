Hakopod 0.1.0-alpha.8 improves Git build onboarding, private image access, shared application configuration and dashboard feedback.

## Build and deploy

- Source builds follow Repository → Build recipe → Runtime → Review. Back navigation preserves drafts, and saving opens the generated workflow review. Installation and deployment still require their explicit actions.
- Pick repositories available to a saved GitHub App connection, or enter a public repository. Existing connections can use the same scoped repository authorization.
- Managed build-registry integration supplies scoped credentials for trusted GitHub build workflows and runtime image pulls. Registry authorization is bound to the selected repository and exact workflow branch; a GitHub login or App credential alone is not arbitrary registry access.
- Reuse a source build's image for additional services, each with its own command, arguments and environment. Runtime command overrides remain editable before deployment.
- Import or paste dotenv content into environment editors. Top-level `inject_env = true` opts services into shared application variables; service values take precedence. Shared application secret references remain separate from plain variables.
- TOML and supported Compose `env_file` directives accept explicitly supplied environment files. The importer never reads arbitrary server files; it expands supported values into the reviewed canonical configuration.

## Dashboard

- Request errors use dismissible Hatch toasts. Input validation stays beside its field, drafts survive failed requests, and failed sections retain retry and error-detail controls.
- Remove the global alert styling that exposed an empty accessibility live region as a strip above the header. Announcements and status information keep their in-page layout.
- Card backgrounds are `#fafafa` in light mode and `#141414` in dark mode.
- Clearer setup guidance, exact API field errors and staged build validation help users correct the relevant setting. Shared edition hooks expose applicable compute limits without duplicating dashboard forms.
- Pod logs and shared environment resolution were verified for web services, workers, one-shot jobs and scheduled jobs in the named development cluster.

## Prebuilt artifacts

Includes Linux amd64/arm64 server, CLI and dashboard bundles, macOS amd64/arm64 CLI archives, and the installer kit. Target machines do not compile sources.

The multi-platform readiness helper is `ghcr.io/hakopod/hakopod-probe:v0.1.0-alpha.8`. Use the immutable reference in the attached `probe-image.txt` for Infrastructure > Setup.

Download this release's `installer.sh`, inspect `sh installer.sh --help`, and select `--version 0.1.0-alpha.8`. Website bootstrap publication is separate. Check the attached `upgrade.json` for verified upgrade paths.

Release publication requires Go/dashboard checks, native packaged smoke tests, Ubuntu 24.04 systemd/K3s host acceptance and both probe-image architectures. Assets include checksums, SBOMs, dependency notices, provenance and acceptance reports. Verify provenance with `gh attestation verify FILE --repo hakopod/hakopod`.

This remains an alpha release. Self-hosted access uses first-administrator setup and invitations. Private Cloud pages are not included in public archives. Arbitrary private repositories, every template, public DNS/ACME, physical RDS and all Linux distributions are not runtime-certified by these checks. CI provider permissions and OAuth/SSO still require provider configuration.
