ALTER TABLE identities ADD COLUMN avatar_style text NOT NULL DEFAULT 'initials' CHECK (avatar_style IN ('initials','identicon','glass'));
ALTER TABLE identities ADD COLUMN avatar_seed text NOT NULL DEFAULT '';
ALTER TABLE identities ADD COLUMN profile_revision bigint NOT NULL DEFAULT 1;
ALTER TABLE team_members ADD COLUMN username text NOT NULL DEFAULT '';
CREATE UNIQUE INDEX team_username_unique ON team_members(team_id,lower(username)) WHERE username <> '';
CREATE TABLE host_access (
 identity_id text NOT NULL REFERENCES identities(id) ON DELETE CASCADE,
 node text NOT NULL,
 permission text NOT NULL CHECK(permission='nodes:terminal'),
 expires_at timestamptz NOT NULL,
 granted_by text NOT NULL REFERENCES identities(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(identity_id,node,permission)
);
INSERT INTO schema_migrations(version) VALUES(9);
