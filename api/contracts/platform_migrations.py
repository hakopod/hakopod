PROVIDER = {"type": "string", "enum": ["railway", "vercel"]}
TOKEN = {**S, "minLength": 8, "maxLength": 4096, "writeOnly": True, "description": "Railway account, workspace or project token, or Vercel access token. Used for this request or this migration only; never returned or logged, and erased when the migration ends or after 24 hours."}

schemas["MigrationSourceProject"] = obj({
    "id": S, "name": S,
    "team": {**S, "description": "Team or workspace name, when the provider has one."},
    "team_id": S,
    "environments": array(obj({"id": S, "name": S}, ["id", "name"])),
}, ["id", "name"])
schemas["MigrationProjectsInput"] = obj({"provider": PROVIDER, "token": TOKEN, "team_id": S}, ["provider", "token"])
schemas["MigrationInput"] = obj({
    "provider": PROVIDER, "token": TOKEN,
    "source_project_id": S, "source_environment_id": S, "source_team_id": S,
    "project": S, "environment": S,
    "application_name": {**S, "pattern": "^[a-z][a-z0-9-]{0,62}$"},
    "git_connection_id": {**S, "description": "Hakopod Git connection used for the build pull requests of services built from GitHub."},
}, ["provider", "token", "source_project_id", "project", "environment", "application_name"])
schemas["MigrationVariablesInput"] = obj({
    "values": mapping(mapping({**S, "writeOnly": True, "maxLength": 65536})),
}, ["values"])

STEP = obj({
    "name": {"type": "string", "enum": ["secrets", "databases", "data", "deploy", "builds", "domains", "finish"]},
    "status": {"type": "string", "enum": ["pending", "running", "succeeded", "failed", "skipped"]},
    "message": S,
    "items": array(obj({"name": S, "status": S, "message": S}, ["name", "status"])),
    "started_at": {**S, "format": "date-time"}, "finished_at": {**S, "format": "date-time"},
}, ["name", "status"])
schemas["MigrationStep"] = STEP
schemas["MigrationProposal"] = obj({
    "spec": ref("Spec"),
    "builds": array(obj({"service": S, "provider": S, "connection_id": S, "repository": S, "branch": S, "context_path": S, "mode": S, "preset": S, "port": I, "public": B}, ["service", "repository", "branch"])),
    "databases": array(obj({"name": S, "source": S, "engine": S, "version": S, "copy_data": B, "reason": S}, ["name", "engine", "copy_data"])),
    "bindings": array(obj({"service": S, "variable": S, "database": S}, ["service", "variable", "database"])),
    "secret_names": array(S),
    "domains": mapping(S),
    "missing": array(obj({"service": S, "name": S}, ["service", "name"])),
    "warnings": array(S),
}, ["spec", "builds", "databases", "secret_names", "domains", "missing", "warnings"])
schemas["PlatformMigration"] = obj({
    "id": S, "project": S, "environment": S, "application_name": S, "provider": PROVIDER,
    "source_project_id": S, "source_project_name": S, "source_environment_id": S, "source_team_id": S,
    "proposal": ref("MigrationProposal"),
    "status": {"type": "string", "enum": ["planned", "queued", "running", "succeeded", "failed", "cancelled"]},
    "steps": array(ref("MigrationStep")),
    "error": S, "application_id": S, "cancel_requested": B,
    "has_token": {**B, "description": "False once the source token has been erased."},
    "created_at": {**S, "format": "date-time"}, "started_at": {**S, "format": "date-time"},
    "finished_at": {**S, "format": "date-time"}, "expires_at": {**S, "format": "date-time"},
}, ["id", "project", "environment", "application_name", "provider", "source_project_id", "proposal", "status", "steps", "cancel_requested", "has_token", "created_at", "expires_at"])

route("/migrations/projects", "post", "listMigrationSourceProjects", obj({"projects": array(ref("MigrationSourceProject"))}, ["projects"]), ref("MigrationProjectsInput"))
route("/migrations", "post", "createPlatformMigration", ref("PlatformMigration"), ref("MigrationInput"), "201")
route("/migrations", "get", "listPlatformMigrations", items("PlatformMigration"), scope=True)
route("/migrations/{id}", "get", "getPlatformMigration", ref("PlatformMigration"))
route("/migrations/{id}/variables", "put", "setPlatformMigrationVariables", ref("PlatformMigration"), ref("MigrationVariablesInput"))
route("/migrations/{id}/start", "post", "startPlatformMigration", ref("PlatformMigration"), idem=True)
route("/migrations/{id}/cancel", "post", "cancelPlatformMigration", ref("PlatformMigration"))
for path, method, text, description in [
    ("/migrations/projects", "post", "List Railway or Vercel projects", "Lists the projects a Railway or Vercel token can read, with their environments. The token is used for this request only and is not stored. Requires deploy permission in some project."),
    ("/migrations", "post", "Plan a migration from Railway or Vercel", "Reads the source project with the token and returns a proposal: services, builds, databases, secret names, domains, variables the provider will not return, and warnings. Nothing is created yet. The token is sealed in the migration and erased when it ends or after 24 hours. The source project is only read."),
    ("/migrations", "get", "List migrations", "Migrations into one project environment, newest first."),
    ("/migrations/{id}", "get", "Get a migration", "The proposal, the status and the progress of each step. Never includes the source token, secret values or source database URLs."),
    ("/migrations/{id}/variables", "put", "Supply variables the provider will not return", "Values for variables marked missing in the proposal, keyed by Hakopod service then variable name. Sealed like the token; only while the migration is planned."),
    ("/migrations/{id}/start", "post", "Start a migration", "Creates secrets and databases, copies PostgreSQL and Redis data, deploys the application, opens build pull requests for services built from GitHub, and adds domains as pending. Refused while any variable is missing. Repeating the call with the same Idempotency-Key returns the same migration."),
    ("/migrations/{id}/cancel", "post", "Cancel a migration", "Cancels a planned or queued migration and erases its token, or asks a running one to stop after its current step. Resources already created stay."),
]:
    paths[path][method]["summary"] = text
    paths[path][method]["description"] = description
