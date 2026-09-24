CREATE TABLE retained_application_data (
 application_id text PRIMARY KEY,
 project text NOT NULL,
 environment text NOT NULL,
 name text NOT NULL,
 status text NOT NULL DEFAULT 'retained' CHECK(status IN ('retained','deleting','deleted')),
 requested_key_id text REFERENCES api_keys(id),
 requested_at timestamptz,
 attempted_at timestamptz,
 error text NOT NULL DEFAULT ''
);
INSERT INTO retained_application_data(application_id,project,environment,name)
 SELECT resource_id,project,environment,name FROM retired_resource_names WHERE kind='application';
CREATE INDEX retained_application_scope ON retained_application_data(project,environment,status);
