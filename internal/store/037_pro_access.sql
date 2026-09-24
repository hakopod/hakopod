CREATE TABLE custom_roles (
 id text PRIMARY KEY CHECK(id ~ '^custom:[a-f0-9]{32}$'),
 name text NOT NULL CHECK(length(name) BETWEEN 1 AND 80),
 permissions text[] NOT NULL,
 revision bigint NOT NULL DEFAULT 1,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX custom_roles_name ON custom_roles(lower(name));
ALTER TABLE project_members DROP CONSTRAINT project_members_role_check;
ALTER TABLE project_members ADD CHECK(role IN ('admin','developer','viewer') OR role ~ '^custom:[a-f0-9]{32}$');
ALTER TABLE project_teams DROP CONSTRAINT project_teams_role_check;
ALTER TABLE project_teams ADD CHECK(role IN ('admin','developer','viewer') OR role ~ '^custom:[a-f0-9]{32}$');
CREATE TABLE organization_security (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
 require_mfa boolean NOT NULL DEFAULT false,
 revision bigint NOT NULL DEFAULT 1
);
INSERT INTO organization_security(singleton) VALUES(true);
ALTER TABLE api_keys ADD COLUMN mfa_verified boolean NOT NULL DEFAULT false;
ALTER TABLE device_codes ADD COLUMN mfa_verified boolean NOT NULL DEFAULT false;
