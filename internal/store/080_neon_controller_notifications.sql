CREATE TABLE managed_platform_neon_controller_state (
 platform_id text NOT NULL REFERENCES managed_platforms(id),
 platform_revision bigint NOT NULL CHECK(platform_revision>0),
 state jsonb NOT NULL CHECK(jsonb_typeof(state)='object' AND octet_length(state::text)<=16384),
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(platform_id,platform_revision)
);

-- Ready Neon runtimes need prompt compute replay with either TLS ownership
-- mode. Reuse the existing bounded maintenance scheduler and durable lease.
INSERT INTO managed_platform_maintenance(id,platform_id,revision,operation_id,next_attempt_at)
 SELECT md5('hakopod-neon-runtime-maintenance-v1:' || p.id),p.id,p.revision,o.id,clock_timestamp()+interval '30 seconds'
 FROM managed_platforms p JOIN managed_platform_operations o ON o.platform_id=p.id AND o.revision=p.revision
 WHERE p.kind='neon' AND p.status='ready' AND p.deleted_at IS NULL AND o.status='succeeded' AND o.kind IN ('create','update')
 ON CONFLICT(platform_id) DO UPDATE SET revision=EXCLUDED.revision,operation_id=EXCLUDED.operation_id,
 next_attempt_at=LEAST(managed_platform_maintenance.next_attempt_at,EXCLUDED.next_attempt_at),updated_at=now()
 WHERE managed_platform_maintenance.status='idle';

INSERT INTO schema_migrations(version) VALUES(80);
