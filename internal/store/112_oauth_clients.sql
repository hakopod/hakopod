-- MCP clients register themselves (RFC 7591) and sign in through the device
-- consent flow. Only public clients exist: no secret is ever stored.
CREATE TABLE oauth_clients(id text PRIMARY KEY,client_name text NOT NULL,redirect_uris text[] NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),last_used_at timestamptz NOT NULL DEFAULT now());
-- A device code started by an OAuth authorization is redeemed only by its
-- client, at its redirect URI, with the PKCE verifier. Empty for CLI codes.
ALTER TABLE device_codes ADD COLUMN oauth_client_id text NOT NULL DEFAULT '';
ALTER TABLE device_codes ADD COLUMN oauth_redirect_uri text NOT NULL DEFAULT '';
ALTER TABLE device_codes ADD COLUMN oauth_code_challenge text NOT NULL DEFAULT '';
