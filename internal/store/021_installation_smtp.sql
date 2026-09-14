CREATE TABLE installation_smtp (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    revision bigint NOT NULL CHECK (revision > 0),
    enabled boolean NOT NULL,
    host text NOT NULL CHECK (length(host) <= 253),
    port integer NOT NULL CHECK (port BETWEEN 1 AND 65535),
    security text NOT NULL CHECK (security IN ('starttls', 'tls')),
    username text NOT NULL CHECK (length(username) <= 256),
    from_email text NOT NULL CHECK (length(from_email) <= 254),
    password bytea NOT NULL CHECK (octet_length(password) = 0 OR octet_length(password) BETWEEN 28 AND 32768),
    updated_at timestamptz NOT NULL DEFAULT now()
);
