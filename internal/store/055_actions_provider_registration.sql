ALTER TABLE actions_slots
 ADD COLUMN provider_runner_id text NOT NULL DEFAULT '' CHECK(length(provider_runner_id)<=128),
 ADD COLUMN encrypted_registration bytea CHECK(octet_length(encrypted_registration)<=262144);
