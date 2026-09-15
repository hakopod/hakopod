schemas["ComposeImportInput"] = obj({"project": S, "environment": S, "name": S, "yaml": {**S, "maxLength": 262144}, "variables": mapping(S), "env_files": mapping({**S, "writeOnly": True}), "application_id": S, "expected_revision": I}, ["project", "environment", "yaml"])
schemas["ComposeImport"] = obj({"spec": ref("Spec"), "toml": S, "warnings": array(S), "application_id": S, "expected_revision": I}, ["spec", "toml", "warnings", "application_id", "expected_revision"])
route("/compose/convert", "post", "convertCompose", ref("ComposeImport"), ref("ComposeImportInput"))
paths["/compose/convert"]["post"]["description"] = "Convert one bounded, image-based Docker Compose document into an editable TOML draft. Does not build or deploy. Explicit env_file uploads save sensitive values as scoped secret references before returning the draft. Existing application imports add services with revision and collision checks. Host environment and filesystem values are never read. Review warnings, then use /plan and /deployments."

for name in ["PlanInput", "DeployInput"]:
    schemas[name]["properties"]["env_files"] = mapping({**S, "writeOnly": True})
