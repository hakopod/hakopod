CREATE TABLE managed_platforms (
 id text PRIMARY KEY CHECK(length(id)=32),
 project text NOT NULL,
 environment text NOT NULL,
 name text NOT NULL,
 kind text NOT NULL CHECK(kind IN ('neon','supabase')),
 revision bigint NOT NULL CHECK(revision>0),
 desired_spec jsonb NOT NULL CHECK(octet_length(desired_spec::text)<=65536),
 status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','ready','failed','deleting','deleted')),
 observation jsonb NOT NULL DEFAULT '{}' CHECK(octet_length(observation::text)<=65536),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 deleted_at timestamptz,
 FOREIGN KEY(project,environment) REFERENCES environments(project,name),
 CHECK(desired_spec->>'name'=name AND desired_spec->>'kind'=kind)
);
CREATE UNIQUE INDEX managed_platform_live_name ON managed_platforms(project,environment,name) WHERE deleted_at IS NULL;

CREATE TABLE managed_platform_reviews (
 id text PRIMARY KEY CHECK(length(id)=32),
 identity_id text NOT NULL REFERENCES identities(id),
 key_id text NOT NULL REFERENCES api_keys(id),
 project text NOT NULL,
 environment text NOT NULL,
 platform_id text NOT NULL CHECK(length(platform_id)=32),
 platform_name text NOT NULL,
 kind text NOT NULL CHECK(kind IN ('create','update','delete')),
 expected_revision bigint NOT NULL CHECK(expected_revision>=0),
 request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
 authority_fingerprint bytea NOT NULL CHECK(octet_length(authority_fingerprint)=32),
 desired_spec jsonb NOT NULL CHECK(octet_length(desired_spec::text)<=65536),
 resolved_plan jsonb NOT NULL CHECK(octet_length(resolved_plan::text)<=65536),
 payload jsonb NOT NULL CHECK(octet_length(payload::text)<=65536),
 expires_at timestamptz NOT NULL,
 consumed_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(project,environment) REFERENCES environments(project,name)
);
CREATE INDEX managed_platform_review_expiry ON managed_platform_reviews(expires_at) WHERE consumed_at IS NULL;
CREATE INDEX managed_platform_review_owner ON managed_platform_reviews(identity_id,platform_id,created_at) WHERE consumed_at IS NULL;

CREATE FUNCTION keep_managed_platform_review_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.id<>OLD.id OR NEW.identity_id<>OLD.identity_id OR NEW.key_id<>OLD.key_id OR NEW.project<>OLD.project OR NEW.environment<>OLD.environment OR NEW.platform_id<>OLD.platform_id OR NEW.platform_name<>OLD.platform_name OR NEW.kind<>OLD.kind OR NEW.expected_revision<>OLD.expected_revision OR NEW.request_hash<>OLD.request_hash OR NEW.authority_fingerprint<>OLD.authority_fingerprint OR NEW.desired_spec<>OLD.desired_spec OR NEW.resolved_plan<>OLD.resolved_plan OR NEW.payload<>OLD.payload OR NEW.expires_at<>OLD.expires_at OR NEW.created_at<>OLD.created_at OR OLD.consumed_at IS NOT NULL AND NEW.consumed_at IS DISTINCT FROM OLD.consumed_at THEN
  RAISE EXCEPTION 'managed platform review is immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER managed_platform_review_immutable BEFORE UPDATE ON managed_platform_reviews FOR EACH ROW EXECUTE FUNCTION keep_managed_platform_review_immutable();

CREATE FUNCTION guard_managed_platform_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.id<>OLD.id OR NEW.project<>OLD.project OR NEW.environment<>OLD.environment OR NEW.name<>OLD.name OR NEW.kind<>OLD.kind THEN
  RAISE EXCEPTION 'managed platform identity is immutable';
 END IF;
 IF NEW.desired_spec<>OLD.desired_spec AND NEW.revision<>OLD.revision+1 THEN
  RAISE EXCEPTION 'managed platform desired spec requires one revision increment';
 END IF;
 IF NEW.revision<>OLD.revision AND NEW.revision<>OLD.revision+1 THEN
  RAISE EXCEPTION 'managed platform revision must advance by one';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER managed_platform_revision_guard BEFORE UPDATE ON managed_platforms FOR EACH ROW EXECUTE FUNCTION guard_managed_platform_revision();

CREATE TABLE managed_platform_operations (
 id text PRIMARY KEY CHECK(length(id)=32),
 platform_id text NOT NULL REFERENCES managed_platforms(id),
 revision bigint NOT NULL,
 identity_id text NOT NULL REFERENCES identities(id),
 key_id text NOT NULL REFERENCES api_keys(id),
 idempotency_key text NOT NULL CHECK(length(idempotency_key) BETWEEN 8 AND 128),
 request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
 authority_fingerprint bytea NOT NULL CHECK(octet_length(authority_fingerprint)=32),
 review_id text NOT NULL REFERENCES managed_platform_reviews(id),
 kind text NOT NULL CHECK(kind IN ('create','update','delete')),
 desired_spec jsonb NOT NULL CHECK(octet_length(desired_spec::text)<=65536),
 resolved_plan jsonb NOT NULL CHECK(octet_length(resolved_plan::text)<=65536),
 encrypted_snapshot bytea NOT NULL CHECK(octet_length(encrypted_snapshot) BETWEEN 1 AND 65536),
 review jsonb NOT NULL CHECK(octet_length(review::text)<=65536),
 status text NOT NULL DEFAULT 'queued' CHECK(status IN ('queued','running','succeeded','failed','cancelled')),
 phase text NOT NULL DEFAULT 'accepted' CHECK(length(phase)<=64),
 message text NOT NULL DEFAULT '' CHECK(length(message)<=512),
 attempt integer NOT NULL DEFAULT 0 CHECK(attempt BETWEEN 0 AND 240),
 next_attempt_at timestamptz NOT NULL DEFAULT now(),
 lease text NOT NULL DEFAULT '',
 lease_until timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 started_at timestamptz,
 finished_at timestamptz,
	UNIQUE(identity_id,idempotency_key),
	UNIQUE(platform_id,revision), UNIQUE(id,platform_id,revision),
 CHECK((status='running')=(lease<>'' AND lease_until IS NOT NULL))
);
CREATE INDEX managed_platform_operation_work ON managed_platform_operations(next_attempt_at,created_at,id) WHERE status IN ('queued','running');
CREATE UNIQUE INDEX managed_platform_review_once ON managed_platform_operations(review_id);

CREATE TABLE platform_resource_intents (
 id text PRIMARY KEY CHECK(length(id)=32),
 platform_id text NOT NULL REFERENCES managed_platforms(id),
 platform_revision bigint NOT NULL,
 component text NOT NULL CHECK(length(component) BETWEEN 1 AND 63),
 resource_kind text NOT NULL CHECK(resource_kind IN ('neon_tenant','neon_timeline','runtime_component')),
 external_key text NOT NULL CHECK(length(external_key) BETWEEN 1 AND 255),
 owner_operation_id text NOT NULL REFERENCES managed_platform_operations(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 confirmed_at timestamptz,
 released_at timestamptz,
	FOREIGN KEY(owner_operation_id,platform_id,platform_revision) REFERENCES managed_platform_operations(id,platform_id,revision),
	CHECK(confirmed_at IS NULL OR confirmed_at>=created_at),
	CHECK(released_at IS NULL OR released_at>=created_at),
	CHECK(confirmed_at IS NULL OR released_at IS NULL OR released_at>=confirmed_at)
);
CREATE UNIQUE INDEX platform_resource_intent_live_external ON platform_resource_intents(resource_kind,external_key) WHERE released_at IS NULL;
CREATE UNIQUE INDEX platform_resource_intent_live_component ON platform_resource_intents(platform_id,platform_revision,component,resource_kind) WHERE released_at IS NULL;

CREATE TABLE platform_component_resources (
 platform_id text NOT NULL REFERENCES managed_platforms(id),
 platform_revision bigint NOT NULL,
 component text NOT NULL CHECK(length(component) BETWEEN 1 AND 63),
 resource_kind text NOT NULL CHECK(resource_kind IN ('neon_tenant','neon_timeline','runtime_component')),
 resource_id text NOT NULL CHECK(length(resource_id) BETWEEN 1 AND 255),
 immutable_generation bigint NOT NULL CHECK(immutable_generation>0),
 owner_operation_id text NOT NULL REFERENCES managed_platform_operations(id),
 intent_id text REFERENCES platform_resource_intents(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 released_at timestamptz,
	FOREIGN KEY(owner_operation_id,platform_id,platform_revision) REFERENCES managed_platform_operations(id,platform_id,revision),
	PRIMARY KEY(platform_id,platform_revision,component,resource_kind)
);
CREATE UNIQUE INDEX platform_component_resource_live_owner ON platform_component_resources(resource_kind,resource_id) WHERE released_at IS NULL;
CREATE UNIQUE INDEX platform_component_resource_intent ON platform_component_resources(intent_id) WHERE intent_id IS NOT NULL;
CREATE INDEX platform_component_resource_platform ON platform_component_resources(platform_id,platform_revision) WHERE released_at IS NULL;

CREATE FUNCTION keep_managed_platform_operation_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.platform_id<>OLD.platform_id OR NEW.revision<>OLD.revision OR NEW.identity_id<>OLD.identity_id OR NEW.key_id<>OLD.key_id OR NEW.idempotency_key<>OLD.idempotency_key OR NEW.request_hash<>OLD.request_hash OR NEW.authority_fingerprint<>OLD.authority_fingerprint OR NEW.review_id<>OLD.review_id OR NEW.kind<>OLD.kind OR NEW.desired_spec<>OLD.desired_spec OR NEW.resolved_plan<>OLD.resolved_plan OR NEW.encrypted_snapshot<>OLD.encrypted_snapshot OR NEW.review<>OLD.review THEN
  RAISE EXCEPTION 'managed platform operation intent is immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER managed_platform_operation_immutable BEFORE UPDATE ON managed_platform_operations FOR EACH ROW EXECUTE FUNCTION keep_managed_platform_operation_immutable();

INSERT INTO schema_migrations(version) VALUES(69);
