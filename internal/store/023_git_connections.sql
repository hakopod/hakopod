CREATE TABLE git_connections (
 id text PRIMARY KEY,
 name text NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
 provider text NOT NULL CHECK (provider IN ('github','gitlab')),
 auth_kind text NOT NULL CHECK (auth_kind IN ('token','github_app','gitlab_oauth')),
 revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
 enabled boolean NOT NULL DEFAULT true,
 account text NOT NULL DEFAULT '',
 subject_id bigint NOT NULL DEFAULT 0,
 github_app_id bigint NOT NULL DEFAULT 0,
 installation_id bigint NOT NULL DEFAULT 0,
 credentials bytea,
 legacy_secret_ref text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK ((credentials IS NOT NULL) <> (legacy_secret_ref <> '')),
 CHECK (auth_kind <> 'github_app' OR provider = 'github'),
 CHECK (auth_kind <> 'gitlab_oauth' OR provider = 'gitlab')
);
CREATE UNIQUE INDEX git_connection_installation ON git_connections(github_app_id,installation_id) WHERE auth_kind='github_app';
CREATE UNIQUE INDEX git_connections_name ON git_connections(lower(name));
INSERT INTO git_connections(id,name,provider,auth_kind,legacy_secret_ref) VALUES
 ('github-default','Default GitHub','github','token','github-connection'),
 ('gitlab-default','Default GitLab','gitlab','token','gitlab-connection');

ALTER TABLE application_sources ADD COLUMN connection_id text REFERENCES git_connections(id);
UPDATE application_sources SET connection_id=provider||'-default';
ALTER TABLE application_sources ALTER COLUMN connection_id SET NOT NULL;
CREATE FUNCTION default_source_connection() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.connection_id IS NULL THEN NEW.connection_id := NEW.provider||'-default'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER source_connection_default BEFORE INSERT ON application_sources FOR EACH ROW EXECUTE FUNCTION default_source_connection();
CREATE INDEX application_sources_connection_repo ON application_sources(connection_id,repository,branch) WHERE auto_deploy;

UPDATE build_configs SET config=config||jsonb_build_object('connection_id',COALESCE(NULLIF(config->>'provider',''),'github')||'-default');
UPDATE build_runs SET config=config||jsonb_build_object('connection_id',COALESCE(NULLIF(config->>'provider',''),'github')||'-default');
ALTER TABLE build_configs ADD COLUMN connection_id text GENERATED ALWAYS AS (COALESCE(NULLIF(config->>'connection_id',''),CASE WHEN config->>'provider'='gitlab' THEN 'gitlab-default' ELSE 'github-default' END)) STORED REFERENCES git_connections(id);
ALTER TABLE build_runs ADD COLUMN connection_id text GENERATED ALWAYS AS (COALESCE(NULLIF(config->>'connection_id',''),CASE WHEN config->>'provider'='gitlab' THEN 'gitlab-default' ELSE 'github-default' END)) STORED REFERENCES git_connections(id);
CREATE INDEX build_configs_connection ON build_configs(connection_id);
CREATE INDEX build_runs_connection ON build_runs(connection_id);

ALTER TABLE source_jobs ADD COLUMN connection_id text REFERENCES git_connections(id);
UPDATE source_jobs j SET connection_id=s.connection_id FROM application_sources s WHERE s.application_id=j.application_id;
ALTER TABLE source_jobs ALTER COLUMN connection_id SET NOT NULL;
CREATE FUNCTION default_source_job_connection() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.connection_id IS NULL THEN SELECT connection_id INTO NEW.connection_id FROM application_sources WHERE application_id=NEW.application_id; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER source_job_connection_default BEFORE INSERT ON source_jobs FOR EACH ROW EXECUTE FUNCTION default_source_job_connection();
CREATE INDEX source_jobs_connection ON source_jobs(connection_id);
