ALTER TABLE managed_database_public_endpoints
 ADD COLUMN member_allocations jsonb NOT NULL DEFAULT '[]'
 CHECK(jsonb_typeof(member_allocations)='array' AND jsonb_array_length(member_allocations)<=48);

-- Keep reservations after a partial route or provider cleanup. Only the final
-- endpoint tombstone releases the complete allocation set.
CREATE TABLE managed_database_public_endpoint_allocations (
 endpoint_id text NOT NULL REFERENCES managed_database_public_endpoints(id),
 allocation_id text NOT NULL, member_name text NOT NULL, member_uid text NOT NULL,
 host text NOT NULL, address inet NOT NULL, port integer NOT NULL CHECK(port BETWEEN 1 AND 65535),
 PRIMARY KEY(endpoint_id,allocation_id), UNIQUE(endpoint_id,member_name),
 UNIQUE(allocation_id), UNIQUE(host), UNIQUE(address,port)
);

CREATE FUNCTION maintain_database_public_endpoint_allocations() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
 entries jsonb; entry jsonb; item jsonb; member text; uid text; previous_member text := '';
BEGIN
 IF TG_OP='UPDATE' AND (NEW.allocation<>OLD.allocation OR NEW.member_allocations<>OLD.member_allocations) THEN
  RAISE EXCEPTION 'public endpoint allocations are immutable; revoke and review again';
 END IF;
 DELETE FROM managed_database_public_endpoint_allocations WHERE endpoint_id=NEW.id;
 IF NEW.revoked_at IS NOT NULL THEN RETURN NEW; END IF;
 IF jsonb_array_length(NEW.member_allocations)=0 THEN
  entries:=jsonb_build_array(jsonb_build_object('member_name','','member_uid','','allocation',NEW.allocation));
 ELSE
  IF NEW.member_allocations->0->'allocation'<>NEW.allocation THEN
   RAISE EXCEPTION 'public member allocation disagrees with primary allocation';
  END IF;
  entries:=NEW.member_allocations;
 END IF;
 FOR entry IN SELECT value FROM jsonb_array_elements(entries) LOOP
  member:=entry->>'member_name'; uid:=entry->>'member_uid'; item:=entry->'allocation';
  IF jsonb_typeof(item) IS DISTINCT FROM 'object' OR member IS NULL OR uid IS NULL OR
   (jsonb_array_length(NEW.member_allocations)>0 AND (member !~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$' OR member<=previous_member OR length(uid) NOT BETWEEN 1 AND 128 OR uid ~ '[[:space:][:cntrl:]]')) OR
   jsonb_typeof(item->'id') IS DISTINCT FROM 'string' OR length(item->>'id') NOT BETWEEN 1 AND 64 OR
   jsonb_typeof(item->'host') IS DISTINCT FROM 'string' OR length(item->>'host') NOT BETWEEN 1 AND 253 OR
   jsonb_typeof(item->'address') IS DISTINCT FROM 'string' OR family((item->>'address')::inet)<>4 OR masklen((item->>'address')::inet)<>32 OR
   jsonb_typeof(item->'port') IS DISTINCT FROM 'number' THEN
   RAISE EXCEPTION 'invalid public member allocation';
  END IF;
  INSERT INTO managed_database_public_endpoint_allocations(endpoint_id,allocation_id,member_name,member_uid,host,address,port)
   VALUES(NEW.id,item->>'id',member,uid,item->>'host',(item->>'address')::inet,(item->>'port')::integer);
  previous_member:=member;
 END LOOP;
 RETURN NEW;
END $$;

CREATE TRIGGER managed_database_public_endpoint_allocation_inventory
 AFTER INSERT OR UPDATE OF allocation,member_allocations,revoked_at ON managed_database_public_endpoints
 FOR EACH ROW EXECUTE FUNCTION maintain_database_public_endpoint_allocations();

INSERT INTO managed_database_public_endpoint_allocations(endpoint_id,allocation_id,member_name,member_uid,host,address,port)
 SELECT id,allocation->>'id','','',allocation->>'host',(allocation->>'address')::inet,(allocation->>'port')::integer
 FROM managed_database_public_endpoints WHERE revoked_at IS NULL;

INSERT INTO schema_migrations(version) VALUES(68);
