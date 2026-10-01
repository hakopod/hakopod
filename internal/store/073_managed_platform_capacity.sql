ALTER TABLE managed_platforms
 ADD COLUMN reserved_cpu_milli bigint NOT NULL DEFAULT 0 CHECK(reserved_cpu_milli>=0),
 ADD COLUMN reserved_memory_bytes bigint NOT NULL DEFAULT 0 CHECK(reserved_memory_bytes>=0),
 ADD COLUMN reserved_storage_gib bigint NOT NULL DEFAULT 0 CHECK(reserved_storage_gib>=0);

CREATE TABLE managed_capacity_scopes (
 project text NOT NULL,
 environment text NOT NULL,
 capacity_pool text NOT NULL CHECK(length(capacity_pool) BETWEEN 1 AND 63),
 PRIMARY KEY(project,environment),
 FOREIGN KEY(project,environment) REFERENCES environments(project,name)
);
CREATE INDEX managed_capacity_scope_pool ON managed_capacity_scopes(capacity_pool,project,environment);

CREATE TABLE managed_capacity_policies (
 capacity_pool text PRIMARY KEY CHECK(length(capacity_pool) BETWEEN 1 AND 63),
 policy_fingerprint bytea NOT NULL CHECK(octet_length(policy_fingerprint)=32)
);

CREATE FUNCTION keep_managed_capacity_policy_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'managed capacity policy is immutable; use a new capacity pool';
END $$;
CREATE TRIGGER managed_capacity_policy_immutable BEFORE UPDATE OR DELETE ON managed_capacity_policies FOR EACH ROW EXECUTE FUNCTION keep_managed_capacity_policy_immutable();

CREATE TABLE managed_platform_capacity_reservations (
 platform_id text NOT NULL REFERENCES managed_platforms(id),
 capacity_pool text NOT NULL CHECK(length(capacity_pool) BETWEEN 1 AND 63),
 reservation_key text NOT NULL CHECK(length(reservation_key) BETWEEN 1 AND 127),
 node_name text NOT NULL CHECK(length(node_name) BETWEEN 1 AND 253),
 cpu_milli bigint NOT NULL CHECK(cpu_milli>=0),
 memory_bytes bigint NOT NULL CHECK(memory_bytes>=0),
 storage_gib bigint NOT NULL CHECK(storage_gib>=0),
 PRIMARY KEY(platform_id,reservation_key,node_name),
 CHECK(cpu_milli>0 OR memory_bytes>0 OR storage_gib>0)
);
CREATE INDEX managed_platform_capacity_node ON managed_platform_capacity_reservations(capacity_pool,node_name,platform_id);

INSERT INTO schema_migrations(version) VALUES(73);
