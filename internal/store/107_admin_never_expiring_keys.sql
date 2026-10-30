-- Self-hosted administrators may create API keys without an expiry date.
-- They are marked explicitly so that a Cloud-bound never-expiring key still
-- stops working when its automation binding is removed.
ALTER TABLE api_keys ADD COLUMN admin_never_expires boolean NOT NULL DEFAULT false;
