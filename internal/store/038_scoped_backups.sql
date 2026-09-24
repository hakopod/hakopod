ALTER TABLE backup_jobs ADD COLUMN authority jsonb;
ALTER TABLE backup_schedules ADD COLUMN authority jsonb;
CREATE INDEX backup_destination_scope ON backup_destinations ((config->>'project'), (config->>'environment'));
-- Deliberately no application FK: deletion retains PVCs and their accounting.
CREATE TABLE storage_reservations (
 project text NOT NULL,
 environment text NOT NULL,
 application_id text NOT NULL,
 claim text NOT NULL,
 size_gib bigint NOT NULL CHECK(size_gib > 0),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(application_id,claim)
);
CREATE INDEX storage_reservation_scope ON storage_reservations(project,environment);
CREATE INDEX backup_job_scope ON backup_jobs ((authority->>'project'),(authority->>'environment'),created_at,id);
CREATE INDEX backup_schedule_scope ON backup_schedules ((authority->>'project'),(authority->>'environment'));
