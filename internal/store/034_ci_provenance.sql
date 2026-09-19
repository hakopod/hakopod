ALTER TABLE deployments ADD COLUMN provenance jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(provenance) = 'object');
