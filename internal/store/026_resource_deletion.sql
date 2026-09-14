-- Retain scope names after deletion so old keys, callbacks and network grants
-- cannot acquire access to a different resource with the same name.
CREATE TABLE retired_resource_names (
 kind text NOT NULL CHECK(kind IN ('project','application')),
 project text NOT NULL, environment text NOT NULL DEFAULT '', name text NOT NULL,
 resource_id text NOT NULL, retired_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(kind,project,environment,name)
);
CREATE FUNCTION reject_retired_resource_name() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_TABLE_NAME='projects' THEN
  PERFORM pg_advisory_xact_lock(hashtextextended(NEW.name,73));
  IF EXISTS(SELECT 1 FROM retired_resource_names WHERE kind='project' AND project=NEW.name) THEN
   RAISE EXCEPTION 'This project ID was deleted; choose a new ID' USING ERRCODE='23514';
  END IF;
 ELSE
  PERFORM pg_advisory_xact_lock(hashtextextended(NEW.project||':'||NEW.environment||':'||NEW.name,2));
  IF EXISTS(SELECT 1 FROM retired_resource_names WHERE kind='application' AND project=NEW.project AND environment=NEW.environment AND name=NEW.name) THEN
   RAISE EXCEPTION 'This application name was deleted; choose a new name' USING ERRCODE='23514';
  END IF;
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER projects_retired_name BEFORE INSERT ON projects FOR EACH ROW EXECUTE FUNCTION reject_retired_resource_name();
CREATE TRIGGER applications_retired_name BEFORE INSERT ON applications FOR EACH ROW EXECUTE FUNCTION reject_retired_resource_name();

-- Scoped runtime resources have no FK because installation resources use ''.
-- Lock their parent to serialize new resources with project deletion.
CREATE FUNCTION require_runtime_project() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.project<>'' THEN
  PERFORM 1 FROM projects WHERE name=NEW.project FOR KEY SHARE;
  IF NOT FOUND THEN
   RAISE EXCEPTION 'Project does not exist' USING ERRCODE='23503';
  END IF;
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER runtime_project_exists BEFORE INSERT OR UPDATE ON runtime_resources FOR EACH ROW EXECUTE FUNCTION require_runtime_project();
INSERT INTO schema_migrations(version) VALUES(26);
