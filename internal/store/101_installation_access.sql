-- Private management access: one installation-wide network policy for the
-- dashboard, API, CLI, SDK and MCP. A save that enables or narrows the policy
-- keeps the previous one until it is confirmed from an allowed network; an
-- unconfirmed save is restored to previous after pending_until.
CREATE TABLE installation_access_policy (
 singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
 revision bigint NOT NULL CHECK (revision > 0),
 policy jsonb NOT NULL,
 previous jsonb,
 pending_until timestamptz,
 updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK ((previous IS NULL) = (pending_until IS NULL))
);
