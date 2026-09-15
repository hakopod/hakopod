ALTER TABLE git_connections ADD COLUMN project text NOT NULL DEFAULT '';
ALTER TABLE git_connections ADD COLUMN environment text NOT NULL DEFAULT '';
ALTER TABLE git_connections ADD CONSTRAINT git_connection_scope CHECK ((project = '') = (environment = ''));
DROP INDEX git_connections_name;
CREATE UNIQUE INDEX git_connections_name ON git_connections(project,environment,lower(name));
CREATE INDEX git_connections_scope ON git_connections(project,environment);
