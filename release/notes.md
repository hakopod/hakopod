Hakopod 0.1.0-alpha.15 adds authenticated HTTP MCP and per-service source provenance.

## HTTP MCP

Connect a Streamable HTTP MCP client to `https://YOUR_HOST/api/v1/mcp?project=demo&environment=development` with a matching scoped machine bearer key. The self-hosted dashboard forwards the route to the API. HTTP and stdio share tools and authorization. Deployments require explicit opt-in and a reviewed plan.

Tools expose application and service configuration, runtime pods, readiness, available metrics, domains, logs and deployment details. Sessions, requests and responses are bounded. Hosted Cloud's HTTP route remains disabled. See [the connection guide](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.15/docs/http-mcp.md).

## Build provenance

Accepted image digests map to verified successful build records, exposing Git commit SHA, repository and build-run metadata, including reused service images. Unknown, ambiguous and truncated mappings never claim an authoritative SHA. An accepted image does not prove rollout success: inspect readiness and running image IDs before closing tickets against a commit.

## Upgrade

Supported upgrade candidates are alpha.8, alpha.9, alpha.10, alpha.12, alpha.13 and alpha.14. Publication requires passing native installation and upgrade checks. Alpha.11 has no installable assets.

Download this release's `installer.sh`, then run:

```sh
sudo sh installer.sh --upgrade --version 0.1.0-alpha.15
```

The installer validates the upgrade path, backs up PostgreSQL and configuration, replaces the API/dashboard and verifies readiness. Application workloads remain running.

## Verification

Go tests with disposable PostgreSQL, Go vet, dashboard typechecking/build and proxy tests passed during development. Official MCP SDK 1.29.0 interoperability covers initialization, tool listing, scoped reads, tool errors and session termination. Actual Arize account integration is not verified. Static bearer authentication is supported; dynamic OAuth registration is not.

Publication additionally requires native packaged smoke tests and fresh-install/upgrade acceptance with managed PostgreSQL on AMD64/ARM64 and local/TLS-verified external PostgreSQL on AMD64. Assets include checksums, SBOMs, provenance and acceptance reports. The probe image is `ghcr.io/hakopod/hakopod-probe:v0.1.0-alpha.15`; use the immutable reference in `probe-image.txt`.

This is an alpha prerelease. Publishing does not upgrade customer VMs.
