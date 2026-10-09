# Application database TLS profiles

Hakopod templates can declare a versioned database client profile for a
connection variable. The profile remains part of the service specification when
the managed connection flow replaces a saved environment value or secret. A
profile activates only for a managed database binding. Saved, bundled and
external connections can continue to declare their own trust environment.

The Infisical `v0.165.10` profile supplies PostgreSQL's public CA through
`DB_ROOT_CERT` and supplies one additive Node.js CA bundle for all profiled Redis
connections. The GlitchTip `v6.1.0` profile supports the verified PostgreSQL URL
and configures its pinned Rust Valkey client to load the image's system bundle
and Hakopod's mounted private CAs together. These profiles do not disable
hostname or certificate verification.

Source inspection used these pinned revisions:

- Infisical `222cd2d37f96c2d8678f36f0523af0514b43cb8b`: `backend/src/db/instance.ts` and `backend/src/lib/config/redis.ts`
- GlitchTip `dcef83161b881a14010cd44fd4b33f67134e92dd`: `glitchtip/settings.py`
- django-vcache `d7486659f10b00aa6fa8dcc1e53ee0f99043c171`: `src/connection.rs` and `Cargo.toml`
- redis-rs `d408f2a8efdafa5582bfb6f8bbd8750d22846ec9`: `redis/src/connection.rs`
- rustls-native-certs `71e4dd82ad584ac93c41a5ec694f255530da97d3` (`0.8.3`): `src/lib.rs`

The source contracts and Hakopod rendering logic are covered by repository
tests. Driver-level acceptance against the pinned Infisical and GlitchTip images
verified the correct CA and rejected a wrong CA. Both pinned applications also
completed migrations and reached their health endpoint with TLS-enabled
PostgreSQL and Redis fixtures. These disposable checks covered the application
startup and trust configuration; they did not repeat database operator lifecycle
qualification.
