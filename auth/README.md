# Shared Hakopod authentication

This package exposes the existing engine authentication handlers and PostgreSQL store to the private Cloud binary. It does not implement another password, OAuth, MFA, passkey or session system. The engine command also uses its shared configuration parser.

Cloud embeds this package from its pinned `engine` submodule. Use a dedicated Cloud PostgreSQL database, separate from every connected workload installation. `Open` runs the canonical engine migrations but starts no reconciler, Kubernetes client, demo workload or background worker. The same pool can hold Cloud-specific tables prefixed `cloud_`.

Only the explicit identity route allowlist is exposed. Human browser sessions are verified on each request, including expiry, revocation and disabled-account checks. Machine and CLI credentials cannot authenticate to Cloud. The caller must enforce verified email and product-level membership/plan policies; an ordinary administrator flag grants no Cloud workspace access. `User.Operator` is true only for a currently verified human installation owner with super-admin permissions; revocation is checked on every verification. The Cloud caller must still authorize its internal workspace separately.

Provider OAuth credentials remain operator configuration. `ConfigFrom` accepts the existing HAKOPOD settings and a build-controlled signup capability. Public engine builds continue passing their existing self-hosted signup policy; the private Cloud caller can enable its existing registration routes explicitly.

`StartRuntime` is optional and starts the same bounded API/worker lifecycle as the engine command. It requires a real managed-cloud Kubernetes connection with one registered node by default. Trusted embeddings may set `RuntimeConfig.NodeLimit` to two for a bounded private operator cluster; customer policy remains single-node. The returned management handler must be placed behind product authorization; never expose it directly to ordinary Cloud customers. Installation and host administration remain restricted in managed-cloud mode.
