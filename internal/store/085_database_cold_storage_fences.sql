CREATE TABLE managed_database_cold_storage_fences (
 database_id text PRIMARY KEY REFERENCES managed_databases(id) ON DELETE CASCADE,
 database_revision bigint NOT NULL CHECK(database_revision>0),
 backup_job_id text NOT NULL UNIQUE REFERENCES backup_jobs(id) ON DELETE RESTRICT,
 kind text NOT NULL CHECK(kind IN ('backup','restore')),
 cleanup_token text NOT NULL DEFAULT '',
 cleanup_worker_lease text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
