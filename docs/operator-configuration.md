# Operator configuration

The management server can read its startup settings from a TOML file. Set
`HAKOPOD_CONFIG_FILE=/etc/hakopod/server.toml` in the service environment and
restart `hakopod-server`. The file is optional; existing environment-only
installations keep working.

Start with [the commented example](../examples/hakopod-server.toml). Every file
needs `schema_version = 1`. Files larger than 64 KiB, unknown settings, incorrect
types and plaintext credential fields are rejected before any file setting is
applied. Error messages do not echo file values. The file is read once at startup;
there is no watcher or background configuration process.

Existing environment variables take precedence, including explicitly empty
variables. For credentials, either the value variable or its `_FILE` companion
overrides the TOML reference. Supplying both nonempty credential variables remains
an error. Unused file references overridden by the environment are not opened.
Relative file references are resolved beside the TOML file, without shell or
environment expansion.

Credentials belong in the process environment or separate files. Database URLs,
setup proofs, MFA encryption keys, OAuth client secrets and SMTP passwords use
restricted regular files of at most 4 KiB; only the owner may read or write them.
Use mode `0600` or `0400`. Kubeconfig and TLS private-key references have the same
permission rule with a 64 KiB limit. Public TLS certificate files may be readable
by other users and are also limited to 64 KiB. The TOML stores only file paths.

The available settings map to the same environment options used by the server:

| TOML setting | Environment option |
| --- | --- |
| `server.listen` | `HAKOPOD_LISTEN` |
| `server.web_origin` | `HAKOPOD_WEB_ORIGIN` |
| `server.database_url_file` | `HAKOPOD_DATABASE_URL_FILE` |
| `server.kubeconfig_file` | `HAKOPOD_KUBECONFIG` |
| `server.app_domain` | `HAKOPOD_APP_DOMAIN` |
| `server.ingress_class` | `HAKOPOD_INGRESS_CLASS` |
| `server.rollout_timeout` | `HAKOPOD_ROLLOUT_TIMEOUT` |
| `server.public_port` | `HAKOPOD_PUBLIC_PORT` |
| `server.public_https_port` | `HAKOPOD_PUBLIC_HTTPS_PORT` |
| `server.tls_issuer` | `HAKOPOD_TLS_ISSUER` |
| `server.tls_cert_file` | `HAKOPOD_TLS_CERT` |
| `server.tls_key_file` | `HAKOPOD_TLS_KEY` |
| `server.trust_proxy` | `HAKOPOD_TRUST_PROXY` |
| `server.k3s_supervisor_url` | `HAKOPOD_K3S_SUPERVISOR_URL` |
| `server.haproxy_namespace` | `HAKOPOD_HAPROXY_NAMESPACE` |
| `server.haproxy_configmap` | `HAKOPOD_HAPROXY_CONFIGMAP` |
| `server.haproxy_release` | `HAKOPOD_HAPROXY_RELEASE` |
| `auth.signup_enabled` | `HAKOPOD_SIGNUP_ENABLED` |
| `auth.setup_secret_file` | `HAKOPOD_SETUP_SECRET_FILE` |
| `auth.encryption_key_file` | `HAKOPOD_AUTH_ENCRYPTION_KEY_FILE` |
| `oauth.github.client_id`, `client_secret_file` | `HAKOPOD_GITHUB_CLIENT_ID`, `HAKOPOD_GITHUB_CLIENT_SECRET_FILE` |
| `oauth.gitlab.client_id`, `client_secret_file` | `HAKOPOD_GITLAB_CLIENT_ID`, `HAKOPOD_GITLAB_CLIENT_SECRET_FILE` |
| `oauth.google.client_id`, `client_secret_file` | `HAKOPOD_GOOGLE_CLIENT_ID`, `HAKOPOD_GOOGLE_CLIENT_SECRET_FILE` |
| `smtp.enabled`, `address`, `from` | `HAKOPOD_SMTP_ENABLED`, `HAKOPOD_SMTP_ADDRESS`, `HAKOPOD_SMTP_FROM` |
| `smtp.username`, `password_file` | `HAKOPOD_SMTP_USERNAME`, `HAKOPOD_SMTP_PASSWORD_FILE` |
| `backups.pg_dump_path` | `HAKOPOD_PG_DUMP_PATH` |
| `backups.state_dir` | `HAKOPOD_BACKUP_STATE_DIR` |
| `backups.managed_postgres` | `HAKOPOD_MANAGED_POSTGRES` |

Production dashboard origins require HTTPS. A non-loopback API listener requires
TLS files or explicit trust in an HTTPS reverse proxy. Public ports are 1–65535;
rollout timeouts are 15 seconds through 15 minutes. Omit a setting to keep its
existing default. Public signup and email delivery remain off by default.

Application networks, mounts, security settings and deployment revisions belong
in each application’s `hakopod.toml`. This operator file configures the server
process; it does not import user accounts, grant roles, activate licenses or
replace persisted application state. Dashboard changes to those resources keep
their normal authorization, revision and audit checks.

Public TCP ports are disabled by default. Set `[server] public_tcp_ports = [587]`
or `HAKOPOD_PUBLIC_TCP_PORTS=587` only after provisioning matching ports on the
owned HAProxy ingress. The installer JSON uses `public_tcp_ports: [587]`.
See [SMTP migration](smtp-migration.md) for firewall, certificate and AWS setup.
