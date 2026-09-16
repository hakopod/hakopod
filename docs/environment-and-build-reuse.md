# Shared variables, secrets and source images

Save ordinary application defaults once and opt in to passing them to every
service with top-level `inject_env = true`. It is false when omitted. A service's
own variable wins, including an explicitly empty value. This works for web
services, workers, deployment jobs and scheduled jobs.

```toml
schema_version = 1
name = "example"
inject_env = true

[env]
APP_MODE = "production"
REGION = "eu"

[secrets.SHARED_TOKEN]
ref = "shared-token"

[services.web]
image = "python:3.13-alpine"

[services.web.env]
REGION = ""

[services.worker]
image = "python:3.13-alpine"

[services.worker.secrets.DATABASE_URL]
ref = "database-url"
```

Save `shared-token` and `database-url` under Application secrets first. Stored
secret values are application-scoped and write-only. References under top-level
`[secrets]` explicitly bind shared defaults; references under a service bind only
that service. Service variables, secret references and generated bindings take
precedence over shared defaults. Saving a secret does not automatically expose
it to every service. Replacing a secret value requires restarting or redeploying
its consumers.

Application > Environment edits the shared defaults and injection switch.
Service > Environment edits overrides and displays inherited names. The service
Environment and Secrets tabs show the effective values/references. Changes use
a reviewed deployment and optimistic revision checks.

Service creation, multi-service application creation, Git build runtime settings
and existing environment editors have separate **Environment variables** and
**Secrets** sections. Use **Add secret** for private values with any variable
name, or **Store as secret** to protect an existing draft variable. New secret
values are masked; **Show value** allows editing, including multiline keys.

All runtime variable editors accept `.env` files and pasted `.env` text. They
support quoted values, empty values, `export`, multiline strings and comments.
They never execute shell commands or expand host variables. Duplicate keys and
invalid input are rejected without discarding the draft. Successful paste/file
imports separate detected secrets and clear the raw paste box. Password/token
names, common credential token formats, private keys and credential URLs are
saved as new application secret references at review or when generating TOML;
only references enter the TOML or build configuration. Partially failed uploads
retain the draft and reuse its newly created references on retry. Uploaded but
unbound references remain visible in Application secrets for cleanup.

## Import environment files

Use an import-time `env_file` directive to avoid repeating values in TOML:

```toml
schema_version = 1
name = "example"
env_file = [".env", "production.env"]

[env]
APP_MODE = "production"

[services.web]
image = "python:3.13-alpine"

[services.worker]
image = "python:3.13-alpine"
env_file = "worker.env"

[services.worker.env]
CONCURRENCY = "2"
```

A top-level file automatically enables `inject_env`. Do not combine it with
`inject_env = false`. A service-level file applies only to that service. Later
files override earlier ones; explicit `env` and `secrets` at that level override
file values. Service values still override application defaults.

The CLI reads filenames relative to `config.toml` when validating, planning or
deploying. Git configuration imports read files relative to the configuration
at the exact reviewed commit. Keep private `.env` files out of Git: attach or
paste them in the dashboard's TOML/Compose importer instead. Enter the relative
filename used by the configuration. Missing files fail clearly; server host
files are never read. `env_file` accepts one filename or a list, including
subdirectories inside the configuration directory. Absolute paths and parent
traversal are rejected. Imports allow up to 8 files and 128 KiB combined.

Imported files are literal UTF-8 assignments, with up to 128 variables per file
and 4 KiB per ordinary value or 64 KiB per secret. Interpolation and shell commands are never evaluated. The
importer removes `env_file` from the canonical deployment and stores ordinary
values and scoped secret references. Sensitive-name and credential-URL detection
is a safeguard; use explicit secret references for other confidential values.
Changing a file value requires another reviewed deployment. The runtime does
not watch the original file. Secrets staged during a failed review may be
removed from Application secrets when no workload references them. Retrying the
same import reuses its references; a changed secret gets a new reference so its
consumers roll out with the new value. Importing secrets requires deployment
write permission, configured auth encryption and Kubernetes secret storage.

API callers send `env_files: {".env": "APP_MODE=production"}` alongside TOML
containing `env_file = ".env"` to `/plan` or `/deployments`. JSON `spec` requests
use already expanded values and references and do not accept `env_files`.

For private images, Hakopod tries a selected credential first, then anonymous
access, then matching saved credentials in deterministic order. The lookup uses
only the exact project, environment and canonical registry host. Docker Hub
aliases map to the same host. Credentials never cross registry hosts. The
successful credential follows the resolved digest into Kubernetes pull secrets.
Network, TLS, rate-limit and server failures stop retries instead of trying
unrelated credentials.

A source build can list `reuse_services` in its saved build configuration.
Choose additional existing services under **Reuse this image** in the build
editor. Every selected service receives the same verified digest and pull
credential, preserving its own command, arguments, variables, ports and volumes.
For example, Django, Celery and a migration job can share one image.
Runtime-only edits can reuse a previous successful run after reviewing the new
application plan. Changing the repository, connection, branch, architecture,
Dockerfile, build inputs or registry destination requires a new verified build.
Install the updated workflow before starting subsequent builds.

Cloud pod logs use the shared explorer and live tail for every service kind.
Queries retain the same pod ownership and workspace authorization checks. They
read recent Kubernetes-retained output, including available previous-container
logs; they do not create a permanent log archive.
