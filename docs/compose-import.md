# Import Docker Compose

Choose **New application → Import Compose**, or open an application's
configuration and choose **Import Compose** to add services. Paste a Compose
document or upload one YAML file, then select **Generate config.toml**.

Review the conversion differences, edit or download the generated TOML, and
review the normal deployment plan. Conversion itself never creates a workload,
runs Docker, builds an image, or changes the existing application. Imports into
an existing application preserve its services and reject duplicate names or
conflicting network/volume definitions. Reload after a concurrent revision change.
Attached environment files can stage application-scoped secrets during
conversion; unused references remain available for cleanup in Application secrets.

For repositories that need an image built first, use **Build from Git**. It offers
framework detection, Dockerfiles and Cloud Native Buildpacks. Compose import
requires an `image` for each service and does not execute `build` instructions.

## Supported settings

- Images, string/list command and entrypoint overrides, plain environment values,
  and explicit interpolation variables. The host environment is never read.
  `$VAR`, `${VAR}`, default/required/alternate forms, and `$$` work;
  nested substitutions require expansion before import.
- Service `env_file: .env` or `env_file: [.env, production.env]`, supplied through
  the **Environment files** attachment/paste control. Later files override earlier
  ones; explicit `environment` and `x-hakopod.secrets` override file values.
  File contents are literal service variables, separate from Compose placeholder
  interpolation. Sensitive file values become scoped secret references. See
  [environment file limits and precedence](environment-and-build-reuse.md#import-environment-files).
- Per-service replicas, working directory, non-root numeric user/group, read-only
  root filesystem, and Linux amd64/arm64 placement.
- Application networks and list-form dependencies. Hakopod waits for dependency
  readiness. Network aliases, external networks and Compose host networking do
  not translate automatically.
- Individual TCP/UDP container ports. **Compose host publishing is not copied.**
  Ports default to private; explicitly enable HTTP or configure an approved TCP
  listener. A published port does not tell Hakopod whether a service speaks HTTP.
- Named persistent volumes and read-only mounts. By default a volume receives
  1 GiB of new storage; existing Docker data is not imported. Configure the size
  and storage class explicitly for production data.

Unsupported fields fail with a field-specific message. These include host bind
mounts, privileged containers, devices, Compose healthcheck commands, conditional
dependencies, profiles, resource reservations, labels, configs/secrets file
mounts, and external volumes. Expand YAML anchors/aliases before importing.
Documents are bounded to 256 KiB, 20 total services and 32 levels of nesting.

## Explicit Hakopod settings

The optional `x-hakopod` service section accepts `port`, `public`, `size`,
`secrets`, `healthcheck`, `public_tcp`, `readiness`, `fs_group`, and
`termination_grace_seconds`. These use the canonical configuration field names.
For example:

```yaml
name: my-app
services:
  web:
    image: nginxinc/nginx-unprivileged:alpine
    expose: [8080]
    x-hakopod:
      port: 8080
      public: true
      healthcheck: /
      size: small
  db:
    image: postgres:17
    expose: [5432]
    volumes: [database:/var/lib/postgresql/data]
    x-hakopod:
      secrets:
        POSTGRES_PASSWORD: {ref: database-password}
volumes:
  database:
    x-hakopod:
      size_gib: 10
      access_mode: ReadWriteOnce
```

Supply the referenced application secret before deployment. Passwords, tokens
and credential-bearing URLs cannot be saved as plain variables. An explicit
secret reference replaces any matching pasted environment value without retaining
that value in the generated configuration or warnings.

Review image filesystem permissions and resource sizes. Hakopod's security and
resource defaults apply; conversion does not reproduce arbitrary Docker host
permissions. Shared volumes, TCP provisioning and Cloud plan restrictions still
pass through the same deployment policy checks as handwritten TOML.

## API

`POST /api/v1/compose/convert` accepts `project`, `environment`, `yaml`, an optional
`name`, an optional string map of `variables`, and an optional map of filenames
to literal file contents in `env_files`. To add services, also supply
`application_id` and its `expected_revision`. The response contains the generated
`toml`, normalized `spec`, `warnings` and base revision. Use `/plan` and
`/deployments` to review and apply the draft. Conversion requires deployment
write permission in the selected application scope.
