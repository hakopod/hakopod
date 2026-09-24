ALTER TABLE device_codes ADD COLUMN scope_id text NOT NULL DEFAULT '';
CREATE TABLE device_session_scopes (key_id text PRIMARY KEY REFERENCES api_keys(id) ON DELETE CASCADE, scope_id text NOT NULL);
