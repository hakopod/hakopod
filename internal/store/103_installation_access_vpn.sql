-- What the host reports about the management VPN connector: the server's
-- public key (its private key never leaves the host) and the last handshake.
CREATE TABLE installation_access_vpn (
 singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
 public_key text NOT NULL DEFAULT '',
 latest_handshake timestamptz,
 rx_bytes bigint NOT NULL DEFAULT 0,
 tx_bytes bigint NOT NULL DEFAULT 0,
 error text NOT NULL DEFAULT '',
 reported_at timestamptz NOT NULL DEFAULT now()
);
