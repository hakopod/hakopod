Hakopod 0.1.0-alpha.33 adds ClickHouse backups, DNS records created at the provider, gRPC backends, certificates for TCP-only services and operator-approved container daemons.

## ClickHouse backups

ClickHouse services can now be backed up and restored on a schedule or on demand, to the same object storage destinations as PostgreSQL and MySQL. ClickHouse performs the backup itself with `BACKUP ... TO S3`, so large databases are not streamed through the dump pipeline and are not bound by its size and time limits. A backup is recorded as complete only after the engine reports it created and its files are found at the destination; a run that is still in flight is shown as in flight rather than as success.

## DNS records at the provider

The custom domains page can create the records a domain needs directly at the DNS provider, for one domain or several at once. Cloudflare is the first supported provider. Provider credentials are stored encrypted, scoped to a project and environment, and can be limited to specific zones. An identical record is left alone, and a different record already at that name is reported as a conflict rather than overwritten unless replacement is explicitly requested.

## gRPC and HTTP/2 backends

`backend_http2 = true` on a public service makes the ingress speak HTTP/2 to it, which gRPC servers require. Services without it keep HTTP/1.1.

## Certificates for TCP-only services

Services published only over public TCP now receive an automatic certificate for their generated hostname, and can mount it to terminate TLS themselves. Custom hostnames on TCP-only services still need an uploaded certificate.

## Container daemons

An operator can register a container daemon reached over mutual TLS and approve it for specific services, which name it with `container_daemon`. Hakopod never mounts a host socket. Bindings require a self-hosted installation or a dedicated BYO node, and endpoints on loopback, link-local, cluster or API server addresses are refused.

## Builds

Builds can check out Git submodules recursively. A private submodule in another repository is not checked out, because the workflow token only reaches the repository that runs it. Creating an application now starts on building from source when a build-capable Git connection exists; otherwise the form is unchanged.

## Upgrade

```sh
sudo sh installer.sh --upgrade --version 0.1.0-alpha.33
```

Direct upgrades are supported from alpha.31 and alpha.32, the last two published versions. Older installations must upgrade through a supported intermediate release. This release adds two database migrations: DNS provider credentials get a new table, and backup records gain a column for engine-performed backups. Existing rows are not rewritten.

## Validation

Engine, API and dashboard suites pass, with the template runtime matrix on amd64 and arm64. ClickHouse backup and restore were exercised against a real server and S3-compatible storage, with matching row counts and full-row hashes. HTTP/2 backends, HTTP-01 issuance for TCP-only services, per-SNI certificate selection and hitless certificate updates were exercised on a development cluster. Publication remains gated by packaged smoke tests and the bounded 12-case native install/upgrade matrix.
