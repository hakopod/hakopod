-- A hostname that reaches the installation only by address (an apex ALIAS or
-- flattened CNAME) must also publish this token at _hakopod-challenge.<host>.
-- The shared ingress address proves no intent for one particular service.
-- Rows created before this migration get no token, so only a CNAME verifies them.
ALTER TABLE custom_hostnames ADD COLUMN verification_token text NOT NULL DEFAULT '';
