ALTER TABLE managed_database_public_endpoint_operations
 ADD COLUMN identity_transition jsonb,
 ADD CONSTRAINT managed_database_public_endpoint_identity_transition_shape CHECK (
  identity_transition IS NULL OR (
   jsonb_typeof(identity_transition) = 'object'
   AND identity_transition->>'engine' = 'oracle'
   AND identity_transition->>'schema_version' = '1'
   AND pg_column_size(identity_transition) <= 32768
  )
 );

INSERT INTO schema_migrations(version) VALUES(66);
