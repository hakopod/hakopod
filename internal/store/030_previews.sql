CREATE TABLE previews (
 id text PRIMARY KEY,
 parent_id text NOT NULL,
 application_id text UNIQUE REFERENCES applications(id) ON DELETE SET NULL,
 project text NOT NULL,
 environment text NOT NULL,
 name text NOT NULL,
 branch text NOT NULL DEFAULT '',
 state text NOT NULL DEFAULT 'active' CHECK(state IN ('active','deleting','deleted')),
 created_at timestamptz NOT NULL DEFAULT now(),
 expires_at timestamptz NOT NULL,
 deleted_at timestamptz,
 cleanup_error text NOT NULL DEFAULT '',
 cleanup_attempted_at timestamptz
);
CREATE UNIQUE INDEX previews_active_name ON previews(parent_id,name) WHERE state<>'deleted';
CREATE INDEX previews_expiry ON previews(expires_at) WHERE state<>'deleted';
CREATE INDEX previews_parent ON previews(parent_id,created_at DESC,id DESC);
INSERT INTO schema_migrations(version) VALUES(30);
