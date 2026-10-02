CREATE TABLE platform_component_recovery_overrides (
 platform_id text NOT NULL REFERENCES managed_platforms(id),
 platform_revision bigint NOT NULL CHECK(platform_revision>0),
 component text NOT NULL CHECK(length(component) BETWEEN 1 AND 63),
 resource_kind text NOT NULL CHECK(resource_kind IN ('neon_tenant','neon_timeline','runtime_component')),
 recovery_operation_id text NOT NULL REFERENCES managed_platform_recovery_operations(id),
 artifact_id text NOT NULL REFERENCES managed_platform_recovery_artifacts(id),
 manifest_sha256 text NOT NULL CHECK(manifest_sha256 ~ '^[0-9a-f]{64}$'),
 prior_resource_id text NOT NULL CHECK(length(prior_resource_id) BETWEEN 1 AND 255),
 prior_generation bigint NOT NULL CHECK(prior_generation>0),
 prior_owner_operation_id text NOT NULL REFERENCES managed_platform_operations(id),
 replacement_external_key text NOT NULL CHECK(length(replacement_external_key) BETWEEN 1 AND 255),
 replacement_resource_id text CHECK(replacement_resource_id IS NULL OR length(replacement_resource_id) BETWEEN 1 AND 255),
 replacement_generation bigint CHECK(replacement_generation IS NULL OR replacement_generation>0),
 transition_token text NOT NULL CHECK(transition_token ~ '^[0-9a-f]{64}$'),
 phase text NOT NULL CHECK(phase IN ('planned','prior_released','reserved','confirmed','replacement_released','complete','empty_complete','untouched_complete','adopted')),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 prior_released_at timestamptz,
 reserved_at timestamptz,
 confirmed_at timestamptz,
 replacement_released_at timestamptz,
 completed_at timestamptz,
 adopted_at timestamptz,
 PRIMARY KEY(platform_id,platform_revision,component,resource_kind),
 UNIQUE(recovery_operation_id,component,resource_kind),
 FOREIGN KEY(prior_owner_operation_id,platform_id,platform_revision) REFERENCES managed_platform_operations(id,platform_id,revision),
 CHECK((replacement_resource_id IS NULL)=(replacement_generation IS NULL)),
 CHECK(phase NOT IN ('confirmed','replacement_released','complete','adopted') OR replacement_resource_id IS NOT NULL)
);
CREATE UNIQUE INDEX platform_component_recovery_live_replacement
 ON platform_component_recovery_overrides(resource_kind,replacement_resource_id)
 WHERE replacement_resource_id IS NOT NULL AND replacement_released_at IS NULL AND adopted_at IS NULL;
CREATE INDEX platform_component_recovery_operation
 ON platform_component_recovery_overrides(recovery_operation_id,component,resource_kind);

CREATE TABLE managed_platform_neon_proxy_recovery_rebindings (
 recovery_operation_id text PRIMARY KEY REFERENCES managed_platform_recovery_operations(id),
 endpoint_id text NOT NULL REFERENCES managed_platform_neon_proxy_endpoints(endpoint_id),
 platform_id text NOT NULL REFERENCES managed_platforms(id),
 platform_revision bigint NOT NULL CHECK(platform_revision>0),
 artifact_id text NOT NULL REFERENCES managed_platform_recovery_artifacts(id),
 manifest_sha256 text NOT NULL CHECK(manifest_sha256 ~ '^[0-9a-f]{64}$'),
 tenant_id text NOT NULL CHECK(length(tenant_id) BETWEEN 1 AND 63),
 prior_branch_id text NOT NULL CHECK(length(prior_branch_id) BETWEEN 1 AND 63),
 replacement_branch_id text NOT NULL CHECK(length(replacement_branch_id) BETWEEN 1 AND 63),
 compute_id text NOT NULL CHECK(length(compute_id) BETWEEN 1 AND 63),
 endpoint_generation bigint NOT NULL CHECK(endpoint_generation>0),
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(endpoint_id,platform_revision)
);

CREATE FUNCTION keep_managed_platform_neon_proxy_rebinding_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'managed platform Neon proxy recovery rebinding is immutable';
END $$;
CREATE TRIGGER managed_platform_neon_proxy_rebinding_immutable BEFORE UPDATE OR DELETE ON managed_platform_neon_proxy_recovery_rebindings FOR EACH ROW EXECUTE FUNCTION keep_managed_platform_neon_proxy_rebinding_immutable();

CREATE FUNCTION keep_platform_component_recovery_override_intent() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.platform_id<>OLD.platform_id OR NEW.platform_revision<>OLD.platform_revision OR NEW.component<>OLD.component OR NEW.resource_kind<>OLD.resource_kind OR NEW.recovery_operation_id<>OLD.recovery_operation_id OR NEW.artifact_id<>OLD.artifact_id OR NEW.manifest_sha256<>OLD.manifest_sha256 OR NEW.prior_resource_id<>OLD.prior_resource_id OR NEW.prior_generation<>OLD.prior_generation OR NEW.prior_owner_operation_id<>OLD.prior_owner_operation_id OR NEW.replacement_external_key<>OLD.replacement_external_key OR NEW.transition_token<>OLD.transition_token OR NEW.created_at<>OLD.created_at OR (OLD.replacement_resource_id IS NOT NULL AND NEW.replacement_resource_id IS DISTINCT FROM OLD.replacement_resource_id) OR (OLD.replacement_generation IS NOT NULL AND NEW.replacement_generation IS DISTINCT FROM OLD.replacement_generation) OR (OLD.prior_released_at IS NOT NULL AND NEW.prior_released_at IS DISTINCT FROM OLD.prior_released_at) OR (OLD.reserved_at IS NOT NULL AND NEW.reserved_at IS DISTINCT FROM OLD.reserved_at) OR (OLD.confirmed_at IS NOT NULL AND NEW.confirmed_at IS DISTINCT FROM OLD.confirmed_at) OR (OLD.replacement_released_at IS NOT NULL AND NEW.replacement_released_at IS DISTINCT FROM OLD.replacement_released_at) OR (OLD.completed_at IS NOT NULL AND NEW.completed_at IS DISTINCT FROM OLD.completed_at) OR (OLD.adopted_at IS NOT NULL AND NEW.adopted_at IS DISTINCT FROM OLD.adopted_at) THEN
  RAISE EXCEPTION 'platform component recovery override intent is immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER platform_component_recovery_override_intent BEFORE UPDATE ON platform_component_recovery_overrides FOR EACH ROW EXECUTE FUNCTION keep_platform_component_recovery_override_intent();

INSERT INTO schema_migrations(version) VALUES(76);
