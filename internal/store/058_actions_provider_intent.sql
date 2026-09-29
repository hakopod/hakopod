ALTER TABLE actions_slots
 ADD COLUMN encrypted_provider_intent bytea CHECK(octet_length(encrypted_provider_intent)<=262144);

ALTER TABLE actions_jobs
 ADD COLUMN encrypted_provider_intent bytea CHECK(octet_length(encrypted_provider_intent)<=262144);
