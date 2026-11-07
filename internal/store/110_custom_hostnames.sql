-- Tenant hostnames a multi-tenant service attaches at runtime. They never enter
-- the application spec, so adding one needs no new revision. The hostname is
-- unique across the installation; application_domains is checked in code.
CREATE TABLE custom_hostnames (
  id text PRIMARY KEY,
  application_id text NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
  service text NOT NULL,
  hostname text NOT NULL UNIQUE CHECK (char_length(hostname) BETWEEN 4 AND 253),
  status text NOT NULL DEFAULT 'pending_dns' CHECK (status IN ('pending_dns','issuing','active','failed','deleting')),
  status_reason text NOT NULL DEFAULT '' CHECK (char_length(status_reason) <= 500),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  verified_at timestamptz,
  active_at timestamptz,
  last_checked_at timestamptz,
  next_check_at timestamptz NOT NULL DEFAULT now(),
  attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0)
);
CREATE INDEX custom_hostnames_service ON custom_hostnames (application_id, service, hostname);
CREATE INDEX custom_hostnames_due ON custom_hostnames (next_check_at) WHERE status IN ('pending_dns','issuing','deleting');

INSERT INTO installation_settings(name,value) VALUES ('custom_hostnames','{"max_per_service":10000}') ON CONFLICT (name) DO NOTHING;
