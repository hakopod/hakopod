# Shared Hakopod authentication

This package exposes the existing engine authentication handlers and PostgreSQL store to the private Cloud binary. It does not implement another password, OAuth, MFA, passkey or session system. The engine command also uses its shared configuration parser.

Cloud embeds this package from its pinned `engine` submodule. Use a dedicated Cloud PostgreSQL database, separate from every connected workload installation. `Open` runs the canonical engine migrations but starts no reconciler, Kubernetes client, demo workload or background worker. The same pool can hold Cloud-specific tables prefixed `cloud_`.

Only the explicit identity route allowlist is exposed. Human browser sessions are verified on each request, including expiry, revocation and disabled-account checks. Machine and CLI credentials cannot authenticate to Cloud. The caller must enforce verified email and product-level membership/plan policies; the engine's administrator flag grants no Cloud workspace access.

Provider OAuth credentials remain operator configuration. `ConfigFrom` accepts the existing HAKOPOD settings and a build-controlled signup capability. Public engine builds continue passing their existing self-hosted signup policy; the private Cloud caller can enable its existing registration routes explicitly.
