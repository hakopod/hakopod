ALTER TABLE actions_slots
 ADD COLUMN manager_launch_attempted boolean NOT NULL DEFAULT false,
 ADD COLUMN provider_cleanup_runner_id text NOT NULL DEFAULT '' CHECK(length(provider_cleanup_runner_id)<=128),
 ADD COLUMN provider_cleanup_confirmed boolean NOT NULL DEFAULT false;
