Hakopod 0.1.0-alpha.7 adds Compose import, clearer Git deployments, runtime controls before deployment, and new build and release workflows.

## Deploy applications

- Import image-based Docker Compose into reviewed `config.toml`, either as a new application or by adding services to an existing one. Supported settings include per-service replicas, entrypoint/command overrides, explicit environment interpolation, private ports, networks and named volumes. Unsupported options produce actionable errors. Host publishing and existing Docker volume data are not copied.
- New application setup links directly to **Build from Git** and **Import Git configuration**. Source builds offer Dockerfiles, Cloud Native Buildpacks and framework detection for supported JavaScript/static projects. An existing Dockerfile takes precedence; detected settings remain editable.
- Set runtime commands, arguments and plain environment variables before deployment. Linked builds preserve existing variables unless explicitly replaced. Runtime variables are not inserted into build workflows; secrets use their separate binding and provider-secret paths.
- Configure replicas per service in TOML. Stop and resume preserve configuration, storage and the saved replica count while suspending autoscaling. Service deletion follows the normal revision-checked deployment plan.
- Rename projects, applications and services through reviewed controls. Application actions are grouped in the three-dot menu, and project card actions are aligned.

## Build and release workflows

- Framework recipes build static sites into a non-root nginx runtime or supported server applications into a Node runtime. GitHub Actions or GitLab CI performs the build; the production API host does not execute repository builds.
- Scoped MCP tools let coding agents inspect applications and review or submit explicitly authorized deployment operations.
- Scheduled jobs support five-field cron expressions, timezones, bounded retries, execution limits and retained run history. Pause/resume controls and observed status are available in the dashboard.
- Preview environments use isolated application namespaces, explicit configuration and bounded lifetimes. Creation does not copy production secrets, volumes or data. CI can call the API after a trusted build; automatic pull-request/merge-request lifecycle is not included.
- Durable release recovery can restore a compatible prior stateless release after failure. The failed attempt and recovery result remain distinct. Recovery does not reverse database migrations or restore previous secret values.
- Build-secret references keep secret values in the selected CI provider's secret store rather than in saved build configuration.

## Fixes and shared engine

- Restore scoped Git and application-management capabilities when identity responses hide installation-wide administrator flags.
- Forward dashboard stop/resume actions correctly, guard cancelled job creation, and retry owned-resource cleanup conflicts.
- Wait for PostgreSQL's final TCP listener during local setup.
- Simplify self-hosted focus styling and remove decorative brackets. Shared dashboard edition hooks let the private Cloud distribution reuse the public interface without including private Cloud pages in these archives.
- Trusted runtime hooks support workspace isolation, HTTP idle sleep/wake and explicitly bound dedicated BYO TCP listeners. These do not grant ordinary workload credentials installation-wide administration.

## Prebuilt artifacts

This release includes Linux amd64/arm64 server, CLI and dashboard bundles, macOS amd64/arm64 CLI archives, and the installer kit. Installation does not compile Go or frontend sources on the target.

The multi-platform readiness helper is published at `ghcr.io/hakopod/hakopod-probe:v0.1.0-alpha.7`. Use the immutable reference in the attached `probe-image.txt` for Infrastructure > Setup.

Download this release's `installer.sh` and run `sh installer.sh --help`. Select this version explicitly with `--version 0.1.0-alpha.7`. The website bootstrap is deployed separately from GitHub publication.

The assets include `SHA256SUMS`, SBOMs, dependency/license notices, build provenance, native smoke reports and host acceptance reports. Verify publisher provenance with `gh attestation verify FILE --repo hakopod/hakopod`.

## Verification and compatibility

Compose deployment acceptance passed on native amd64 and arm64 Kubernetes clusters, including two replicas, runtime variables, private DNS, persistent data and additive service imports. The shared dashboard passed independent desktop/mobile, light/dark and keyboard review. Publication additionally requires the exact release archives to pass native packaged smoke, Ubuntu 24.04 systemd/K3s host acceptance and probe-image checks.

This is an alpha release. Public binaries retain first-administrator setup and invitation-based access; open signup remains disabled. Existing installations should consult the attached `upgrade.json` for verified upgrade paths. A new version being available does not establish compatibility with every earlier release.

Compose is not a universal Docker runtime translator. The complete template catalog and arbitrary private repositories are not runtime-certified. Public DNS/ACME, host reboot, restore drills, physical RDS, other Linux distributions and existing-database host modes on arm64 remain outside release acceptance. OAuth/SSO provider setup and CI permissions still require configuration in the respective provider.
