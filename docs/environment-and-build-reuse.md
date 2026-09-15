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

All runtime variable editors accept `.env` files and pasted `.env` text. They
support quoted values, empty values, `export`, multiline strings and comments.
They never execute shell commands or expand host variables. Duplicate keys and
invalid input are rejected without discarding the draft. Password/token names
and credential URLs are saved as new application secret references at review;
only references enter the TOML or build configuration. Partially failed uploads
retain the draft and reuse its newly created references on retry. Uploaded but
unbound references remain visible in Application secrets for cleanup.

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
