-- Placement observations never change the accepted resource claim. An
-- observation applies only to the exact effective source after restoration.
CREATE TABLE managed_platform_neon_tenant_placements (
 platform_id text NOT NULL REFERENCES managed_platforms(id),
 platform_revision bigint NOT NULL CHECK(platform_revision>0),
 component text NOT NULL CHECK(component='tenant'),
 resource_kind text NOT NULL CHECK(resource_kind='neon_tenant'),
 owner_operation_id text NOT NULL REFERENCES managed_platform_operations(id),
 source_resource_id text NOT NULL CHECK(length(source_resource_id) BETWEEN 1 AND 255),
 source_generation bigint NOT NULL CHECK(source_generation>0),
 observed_resource_id text NOT NULL CHECK(length(observed_resource_id) BETWEEN 1 AND 255),
 observed_generation bigint NOT NULL CHECK(observed_generation>=source_generation),
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(platform_id,platform_revision,component,resource_kind)
);

CREATE VIEW effective_platform_component_resources AS
WITH source AS (
 SELECT b.platform_id,b.platform_revision,b.component,b.resource_kind,b.owner_operation_id,b.released_at,b.intent_id,
 b.resource_id AS base_resource_id,b.immutable_generation AS base_generation,
 COALESCE(r.replacement_resource_id,b.resource_id) AS source_resource_id,
 COALESCE(r.runtime_generation,r.replacement_generation,b.immutable_generation) AS source_generation,
 r.recovery_operation_id
 FROM platform_component_resources b
 LEFT JOIN platform_component_recovery_overrides r ON r.platform_id=b.platform_id AND r.platform_revision=b.platform_revision AND r.component=b.component AND r.resource_kind=b.resource_kind
 AND r.phase IN ('confirmed','adopted') AND r.replacement_released_at IS NULL
 WHERE b.released_at IS NULL
 AND NOT EXISTS(SELECT 1 FROM platform_component_recovery_overrides pending WHERE pending.platform_id=b.platform_id AND pending.platform_revision=b.platform_revision AND pending.component=b.component AND pending.resource_kind=b.resource_kind AND pending.phase IN ('prior_released','reserved','replacement_released','complete','empty_complete'))
)
SELECT s.platform_id,s.platform_revision,s.component,s.resource_kind,
 COALESCE(p.observed_resource_id,s.source_resource_id) AS resource_id,
 COALESCE(p.observed_generation,s.source_generation) AS immutable_generation,
 s.owner_operation_id,s.released_at,s.intent_id,s.base_resource_id,s.base_generation,s.source_resource_id,s.source_generation,s.recovery_operation_id
FROM source s
LEFT JOIN managed_platform_neon_tenant_placements p ON p.platform_id=s.platform_id AND p.platform_revision=s.platform_revision AND p.component=s.component AND p.resource_kind=s.resource_kind
 AND p.owner_operation_id=s.owner_operation_id AND p.source_resource_id=s.source_resource_id AND p.source_generation=s.source_generation;

INSERT INTO schema_migrations(version) VALUES(81);
