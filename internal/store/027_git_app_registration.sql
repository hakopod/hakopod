-- Unregistered manifest drafts have no provider identity yet. Registered Apps
-- and installations remain unique, including Apps awaiting installation.
DROP INDEX git_connection_installation;
CREATE UNIQUE INDEX git_connection_installation ON git_connections(github_app_id,installation_id)
 WHERE auth_kind='github_app' AND github_app_id > 0;
