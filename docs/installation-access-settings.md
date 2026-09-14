# Installation access settings

Free self-hosted installations can create one team. Additional teams require the `multi_team` entitlement on a valid signed license. The database serializes team creation, so simultaneous requests cannot exceed the free limit. Existing teams, memberships and project access remain available after expiry or downgrade; new teams are blocked. Managed Cloud keeps its operator-controlled team limits.

Self-hosted administrators can configure Google, GitHub and GitLab login with `oauth_login`, or an OpenID Connect provider with `enterprise_sso`. Settings use encrypted database storage and optimistic revisions. Responses report whether a secret exists and never return it. An empty secret retains the saved value; clearing it is explicit. Saving settings takes precedence over operator configuration for that provider.

Provider login checks the current license at initiation and callback. An expired license hides the provider from login and blocks new provider sessions, but does not revoke existing sessions or disable password login, password recovery, passkeys or administrator recovery. Administrators can inspect and disable providers without a paid entitlement. Keep a password or passkey on the recovery administrator account.

OpenID Connect checks the signed ID token, issuer, audience, expiry, nonce and verified email. Authorization uses state, a browser-bound cookie and PKCE. Configuration changes invalidate an in-progress login. New accounts still need an invitation on self-hosted installations. This implements OpenID Connect SSO, not SAML, SCIM provisioning or enforced organization-wide SSO.

Operator TOML can configure the same provider before any dashboard override:

```toml
schema_version = 1

[oauth.oidc]
client_id = "hakopod"
client_secret_file = "/etc/hakopod/secrets/oidc-client-secret"
issuer_url = "https://identity.example.com/realms/company"
```

Register the callback shown in Settings with the identity provider. The callback is the public dashboard origin followed by `/api/v1/auth/oauth/oidc/callback`. The issuer must use HTTPS. Client secrets belong in restricted files or encrypted Settings, never source control.

Managed Cloud does not expose installation login-provider settings to customers, including administrator accounts. Its provider configuration remains under operator control; self-hosted paid entitlements do not disable Cloud's operator-managed login.
