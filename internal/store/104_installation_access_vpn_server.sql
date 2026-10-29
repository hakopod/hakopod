-- Built-in WireGuard server for management access. Each peer is one person's
-- device: the browser generates its key pair and only the public key is
-- stored. Addresses are unique among live peers; a revoked peer keeps its row
-- for the audit trail and frees its address.
CREATE TABLE installation_access_vpn_peers (
 id text PRIMARY KEY,
 identity_id text NOT NULL,
 name text NOT NULL,
 public_key text NOT NULL,
 address text NOT NULL,
 latest_handshake timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 revoked_at timestamptz
);
CREATE UNIQUE INDEX installation_access_vpn_peers_live_key ON installation_access_vpn_peers(public_key) WHERE revoked_at IS NULL;
CREATE UNIQUE INDEX installation_access_vpn_peers_live_address ON installation_access_vpn_peers(address) WHERE revoked_at IS NULL;
ALTER TABLE installation_access_vpn
 ADD COLUMN server_public_key text NOT NULL DEFAULT '',
 ADD COLUMN server_error text NOT NULL DEFAULT '';
