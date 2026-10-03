CREATE TABLE managed_platform_maintenance (
 id text PRIMARY KEY CHECK(length(id)=32),
 platform_id text NOT NULL UNIQUE REFERENCES managed_platforms(id),
 revision bigint NOT NULL CHECK(revision>0),
 operation_id text NOT NULL REFERENCES managed_platform_operations(id),
 status text NOT NULL DEFAULT 'idle' CHECK(status IN ('idle','running')),
 phase text NOT NULL DEFAULT 'scheduled' CHECK(length(phase)<=64),
 message text NOT NULL DEFAULT '' CHECK(length(message)<=512),
 observation jsonb NOT NULL DEFAULT '{}' CHECK(octet_length(observation::text)<=65536),
 attempt integer NOT NULL DEFAULT 0 CHECK(attempt BETWEEN 0 AND 16),
 next_attempt_at timestamptz NOT NULL DEFAULT now(),
 lease text NOT NULL DEFAULT '',
 lease_until timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(operation_id,platform_id,revision) REFERENCES managed_platform_operations(id,platform_id,revision),
 CHECK((status='running')=(lease<>'' AND lease_until IS NOT NULL))
);
CREATE INDEX managed_platform_maintenance_work ON managed_platform_maintenance(next_attempt_at,platform_id) WHERE status IN ('idle','running');

CREATE FUNCTION keep_managed_platform_maintenance_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.id<>OLD.id OR NEW.platform_id<>OLD.platform_id OR NEW.created_at<>OLD.created_at THEN
  RAISE EXCEPTION 'managed platform maintenance identity is immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER managed_platform_maintenance_identity BEFORE UPDATE ON managed_platform_maintenance FOR EACH ROW EXECUTE FUNCTION keep_managed_platform_maintenance_identity();
