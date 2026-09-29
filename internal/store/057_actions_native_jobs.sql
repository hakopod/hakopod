ALTER TABLE actions_pools ADD COLUMN provider_hold_reason text NOT NULL DEFAULT '' CHECK(length(provider_hold_reason)<=64);
ALTER TABLE actions_pools
 ADD COLUMN provider_hold_id uuid,
 ADD COLUMN provider_hold_observed_at timestamptz,
 ADD COLUMN provider_hold_evidence jsonb CHECK(octet_length(provider_hold_evidence::text)<=131072),
 ADD CONSTRAINT actions_provider_hold_complete CHECK (
  (provider_hold_reason='' AND provider_hold_id IS NULL AND provider_hold_observed_at IS NULL AND provider_hold_evidence IS NULL)
  OR (provider_hold_reason<>'' AND provider_hold_id IS NOT NULL AND provider_hold_observed_at IS NOT NULL AND provider_hold_evidence IS NOT NULL)
 );
ALTER TABLE actions_jobs
 ADD COLUMN provider_runner_id text NOT NULL DEFAULT '' CHECK(length(provider_runner_id)<=128),
 ADD COLUMN native_job jsonb CHECK(octet_length(native_job::text)<=32768),
 ADD COLUMN discovery_state text NOT NULL DEFAULT '' CHECK(discovery_state IN ('','pending','observed','unavailable','reuse_detected')),
 ADD COLUMN discovery_attempts integer NOT NULL DEFAULT 0 CHECK(discovery_attempts BETWEEN 0 AND 3),
 ADD COLUMN discovery_started_at timestamptz,
 ADD COLUMN discovery_finished boolean NOT NULL DEFAULT false,
 ADD COLUMN cleanup_paused boolean NOT NULL DEFAULT false,
 ADD COLUMN provider_removed boolean NOT NULL DEFAULT false,
 ADD COLUMN provider_history_version bigint NOT NULL DEFAULT 0,
 ADD COLUMN provider_reuse_evidence jsonb CHECK(octet_length(provider_reuse_evidence::text)<=65536);
