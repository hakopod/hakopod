CREATE TABLE automation_key_scopes (
    key_id text PRIMARY KEY REFERENCES api_keys(id) ON DELETE CASCADE,
    scope_id text NOT NULL CHECK(length(scope_id) BETWEEN 1 AND 128),
    binding text NOT NULL CHECK(length(binding) BETWEEN 1 AND 512)
);
CREATE INDEX automation_key_scopes_scope ON automation_key_scopes(scope_id);
