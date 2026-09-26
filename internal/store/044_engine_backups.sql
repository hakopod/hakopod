-- Some database engines back themselves up: hakopod asks the engine to write to
-- object storage and then polls it, so the server never sees the bytes and has
-- no digest to record. Logical dumps still stream through this server and keep
-- an absolute digest; only an engine-managed artifact may omit one, and the
-- server-written format prefix is the discriminator. A zero-byte backup is
-- still not a success, so the bytes bound is unchanged.
ALTER TABLE backup_artifacts DROP CONSTRAINT backup_artifacts_sha256_check;
ALTER TABLE backup_artifacts ADD CONSTRAINT backup_artifacts_sha256_check
 CHECK (length(sha256) = 64 OR (sha256 = '' AND format LIKE 'engine:%'));

-- The engine's own name for the backup it is running, so a restarted worker can
-- resume polling it. Deliberately not part of target jsonb: target feeds the
-- idempotency request hash, and writing to it after enqueue would corrupt the
-- comparison a retried request makes. 256 bytes covers an engine backup name
-- with room to spare.
ALTER TABLE backup_jobs ADD COLUMN engine_ref text
 CHECK (engine_ref IS NULL OR length(engine_ref) BETWEEN 1 AND 256);
