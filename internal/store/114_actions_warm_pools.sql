-- A VM slot's instance came from a cold launch or a stopped standby. An
-- instance belongs to one slot at most: claiming a standby races on this index.
ALTER TABLE actions_slots ADD COLUMN instance_source text NOT NULL DEFAULT '' CHECK(instance_source IN ('','cold','standby'));
CREATE UNIQUE INDEX actions_slots_instance ON actions_slots(instance_id) WHERE instance_id<>'';
-- One hakopod-minimal image per capacity, built in the customer's account by
-- the Actions worker. A build moves through builder_id, then image_id.
CREATE TABLE actions_capacity_images (
 capacity text PRIMARY KEY CHECK(capacity ~ '^[a-z][a-z0-9-]{0,62}$'),
 state text NOT NULL CHECK(state IN ('building','available','failed')),
 builder_id text NOT NULL DEFAULT '' CHECK(length(builder_id)<=64),
 building_image_id text NOT NULL DEFAULT '' CHECK(length(building_image_id)<=64),
 image_id text NOT NULL DEFAULT '' CHECK(length(image_id)<=64),
 message text NOT NULL DEFAULT '' CHECK(length(message)<=512),
 requested_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
