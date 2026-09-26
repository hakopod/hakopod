-- A DNS provider credential creates the ownership and routing records a user
-- would otherwise enter by hand at their provider. Scoping copies
-- git_connections exactly: a row is installation-wide, or it belongs to one
-- project and one environment, never to a project alone.
CREATE TABLE dns_providers (
 id text PRIMARY KEY,
 name text NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
 kind text NOT NULL CHECK (kind IN ('cloudflare')),
 project text NOT NULL DEFAULT '',
 environment text NOT NULL DEFAULT '',
 revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
 enabled boolean NOT NULL DEFAULT true,
 credentials bytea NOT NULL CHECK (length(credentials) BETWEEN 28 AND 8192),
 -- The DNS zones this credential may write to. hakopod cannot see what a
 -- provider token actually reaches, so this list, not the token, is the
 -- enforceable boundary. Bounded by item count and by total length.
 zone_filter text[] NOT NULL CHECK (cardinality(zone_filter) BETWEEN 1 AND 32 AND length(array_to_string(zone_filter,',')) BETWEEN 1 AND 2048),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 CONSTRAINT dns_provider_scope CHECK ((project = '') = (environment = ''))
);
CREATE UNIQUE INDEX dns_providers_name ON dns_providers(project,environment,lower(name));
CREATE INDEX dns_providers_scope ON dns_providers(project,environment);
