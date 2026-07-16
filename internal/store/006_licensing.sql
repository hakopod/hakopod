CREATE TABLE installation_license (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
 installation_id text NOT NULL DEFAULT replace(gen_random_uuid()::text,'-',''),
 token text NOT NULL DEFAULT '' CHECK(length(token)<=16384),
 revision bigint NOT NULL DEFAULT 0,
 highest_sequence bigint NOT NULL DEFAULT 0,
 token_digest bytea,
 updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO installation_license(singleton) VALUES(true);
