# Certificates for managed platforms

The guided creation flow uses `tls_mode = "managed"`. Hakopod creates the certificates after it has claimed the platform's namespace. You do not need to learn a generated namespace name, make a certificate for it, or restart the API before applying your review.

This describes the managed certificate implementation. A platform remains unavailable until its pinned images and native acceptance evidence pass the release checks. Certificate support alone does not qualify Neon or Supabase for release.

## What you supply

You still choose immutable secret references for database passwords, application keys, object storage credentials and runtime configuration. Managed TLS does not create or replace those credentials. Supabase's Envoy configuration must still preserve the supported authentication rules, upstream routes and TLS listener.

For Supabase, omit `database-tls-certificate` and `gateway-tls-certificate` from `secrets`. For Neon, omit `broker-auth`; its remaining authentication snapshots contain credentials and configuration without `tls.crt`, `tls.key`, `ca.crt` or `ca.key`. The catalog reports the secret references needed by the guided flow.

```toml
schema_version = 1
name = "analytics"
kind = "supabase"
tls_mode = "managed"
```

This is a fragment. Use the catalog's complete specification for the selected platform version, resources, storage and placement.

Existing specifications without `tls_mode`, and specifications with `tls_mode = "operator"`, retain operator-supplied certificates. Those certificates must cover the exact internal endpoints and meet the same runtime validation. Their renewal remains the operator's responsibility. Changing certificate ownership requires a separate platform and a reviewed connection migration.

## Where the keys live

Every platform receives a separate certificate authority. Its private signing key stays in the `platform-tls-issuer` Secret in that platform's namespace. Application pods do not mount that Secret. They receive a public CA certificate and a server certificate with its separate private key.

Supabase's database and gateway use separate leaf keys. Neon uses separate leaf keys for its broker, controller, controller database, proxy, compute group, pageserver group and safekeeper group. Each certificate covers the short Service name and the full name in the platform's namespace. Indexed groups include their configured members. Supabase's gateway certificate also covers the configured public hostname.

The issuer is bound to the namespace UID and the durable resource claim. A Secret with the right name but a different owner cannot be adopted. If a claimed issuer disappears, reconciliation stops instead of silently replacing the trust root.

Generated runtime Secrets are immutable. Their names include a hash of their contents. The operation snapshot retains the reviewed credential references; generated certificates are resolved under the operation lease. The CA private key is never included in the operation snapshot, API response, log or pod environment.

## Renewal and proof that it worked

Leaf certificates last 30 days. Hakopod replaces them when fewer than seven days remain. It retains the issuer's signing key when renewing the one-year CA certificate, starting 30 days before expiry. Keeping the same issuer identity allows clients using the previous, still-valid CA certificate to verify new leaves during that overlap. Clients should refresh their downloaded CA certificate before its expiry.

A renewal creates a new immutable Secret and changes the workload's reference. Kubernetes rolls the affected workload. Hakopod then opens a bounded connection to each owned TLS listener, verifies its hostname and CA, and compares the certificate actually served with the new snapshot. PostgreSQL listeners must first accept the PostgreSQL TLS negotiation request. An old certificate, a failed handshake or an unready member keeps the operation pending. Old snapshots are pruned only after the new certificates are observed.

Certificate maintenance uses a separate durable lease. It keeps the reviewed platform revision and credentials. It does not create another Neon tenant, change proxy authorization or replay a Supabase database password migration. Maintenance must wait for lifecycle and recovery work, and it rechecks the platform's capacity and runtime qualification. Restarting the control plane resumes from owned Secrets and PostgreSQL claims.

A restored Supabase target keeps its gateway and application services stopped for inspection. Certificate maintenance preserves that isolation. Its Security tab explains that maintenance is paused until you review and apply a new platform revision. The source platform continues its own maintenance schedule.

The platform observation records the issuer fingerprint, each verified endpoint's leaf fingerprint and expiry, and the verification time. These are public certificate facts; they do not expose key material.

## Connecting clients and restoring data

Download the platform's public CA certificate from its trust endpoint and configure clients to verify the server hostname. Managed certificates are issued by the platform's own CA; they are not automatically trusted by browsers or a computer's system certificate store. A public hostname in a certificate does not by itself publish the database or make that CA publicly trusted.

In the dashboard, open the platform's **Security** tab and choose **Download public CA**. The same public certificate is available through `GET /api/v1/managed-platforms/{id}/trust`, `hakopod platform trust PLATFORM_ID`, and the SDK's `client.managedPlatform(id).trust()`. The CLI returns JSON; the PEM certificate is in `certificate_pem`. A scoped reader can access it. Missing ownership or an issuer that has not been verified makes the endpoint unavailable.

A recovery target has its own namespace and issuer. Restoring data does not copy the source's TLS private keys or make the target impersonate the source. Application credentials that must remain compatible with the restored data are checked separately. Neon recovery reads the target's currently claimed certificate snapshots and refuses a missing, changed or unmaintained identity.
