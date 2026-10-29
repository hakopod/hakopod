-- The on-demand hakopod-vpn host agent reports its version on every tick, so
-- the dashboard can tell "not installed" from "installed but stale".
ALTER TABLE installation_access_vpn
 ADD COLUMN agent_version text NOT NULL DEFAULT '',
 ADD COLUMN agent_seen_at timestamptz;
