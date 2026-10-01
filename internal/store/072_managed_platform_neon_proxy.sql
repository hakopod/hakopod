CREATE TABLE managed_platform_neon_proxy_endpoints (
 endpoint_id text PRIMARY KEY CHECK(length(endpoint_id)=32),
 platform_id text NOT NULL REFERENCES managed_platforms(id),
 platform_revision bigint NOT NULL CHECK(platform_revision>0),
 owner_operation_id text NOT NULL REFERENCES managed_platform_operations(id),
 generation bigint NOT NULL CHECK(generation>0),
 enabled boolean NOT NULL DEFAULT false,
 address text NOT NULL CHECK(length(address) BETWEEN 3 AND 255),
 server_name text NOT NULL CHECK(length(server_name) BETWEEN 1 AND 253),
 project_id text NOT NULL CHECK(length(project_id) BETWEEN 1 AND 63),
 branch_id text NOT NULL CHECK(length(branch_id) BETWEEN 1 AND 63),
 compute_id text NOT NULL CHECK(length(compute_id) BETWEEN 1 AND 63),
 encrypted_roles bytea NOT NULL CHECK(octet_length(encrypted_roles) BETWEEN 29 AND 65536),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
	FOREIGN KEY(owner_operation_id,platform_id,platform_revision) REFERENCES managed_platform_operations(id,platform_id,revision),
	CHECK(endpoint_id=platform_id AND project_id=platform_id AND generation=platform_revision)
);
CREATE INDEX managed_platform_neon_proxy_platform ON managed_platform_neon_proxy_endpoints(platform_id,platform_revision);

INSERT INTO schema_migrations(version) VALUES(72);
