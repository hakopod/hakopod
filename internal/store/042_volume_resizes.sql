CREATE TABLE volume_resizes (
 id text PRIMARY KEY,
 application_id text NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
 identity_id text NOT NULL REFERENCES identities(id),
 key_id text NOT NULL REFERENCES api_keys(id),
 idempotency_key text NOT NULL,
 expected_revision bigint NOT NULL,
 claim text NOT NULL,
 target_claim text NOT NULL,
 size_gib bigint NOT NULL,
 old_gib bigint NOT NULL,
 source_spec jsonb NOT NULL,
 target_spec jsonb NOT NULL,
 phase text NOT NULL DEFAULT 'queued',
 error text NOT NULL DEFAULT '',
 runtime jsonb NOT NULL DEFAULT '{}',
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(identity_id,idempotency_key)
);
CREATE UNIQUE INDEX volume_resize_maintenance ON volume_resizes(application_id)
 WHERE phase IN ('queued','copying','switching','cancelling','deleting_original');
