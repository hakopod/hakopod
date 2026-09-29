ALTER TABLE actions_jobs ADD COLUMN provider_config jsonb;

-- Only a surviving slot proves the original provider target and secret reference.
-- Older rows without a slot remain readable metadata, with provider access disabled.
UPDATE actions_jobs j SET provider_config = s.config->'actions'
FROM actions_slots s
WHERE s.id=j.slot_id AND s.application_id=j.application_id AND s.service=j.service;
