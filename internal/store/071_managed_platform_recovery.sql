CREATE TABLE managed_platform_recovery_artifacts (
 id text PRIMARY KEY CHECK(length(id)=32),
 source_platform_id text NOT NULL REFERENCES managed_platforms(id),
 source_revision bigint NOT NULL CHECK(source_revision>0),
 destination_id text NOT NULL REFERENCES backup_destinations(id),
 object_key text NOT NULL CHECK(length(object_key) BETWEEN 1 AND 512),
 encrypted_bytes bigint NOT NULL CHECK(encrypted_bytes>0 AND encrypted_bytes<=68719476736),
 encrypted_sha256 text NOT NULL CHECK(encrypted_sha256 ~ '^[0-9a-f]{64}$'),
 manifest jsonb NOT NULL CHECK(octet_length(manifest::text)<=131072),
 manifest_sha256 text NOT NULL CHECK(manifest_sha256 ~ '^[0-9a-f]{64}$'),
 created_at timestamptz NOT NULL DEFAULT now(),
 published_at timestamptz,
 deleted_at timestamptz,
 UNIQUE(destination_id,object_key)
);

CREATE TABLE managed_platform_recovery_reviews (
 id text PRIMARY KEY CHECK(length(id)=32),
 identity_id text NOT NULL REFERENCES identities(id),
 key_id text NOT NULL REFERENCES api_keys(id),
 project text NOT NULL,
 environment text NOT NULL,
 kind text NOT NULL CHECK(kind IN ('backup','restore')),
 source_platform_id text NOT NULL REFERENCES managed_platforms(id),
 target_platform_id text REFERENCES managed_platforms(id),
 artifact_id text REFERENCES managed_platform_recovery_artifacts(id),
 destination_id text REFERENCES backup_destinations(id),
 destination_revision bigint CHECK(destination_revision>0),
 expected_source_revision bigint NOT NULL CHECK(expected_source_revision>0),
 expected_target_revision bigint CHECK(expected_target_revision>0),
 request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
 authority_fingerprint bytea NOT NULL CHECK(octet_length(authority_fingerprint)=32),
 reviewed_intent jsonb NOT NULL CHECK(octet_length(reviewed_intent::text)<=131072),
 expires_at timestamptz NOT NULL,
 consumed_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(project,environment) REFERENCES environments(project,name),
 CHECK((kind='backup' AND target_platform_id IS NULL AND artifact_id IS NULL AND destination_id IS NOT NULL AND destination_revision IS NOT NULL AND expected_target_revision IS NULL)
    OR (kind='restore' AND target_platform_id IS NOT NULL AND target_platform_id<>source_platform_id AND artifact_id IS NOT NULL AND destination_id IS NULL AND destination_revision IS NULL AND expected_target_revision IS NOT NULL))
);
CREATE INDEX managed_platform_recovery_review_expiry ON managed_platform_recovery_reviews(expires_at) WHERE consumed_at IS NULL;

CREATE TABLE managed_platform_recovery_operations (
 id text PRIMARY KEY CHECK(length(id)=32),
 kind text NOT NULL CHECK(kind IN ('backup','restore')),
 identity_id text NOT NULL REFERENCES identities(id),
 key_id text NOT NULL REFERENCES api_keys(id),
 project text NOT NULL,
 environment text NOT NULL,
 review_id text NOT NULL UNIQUE REFERENCES managed_platform_recovery_reviews(id),
 idempotency_key text NOT NULL CHECK(length(idempotency_key) BETWEEN 8 AND 128),
 request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
 authority_fingerprint bytea NOT NULL CHECK(octet_length(authority_fingerprint)=32),
 source_platform_id text NOT NULL REFERENCES managed_platforms(id),
 target_platform_id text REFERENCES managed_platforms(id),
 artifact_id text REFERENCES managed_platform_recovery_artifacts(id),
 destination_id text REFERENCES backup_destinations(id),
 destination_revision bigint CHECK(destination_revision>0),
 expected_source_revision bigint NOT NULL CHECK(expected_source_revision>0),
 expected_target_revision bigint CHECK(expected_target_revision>0),
 status text NOT NULL DEFAULT 'queued' CHECK(status IN ('queued','running','succeeded','failed','cancelled')),
 phase text NOT NULL DEFAULT 'accepted' CHECK(length(phase)<=64),
 message text NOT NULL DEFAULT '' CHECK(length(message)<=512),
 attempt integer NOT NULL DEFAULT 0 CHECK(attempt BETWEEN 0 AND 240),
 next_attempt_at timestamptz NOT NULL DEFAULT now(),
 lease text NOT NULL DEFAULT '',
 lease_until timestamptz,
 cancel_requested boolean NOT NULL DEFAULT false,
 cleanup_required boolean NOT NULL DEFAULT false,
 result_artifact_id text REFERENCES managed_platform_recovery_artifacts(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 started_at timestamptz,
 finished_at timestamptz,
 UNIQUE(identity_id,idempotency_key),
 FOREIGN KEY(project,environment) REFERENCES environments(project,name),
 CHECK((status='running')=(lease<>'' AND lease_until IS NOT NULL)),
 CHECK((kind='backup' AND target_platform_id IS NULL AND artifact_id IS NULL AND destination_id IS NOT NULL AND destination_revision IS NOT NULL AND expected_target_revision IS NULL)
    OR (kind='restore' AND target_platform_id IS NOT NULL AND target_platform_id<>source_platform_id AND artifact_id IS NOT NULL AND destination_id IS NULL AND destination_revision IS NULL AND expected_target_revision IS NOT NULL))
);
CREATE TABLE managed_platform_recovery_database_state (
 operation_id text PRIMARY KEY REFERENCES managed_platform_recovery_operations(id),
 platform_id text NOT NULL REFERENCES managed_platforms(id),
 prior_read_only boolean NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE managed_platform_recovery_deployments (
 operation_id text NOT NULL REFERENCES managed_platform_recovery_operations(id),
 platform_id text NOT NULL REFERENCES managed_platforms(id),
 deployment_name text NOT NULL,
 deployment_uid text NOT NULL,
 baseline_generation bigint NOT NULL CHECK(baseline_generation>0),
 current_generation bigint NOT NULL CHECK(current_generation>=baseline_generation),
 prior_replicas integer NOT NULL CHECK(prior_replicas>=0 AND prior_replicas<=16),
 target_replicas integer NOT NULL CHECK(target_replicas>=0 AND target_replicas<=16),
 transition_token text NOT NULL CHECK(transition_token ~ '^[0-9a-f]{64}$'),
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(operation_id,deployment_name)
);
CREATE TABLE managed_platform_recovery_uploads (
 operation_id text PRIMARY KEY REFERENCES managed_platform_recovery_operations(id),
 destination_id text NOT NULL REFERENCES backup_destinations(id),
 object_key text NOT NULL CHECK(length(object_key) BETWEEN 1 AND 512),
 artifact_id text REFERENCES managed_platform_recovery_artifacts(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(destination_id,object_key)
);
CREATE INDEX managed_platform_recovery_work ON managed_platform_recovery_operations(next_attempt_at,created_at,id) WHERE status IN ('queued','running');
CREATE UNIQUE INDEX managed_platform_recovery_active_source ON managed_platform_recovery_operations(source_platform_id) WHERE status IN ('queued','running');
CREATE UNIQUE INDEX managed_platform_recovery_active_target ON managed_platform_recovery_operations(target_platform_id) WHERE target_platform_id IS NOT NULL AND status IN ('queued','running');

CREATE FUNCTION keep_managed_platform_recovery_review_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.id<>OLD.id OR NEW.identity_id<>OLD.identity_id OR NEW.key_id<>OLD.key_id OR NEW.project<>OLD.project OR NEW.environment<>OLD.environment OR NEW.kind<>OLD.kind OR NEW.source_platform_id<>OLD.source_platform_id OR NEW.target_platform_id IS DISTINCT FROM OLD.target_platform_id OR NEW.artifact_id IS DISTINCT FROM OLD.artifact_id OR NEW.destination_id IS DISTINCT FROM OLD.destination_id OR NEW.destination_revision IS DISTINCT FROM OLD.destination_revision OR NEW.expected_source_revision<>OLD.expected_source_revision OR NEW.expected_target_revision IS DISTINCT FROM OLD.expected_target_revision OR NEW.request_hash<>OLD.request_hash OR NEW.authority_fingerprint<>OLD.authority_fingerprint OR NEW.reviewed_intent<>OLD.reviewed_intent OR NEW.expires_at<>OLD.expires_at OR NEW.created_at<>OLD.created_at OR (OLD.consumed_at IS NOT NULL AND NEW.consumed_at IS DISTINCT FROM OLD.consumed_at) THEN
  RAISE EXCEPTION 'managed platform recovery review is immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER managed_platform_recovery_review_immutable BEFORE UPDATE ON managed_platform_recovery_reviews FOR EACH ROW EXECUTE FUNCTION keep_managed_platform_recovery_review_immutable();

CREATE FUNCTION keep_managed_platform_recovery_operation_intent() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.id<>OLD.id OR NEW.kind<>OLD.kind OR NEW.identity_id<>OLD.identity_id OR NEW.key_id<>OLD.key_id OR NEW.project<>OLD.project OR NEW.environment<>OLD.environment OR NEW.review_id<>OLD.review_id OR NEW.idempotency_key<>OLD.idempotency_key OR NEW.request_hash<>OLD.request_hash OR NEW.authority_fingerprint<>OLD.authority_fingerprint OR NEW.source_platform_id<>OLD.source_platform_id OR NEW.target_platform_id IS DISTINCT FROM OLD.target_platform_id OR NEW.artifact_id IS DISTINCT FROM OLD.artifact_id OR NEW.destination_id IS DISTINCT FROM OLD.destination_id OR NEW.destination_revision IS DISTINCT FROM OLD.destination_revision OR NEW.expected_source_revision<>OLD.expected_source_revision OR NEW.expected_target_revision IS DISTINCT FROM OLD.expected_target_revision OR NEW.created_at<>OLD.created_at THEN
  RAISE EXCEPTION 'managed platform recovery operation intent is immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER managed_platform_recovery_operation_intent BEFORE UPDATE ON managed_platform_recovery_operations FOR EACH ROW EXECUTE FUNCTION keep_managed_platform_recovery_operation_intent();

CREATE FUNCTION keep_managed_platform_recovery_artifact_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.id<>OLD.id OR NEW.source_platform_id<>OLD.source_platform_id OR NEW.source_revision<>OLD.source_revision OR NEW.destination_id<>OLD.destination_id OR NEW.object_key<>OLD.object_key OR NEW.encrypted_bytes<>OLD.encrypted_bytes OR NEW.encrypted_sha256<>OLD.encrypted_sha256 OR NEW.manifest<>OLD.manifest OR NEW.manifest_sha256<>OLD.manifest_sha256 OR NEW.created_at<>OLD.created_at OR (OLD.published_at IS NOT NULL AND NEW.published_at IS DISTINCT FROM OLD.published_at) OR (OLD.deleted_at IS NOT NULL AND NEW.deleted_at IS DISTINCT FROM OLD.deleted_at) THEN
  RAISE EXCEPTION 'managed platform recovery artifact is immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER managed_platform_recovery_artifact_immutable BEFORE UPDATE ON managed_platform_recovery_artifacts FOR EACH ROW EXECUTE FUNCTION keep_managed_platform_recovery_artifact_immutable();

INSERT INTO schema_migrations(version) VALUES(71);
