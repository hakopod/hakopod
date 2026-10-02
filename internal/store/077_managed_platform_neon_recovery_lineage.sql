CREATE TABLE managed_platform_neon_recovery_lineage (
 platform_id text NOT NULL REFERENCES managed_platforms(id),
 platform_revision bigint NOT NULL CHECK(platform_revision>0),
 recovery_operation_id text NOT NULL REFERENCES managed_platform_neon_recovery_bindings(operation_id),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(platform_id,platform_revision),
 UNIQUE(recovery_operation_id,platform_revision)
);

INSERT INTO managed_platform_neon_recovery_lineage(platform_id,platform_revision,recovery_operation_id)
SELECT target_platform_id,target_revision,operation_id FROM managed_platform_neon_recovery_bindings;

CREATE FUNCTION keep_managed_platform_neon_recovery_lineage_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'managed platform Neon recovery lineage is immutable';
END $$;
CREATE TRIGGER managed_platform_neon_recovery_lineage_immutable BEFORE UPDATE OR DELETE ON managed_platform_neon_recovery_lineage FOR EACH ROW EXECUTE FUNCTION keep_managed_platform_neon_recovery_lineage_immutable();

INSERT INTO schema_migrations(version) VALUES(77);
