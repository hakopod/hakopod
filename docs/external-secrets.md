# External secret providers

Hakopod supports Vault/OpenBao KV v2 and Infisical alongside native application
secrets. Provider integration is available in self-hosted installations without a
paid license. AWS Secrets Manager, AWS Parameter Store, Doppler, Azure Key Vault,
Scaleway and Phase are not implemented.

An installation administrator configures providers through `/api/v1/secret-providers`.
The service Secrets tab displays external references; provider setup currently uses
the API. Applications keep only provider names, relative paths and keys in strict
version-1 TOML. Credentials and resolved values never enter application revisions,
plans, audit metadata or API responses.

```toml
schema_version = 1
name = "shop"

[services.web]
image = "nginx:alpine"
port = 80

[services.web.secrets]
DATABASE_URL = { provider = "production-vault", path = "shop", key = "DATABASE_URL" }
SESSION_SECRET = { ref = "native-session-secret" }
```

`ref` selects an existing native application secret. `provider`, `path` and `key`
select an external value. These forms cannot be mixed. Paths are relative to the
administrator's root path and cannot contain traversal or URL escapes. Omit `path`
to select the configured root. Vault values must be strings; `key` is the KV field.
For Infisical, `path` is a folder and `key` is the shared secret name. Imported secrets
and interpolated secret references are not expanded.

## Provider setup

Configure the persistent authentication encryption key before creating providers.
Hakopod encrypts credentials with AES-256-GCM, bound to provider name and kind.
Preserve this key alongside management database backups. Provider credentials are
write-only; omitting `credentials` on update retains the current encrypted record.

An authenticated administrator can PUT this JSON to
`/api/v1/secret-providers/production-vault`:

```json
{
  "kind": "vault",
  "endpoint": "https://vault.example.com",
  "mount": "secret",
  "root_path": "applications",
  "private_cidrs": [],
  "scopes": [{"project": "shop", "environments": ["production"]}],
  "expected_revision": 0,
  "credentials": {"token": "REPLACE_WITH_RESTRICTED_VAULT_TOKEN"}
}
```

The example TOML then reads `secret/data/applications/shop` and selects `DATABASE_URL`.
An optional `namespace` selects a Vault namespace. Use a Vault token whose policy
grants only read access to the intended KV v2 subtree. The integration does not renew
Vault tokens; rotate the provider credentials before they expire.

For Infisical, use `kind: "infisical"`, omit `mount`, and supply `project_id`,
`environment`, and `credentials: {"client_id": "…", "client_secret": "…"}`.
Use the regional or self-hosted HTTPS origin for `endpoint`. The machine identity
must have read access only to the intended project, environment and folder subtree.
Hakopod obtains a fresh universal-auth access token for each deployment snapshot.

Only installation administrators can set endpoints, credentials and access scopes.
An empty environment list grants all environments in its selected project. No
selected project grants no access; at least one project is required. Deployment users
can discover only names and kinds of providers granted to their authorized scope.
Granting a provider makes every permitted key below its root available to those
projects: create separate providers and upstream identities for separate trust scopes.

HTTPS certificate verification is always enabled. An optional `ca_cert` PEM bundle
supports operator certificate authorities. Private endpoints require an explicit
`private_cidrs` allowlist, such as `10.20.0.0/24`. Loopback, link-local, multicast,
cloud metadata and shared carrier addresses are denied. DNS results are checked and
the checked IP is dialed directly. Redirects and environment HTTP proxies are disabled.
Endpoint, root, upstream project, namespace, CA and network trust are immutable;
create a separate provider to change them. Scope and credential updates require the
current `expected_revision`; stale writes return 409.

## Refresh and failure behavior

Every deployment, including the existing service Restart action, reads fresh external
values before Kubernetes writes. Restart the affected service to load a rotated value;
the action queues an auditable deployment with a new revision and restart nonce.
There is no background polling or provider-value preview endpoint.

Hakopod resolves the complete application snapshot before applying any resources.
A denied, missing, timed-out or malformed provider response fails the deployment and
leaves existing workload secrets unchanged. It never substitutes an empty value for
an unavailable value. Explicit empty string values are valid. Successful snapshots
are written to owned service Kubernetes Secrets; workloads receive secret-key
references and never receive provider credentials. Individual Kubernetes writes are
not a cross-resource transaction.

Limits: 64 providers per installation, 32 project scopes per provider, four concurrent
snapshots, 60 seconds per snapshot, 10 seconds per request, 1 MiB per response,
64 KiB per value, 512 KiB per service, and 2 MiB of external values per application
snapshot. Existing native-secret bounds remain unchanged.

## Upstream contracts

- [Vault KV v2 read API](https://developer.hashicorp.com/vault/api-docs/secret/kv/kv-v2)
- [OpenBao KV v2 read API](https://openbao.org/docs/api/secret/kv/kv-v2/)
- [Infisical universal auth login](https://infisical.com/docs/api-reference/endpoints/universal-auth/login)
- [Infisical v4 read secret](https://infisical.com/docs/api-reference/endpoints/secrets/read)

HTTP protocol, failure and network policy behavior is covered by local mock servers;
real upstream account credentials are not part of the repository tests.
