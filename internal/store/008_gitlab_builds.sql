CREATE INDEX build_configs_gitlab_repository ON build_configs ((config->>'repository'),(config->>'branch')) WHERE config->>'provider'='gitlab';
