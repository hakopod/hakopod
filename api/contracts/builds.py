"""Remote source builds with reviewed GitHub Actions or GitLab CI workflows."""
schemas["BuildConfig"] = obj({"architecture":{"type":"string","enum":["amd64","arm64"]},"id":S,"application_id":S,"project":S,"environment":S,"name":S,"service":S,"repository":S,"branch":S,"mode":{"type":"string","enum":["dockerfile","buildpacks"]},"preset":{"type":"string","enum":["auto","nodejs","python","go","java","dotnet","ruby","static"]},"context_path":S,"dockerfile":S,"registry_credential":S,"port":I,"public":B,"size":S,"auto_build":B,"auto_deploy":B,"revision":I,"installed_revision":I,"installed_commit":S},["architecture","id","project","environment","name","service","repository","branch","mode","preset","context_path","dockerfile","port","public","size","auto_build","auto_deploy","revision","installed_revision","installed_commit"])
schemas["BuildConfig"]["properties"]["provider"]={"type":"string","enum":["github","gitlab"]}
schemas["BuildConfig"]["required"].append("provider")
schemas["BuildInput"] = obj({**{k:v for k,v in schemas["BuildConfig"]["properties"].items() if k not in {"id","revision","installed_revision","installed_commit"}},"expected_config_revision":I},["project","environment","name","repository"])
schemas["BuildRun"] = obj({"id":S,"build_id":S,"config_revision":I,"commit_sha":S,"status":S,"github_run_id":I,"conclusion":S,"image":S,"run_url":S,"message":S,"deployment_id":S,"created_at":T,"updated_at":T,"automatic":B,"auto_status":S},["id","build_id","config_revision","commit_sha","status","github_run_id","conclusion","image","run_url","message","deployment_id","created_at","updated_at","automatic","auto_status"])
schemas["BuildRun"]["properties"].update({"provider":{"type":"string","enum":["github","gitlab"]},"remote_run_id":I})
schemas["BuildRun"]["required"] += ["provider","remote_run_id"]
schemas["BuildPreview"] = obj({"config":ref("BuildConfig"),"workflow_path":S,"workflow":S,"image_repository":S,"requirements":array(S)},["config","workflow_path","workflow","image_repository","requirements"])
schemas["BuildInstalled"] = obj({"config":ref("BuildConfig"),"commit_sha":S,"workflow_path":S},["config","commit_sha","workflow_path"])
route("/builds","get","listSourceBuilds",items("BuildConfig"),scope=True)
route("/builds","post","createSourceBuild",ref("BuildConfig"),ref("BuildInput"),"201")
route("/builds/{id}","get","getSourceBuild",ref("BuildConfig"))
route("/builds/{id}","put","updateSourceBuild",ref("BuildConfig"),ref("BuildInput"))
route("/builds/{id}/preview","post","previewBuildWorkflow",ref("BuildPreview"),obj({}))
route("/builds/{id}/install","post","installBuildWorkflow",ref("BuildInstalled"),obj({"expected_config_revision":I},["expected_config_revision"]))
route("/builds/{id}/run","post","runSourceBuild",ref("BuildRun"),obj({"expected_config_revision":I,"commit":S},["expected_config_revision"]),"202",idem=True)
route("/builds/{id}/runs","get","listSourceBuildRuns",items("BuildRun"))
route("/builds/{id}/runs/{run}","get","observeSourceBuild",ref("BuildRun"))
route("/builds/{id}/runs/{run}/deploy","post","deploySourceBuild",ref("Deployment"),obj({"expected_revision":I,"expected_config_revision":I},["expected_revision","expected_config_revision"]),"202")
route("/builds/{id}/runs/{run}/cancel","post","cancelSourceBuild",obj({"status":S},["status"]),obj({}),"202")
for path in ["/builds/{id}/runs/{run}","/builds/{id}/runs/{run}/deploy","/builds/{id}/runs/{run}/cancel"]:
    for item in paths[path].values():item["parameters"].append({"name":"run","in":"path","required":True,"schema":S})

schemas["BuildDeployPlan"] = obj({**schemas["Plan"]["properties"],"expected_config_revision":I},schemas["Plan"]["required"]+["expected_config_revision"])
route("/builds/{id}/runs/{run}/plan","post","planSourceBuildDeployment",ref("BuildDeployPlan"),obj({}))
paths["/builds/{id}/runs/{run}/plan"]["post"]["parameters"].append({"name":"run","in":"path","required":True,"schema":S})
