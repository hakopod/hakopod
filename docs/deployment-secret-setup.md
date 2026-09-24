# Missing secrets during deployment

Configuration plans now return `required_secrets` and `missing_secrets` for local
application references. This includes inherited defaults, service variables,
mounted secret files, job variables and connection-binding passwords. Names are
sorted and deduplicated. A reference overridden by a service does not need an
unused default value.

The dashboard's TOML, Compose, Git import, Git synchronization and built-image
review flows let a deployment user enter a missing value or explicitly generate
a random secret before submission. The catalog keeps its format-aware helpers.
Values go directly to scoped private secret storage. They are never put in TOML,
plan responses, immutable deployment history or CLI state files. Failed writes
preserve the current draft; moving to another reference clears it.

The native CLI and npm CLI prompt in interactive terminals. Noninteractive use
(including npm `--yes`) stops with the missing names; it does not invent values.
Supply CI credentials beforehand through the dashboard or API.

`POST /api/v1/secrets/{name}?project=...&environment=...&application=...` creates
one value using `{"value":"..."}` or explicit `{"generate":true}`. Generation
uses 32 cryptographically random bytes, encoded as URL-safe unpadded Base64, or
64 hexadecimal characters with `"format":"hex"`. Generation does not create
provider-issued tokens, certificates or credentials for existing databases.
Check an application's documented format before choosing generation.

Creation never overwrites an existing value: retry or concurrent creation returns
`409 secret_exists`. Recheck using `POST /api/v1/secrets/requirements` with
`project`, `environment` and the canonical `spec`. Existing secret rotation stays
an explicit operation through the Secrets page/PUT endpoint. Only metadata is
returned. Cloud Team approval policy still applies to secret creation.

Every acceptance path in the shared runtime checks local references again,
including automated source/build deployment and rollback. Reconciliation also
reads the full secret snapshot before changing workloads, because a value can be
removed after a plan. A Kubernetes failure is an unavailable backend, never a
false assertion that the secret is absent. External-provider references continue
to resolve through their configured provider and cannot be replaced by generating
an unrelated local value.

Validation also exercises the native and npm executables through real pseudo-terminals
against a loopback fixture API. Hidden entry, explicit generation and cancellation
passed; the entered value did not appear in terminal output or deployment payloads.
The fixture uses synthetic credentials and does not claim a production deployment.
