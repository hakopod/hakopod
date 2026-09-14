ALTER TABLE git_connections ADD COLUMN oauth_client_id text NOT NULL DEFAULT '';
ALTER TABLE git_connections ADD COLUMN credential_generation bigint NOT NULL DEFAULT 1;
ALTER TABLE git_connections ADD COLUMN refresh_state text NOT NULL DEFAULT 'ready' CHECK (refresh_state IN ('ready','refreshing','reauthorize'));
CREATE UNIQUE INDEX git_oauth_subject ON git_connections(oauth_client_id,subject_id) WHERE auth_kind='gitlab_oauth' AND subject_id>0;
