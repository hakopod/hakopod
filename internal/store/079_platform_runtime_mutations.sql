-- A restore's replacement generation remains immutable evidence. Later owned
-- workload updates advance a separate current generation under their lease.
ALTER TABLE platform_component_recovery_overrides ADD COLUMN runtime_generation bigint
 CHECK(runtime_generation IS NULL OR (resource_kind='runtime_component' AND
 (component LIKE 'deployment.%' OR component LIKE 'statefulset.%') AND
 replacement_generation IS NOT NULL AND runtime_generation>=replacement_generation));

ALTER TABLE managed_platform_recovery_deployments
 ADD COLUMN desired_spec_sha256 text CHECK(desired_spec_sha256 IS NULL OR desired_spec_sha256 ~ '^[0-9a-f]{64}$'),
 ADD COLUMN transition_generation bigint CHECK(transition_generation IS NULL OR transition_generation>=baseline_generation),
 ADD COLUMN transition_complete boolean NOT NULL DEFAULT true,
 ADD CONSTRAINT platform_recovery_workload_intent_complete CHECK(transition_complete OR (desired_spec_sha256 IS NOT NULL AND transition_generation IS NOT NULL));

CREATE TABLE managed_platform_runtime_mutations (
 platform_id text NOT NULL REFERENCES managed_platforms(id),
 platform_revision bigint NOT NULL CHECK(platform_revision>0),
 component text NOT NULL CHECK(component LIKE 'deployment.%' OR component LIKE 'statefulset.%'),
 operation_id text NOT NULL REFERENCES managed_platform_operations(id),
 resource_id text NOT NULL CHECK(length(resource_id) BETWEEN 1 AND 255),
 old_generation bigint NOT NULL CHECK(old_generation>0),
 new_generation bigint NOT NULL CHECK(new_generation BETWEEN old_generation AND old_generation+1),
 transition_token text NOT NULL CHECK(transition_token ~ '^[0-9a-f]{32}$'),
 spec_sha256 text NOT NULL CHECK(spec_sha256 ~ '^[0-9a-f]{64}$'),
 completed_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(platform_id,platform_revision,component),
 FOREIGN KEY(operation_id,platform_id,platform_revision) REFERENCES managed_platform_operations(id,platform_id,revision)
);

INSERT INTO schema_migrations(version) VALUES(79);
