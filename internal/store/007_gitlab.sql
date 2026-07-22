ALTER TABLE application_sources ADD COLUMN IF NOT EXISTS provider text NOT NULL DEFAULT 'github' CHECK(provider IN ('github','gitlab'));
CREATE INDEX IF NOT EXISTS application_sources_provider_repo ON application_sources(provider,repository,branch) WHERE auto_deploy;
