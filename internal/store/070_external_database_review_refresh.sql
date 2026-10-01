ALTER TABLE external_database_reviews DROP CONSTRAINT external_database_reviews_kind_check;
ALTER TABLE external_database_reviews ADD CONSTRAINT external_database_reviews_kind_check CHECK(kind IN ('connect','disconnect','refresh'));
INSERT INTO schema_migrations(version) VALUES(70);
