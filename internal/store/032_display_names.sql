ALTER TABLE applications ADD COLUMN display_name text NOT NULL DEFAULT '';
ALTER TABLE applications ADD COLUMN service_display_names jsonb NOT NULL DEFAULT '{}';
ALTER TABLE applications ADD COLUMN metadata_revision bigint NOT NULL DEFAULT 1;
ALTER TABLE projects ADD COLUMN metadata_revision bigint NOT NULL DEFAULT 1;
