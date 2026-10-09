# Application database TLS profiles

Hakopod templates can declare a versioned database client profile for a
connection variable. The profile remains part of the service specification when
the managed connection flow replaces a saved environment value or secret.

The Infisical `v0.165.10` profile supplies PostgreSQL's public CA through
`DB_ROOT_CERT` and supplies one additive Node.js CA bundle for all profiled Redis
connections. The GlitchTip `v6.1.0` profile supports the verified PostgreSQL URL.
Hakopod rejects managed Redis for this GlitchTip version because its Valkey
client cannot load Hakopod's private CA. These profiles do not disable hostname
or certificate verification.

Source inspection used these pinned revisions:

- Infisical `222cd2d37f96c2d8678f36f0523af0514b43cb8b`: `backend/src/db/instance.ts` and `backend/src/lib/config/redis.ts`
- GlitchTip `dcef83161b881a14010cd44fd4b33f67134e92dd`: `glitchtip/settings.py`
- django-vcache `d7486659f10b00aa6fa8dcc1e53ee0f99043c171`: `src/connection.rs` and `Cargo.toml`

The source contracts and Hakopod rendering logic are covered by repository
tests. Runtime acceptance remains pending.
