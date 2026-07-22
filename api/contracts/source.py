schemas["GitHubStatus"] = obj({"configured": B, "token_configured": B, "webhook_path": S, "webhook_secret": S, "private_repositories": B}, ["configured", "token_configured", "webhook_path"])
schemas["SourceBinding"] = obj({"application_id": S, "provider": {"type":"string","enum":["github","gitlab"]}, "repository": S, "branch": S, "path": S, "auto_deploy": B, "revision": I, "last_commit": S, "last_deployment": S, "last_error": S, "updated_at": T}, ["application_id", "provider", "repository", "branch", "path", "auto_deploy", "revision", "last_commit", "last_deployment", "last_error", "updated_at"])
schemas["SourceStatus"] = obj({"connected": B, "source": ref("SourceBinding")}, ["connected"])
schemas["SourcePlan"] = obj({**schemas["Plan"]["properties"], "commit_sha": S, "expected_source_revision": I}, [*schemas["Plan"]["required"], "commit_sha", "expected_source_revision"])
route("/integrations/github", "get", "githubStatus", ref("GitHubStatus"))
route("/integrations/github", "put", "configureGitHub", ref("GitHubStatus"), obj({"token": S, "webhook_secret": S}))
route("/applications/{id}/source", "get", "getSource", ref("SourceStatus"))
route("/applications/{id}/source", "put", "setSource", ref("SourceStatus"), obj({"provider": {"type":"string","enum":["github","gitlab"],"default":"github"}, "repository": S, "branch": S, "path": S, "auto_deploy": B, "expected_source_revision": I}, ["repository", "branch", "path", "auto_deploy", "expected_source_revision"]))
route("/applications/{id}/source/plan", "post", "planSource", ref("SourcePlan"), obj({}))
route("/applications/{id}/source/deploy", "post", "deploySource", ref("Deployment"), obj({"expected_revision": I, "expected_source_revision": I, "commit_sha": S}, ["expected_revision", "expected_source_revision", "commit_sha"]), "202", idem=True)
route("/webhooks/github", "post", "githubWebhook", obj({"accepted": B, "ignored": B}), obj({}), "202")
paths["/webhooks/github"]["post"]["security"] = []
paths["/webhooks/github"]["post"]["description"] = "GitHub push webhook. Requires X-Hub-Signature-256 HMAC, X-GitHub-Delivery and X-GitHub-Event. Bounded durable inbox with retry and duplicate suppression."

schemas["GitLabStatus"] = schemas["GitHubStatus"]
route("/integrations/gitlab", "get", "gitlabStatus", ref("GitLabStatus"))
route("/integrations/gitlab", "put", "configureGitLab", ref("GitLabStatus"), obj({"token": S, "webhook_secret": S}))
route("/webhooks/gitlab", "post", "gitlabWebhook", obj({"accepted": B, "ignored": B}), obj({}), "202")
paths["/webhooks/gitlab"]["post"]["security"] = []
paths["/webhooks/gitlab"]["post"]["description"] = "GitLab.com push webhook authenticated with X-Gitlab-Token and deduplicated by X-Gitlab-Event-UUID. Provider-separated bounded durable inbox."
