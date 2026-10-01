ALTER TABLE api_keys ALTER COLUMN expires_at DROP NOT NULL;
ALTER TABLE api_keys ADD COLUMN never_expires boolean NOT NULL DEFAULT false;
ALTER TABLE api_keys ADD CONSTRAINT api_keys_expiry_shape CHECK (
 (never_expires=false AND expires_at IS NOT NULL)
 OR (never_expires=true AND expires_at IS NULL AND kind='machine')
);
INSERT INTO schema_migrations(version) VALUES(67);
