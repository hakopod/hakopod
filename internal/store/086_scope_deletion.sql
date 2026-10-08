-- Retain deleted scope IDs while preserving immutable resource history.
ALTER TABLE retired_resource_names DROP CONSTRAINT retired_resource_names_kind_check;
ALTER TABLE retired_resource_names ADD CONSTRAINT retired_resource_names_kind_check CHECK(kind IN ('project','environment','application'));

CREATE FUNCTION reject_retired_environment_name() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM 1 FROM projects WHERE name=NEW.project FOR KEY SHARE;
 IF NOT FOUND THEN RAISE EXCEPTION 'Project does not exist' USING ERRCODE='23503'; END IF;
 IF EXISTS(SELECT 1 FROM retired_resource_names WHERE kind='environment' AND project=NEW.project AND environment=NEW.name) THEN
  RAISE EXCEPTION 'This environment ID was deleted. Choose a new ID.' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER environments_retired_name BEFORE INSERT ON environments FOR EACH ROW EXECUTE FUNCTION reject_retired_environment_name();

-- These rows retain resource and review history after their scope is removed.
-- Inserts and scope changes still require and lock a live parent environment.
CREATE FUNCTION require_live_environment() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' AND NEW.project=OLD.project AND NEW.environment=OLD.environment THEN
  IF TG_TABLE_NAME NOT IN ('managed_databases','external_databases','managed_platforms') THEN RETURN NEW; END IF;
  IF (to_jsonb(NEW)->>'deleted_at') IS NOT NULL OR (to_jsonb(OLD)->>'deleted_at') IS NULL THEN RETURN NEW; END IF;
 END IF;
 PERFORM 1 FROM environments WHERE project=NEW.project AND name=NEW.environment FOR KEY SHARE;
 IF NOT FOUND THEN RAISE EXCEPTION 'Environment does not exist' USING ERRCODE='23503'; END IF;
 RETURN NEW;
END $$;
DO $$
DECLARE table_name text;
BEGIN
 FOREACH table_name IN ARRAY ARRAY['managed_databases','external_databases','managed_platforms','managed_platform_reviews','managed_platform_recovery_reviews','managed_platform_recovery_operations'] LOOP
  EXECUTE format('ALTER TABLE %I DROP CONSTRAINT %I',table_name,table_name||'_project_environment_fkey');
  EXECUTE format('CREATE TRIGGER scope_parent_exists BEFORE INSERT OR UPDATE OF project,environment ON %I FOR EACH ROW EXECUTE FUNCTION require_live_environment()',table_name);
 END LOOP;
 FOREACH table_name IN ARRAY ARRAY['managed_databases','external_databases','managed_platforms'] LOOP
  EXECUTE format('CREATE TRIGGER resource_parent_on_restore BEFORE UPDATE OF deleted_at ON %I FOR EACH ROW EXECUTE FUNCTION require_live_environment()',table_name);
 END LOOP;
END $$;

-- Runtime resources may be installation-wide or project-wide. Scoped inserts
-- lock their environment as well as their project against metadata deletion.
CREATE OR REPLACE FUNCTION require_runtime_project() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' AND NEW.project=OLD.project AND NEW.environment=OLD.environment THEN RETURN NEW; END IF;
 IF NEW.project<>'' THEN
  PERFORM 1 FROM projects WHERE name=NEW.project FOR KEY SHARE;
  IF NOT FOUND THEN RAISE EXCEPTION 'Project does not exist' USING ERRCODE='23503'; END IF;
  IF NEW.environment<>'' THEN
   PERFORM 1 FROM environments WHERE project=NEW.project AND name=NEW.environment FOR KEY SHARE;
   IF NOT FOUND THEN RAISE EXCEPTION 'Environment does not exist' USING ERRCODE='23503'; END IF;
  END IF;
 END IF;
 RETURN NEW;
END $$;
DROP TRIGGER runtime_project_exists ON runtime_resources;
CREATE TRIGGER runtime_project_exists BEFORE INSERT OR UPDATE OF project,environment ON runtime_resources FOR EACH ROW EXECUTE FUNCTION require_runtime_project();
-- Provider grants must not acquire a deleted scope through array updates.
CREATE FUNCTION require_secret_provider_scope() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE environment_name text;
BEGIN
 PERFORM 1 FROM projects WHERE name=NEW.project FOR KEY SHARE;
 IF NOT FOUND THEN RAISE EXCEPTION 'Project does not exist' USING ERRCODE='23503'; END IF;
 FOR environment_name IN SELECT DISTINCT unnest(NEW.environments) ORDER BY 1 LOOP
  PERFORM 1 FROM environments WHERE project=NEW.project AND name=environment_name FOR KEY SHARE;
  IF NOT FOUND THEN RAISE EXCEPTION 'Environment does not exist' USING ERRCODE='23503'; END IF;
 END LOOP;
 RETURN NEW;
END $$;
CREATE TRIGGER secret_provider_scope_exists BEFORE INSERT OR UPDATE OF project,environments ON secret_provider_scopes FOR EACH ROW EXECUTE FUNCTION require_secret_provider_scope();
INSERT INTO schema_migrations(version) VALUES(86);
