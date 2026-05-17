schemas["Template"] = obj({"id": S, "name": S, "category": S, "description": S, "license": S, "upstream": S, "required_secrets": array(S), "requirements": array(S)}, ["id", "name", "category", "description", "license", "upstream", "required_secrets", "requirements"])
schemas["TemplatePlan"] = obj({**schemas["Plan"]["properties"], "required_secrets": array(S), "model_source": S}, [*schemas["Plan"]["required"], "required_secrets"])
route("/templates", "get", "listTemplates", items("Template"))
route("/templates/{id}/plan", "post", "planTemplate", ref("TemplatePlan"), obj({"project": S, "environment": S, "name": S, "public": B, "storage_gib": I, "model": S, "model_revision": S}, ["project", "environment", "name"]))
