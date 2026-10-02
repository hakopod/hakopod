ALTER TABLE managed_platform_recovery_deployments
 ADD COLUMN workload_kind text NOT NULL DEFAULT 'deployment'
 CHECK(workload_kind IN ('deployment','statefulset'));

CREATE TABLE managed_platform_neon_recovery_bindings (
 operation_id text PRIMARY KEY REFERENCES managed_platform_recovery_operations(id),
 target_platform_id text NOT NULL REFERENCES managed_platforms(id),
 target_revision bigint NOT NULL CHECK(target_revision>0),
 artifact_id text NOT NULL REFERENCES managed_platform_recovery_artifacts(id),
 manifest_sha256 text NOT NULL CHECK(manifest_sha256 ~ '^[0-9a-f]{64}$'),
 tenant_id text NOT NULL CHECK(tenant_id ~ '^[0-9a-f]{32}$'),
 timeline_id text NOT NULL CHECK(timeline_id ~ '^[0-9a-f]{32}$'),
 tenant_generation bigint NOT NULL CHECK(tenant_generation>0),
 timeline_generation bigint NOT NULL CHECK(timeline_generation>0),
 staging_prefix text NOT NULL CHECK(length(staging_prefix) BETWEEN 1 AND 512),
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(target_platform_id,target_revision),
 UNIQUE(staging_prefix)
);

CREATE FUNCTION keep_managed_platform_neon_recovery_binding_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'managed platform Neon recovery binding is immutable';
END $$;
CREATE TRIGGER managed_platform_neon_recovery_binding_immutable BEFORE UPDATE OR DELETE ON managed_platform_neon_recovery_bindings FOR EACH ROW EXECUTE FUNCTION keep_managed_platform_neon_recovery_binding_immutable();

CREATE TABLE managed_platform_neon_recovery_captures (
 operation_id text PRIMARY KEY REFERENCES managed_platform_recovery_operations(id),
 manifest_neon jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
