-- Write-only connector secrets (Tailscale auth key, OpenVPN profile, IPsec
-- pre-shared key), sealed with the installation's authentication key, and what
-- the host reports about the connector it runs.
ALTER TABLE installation_access_policy ADD COLUMN secrets bytea;
ALTER TABLE installation_access_vpn ADD COLUMN connector jsonb NOT NULL DEFAULT '{}';
