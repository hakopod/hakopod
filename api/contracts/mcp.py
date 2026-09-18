schemas["BuildProvenance"] = obj({"build_id":S,"run_id":S,"commit_sha":S,"image":S,"provider":S,"repository":S,"branch":S,"run_url":S,"created_at":T},["build_id","run_id","commit_sha","image","provider","repository","branch","run_url","created_at"])
schemas["ServiceProvenance"] = obj({"configured_image":S,"accepted_image":S,"commit_sha":S,"source_status":{"type":"string","enum":["unknown","matched_build","ambiguous","incomplete"]},"builds":array(ref("BuildProvenance"))},["configured_image","source_status","builds"])
schemas["ApplicationProvenance"] = obj({"application_id":S,"revision":I,"services":mapping(ref("ServiceProvenance")),"truncated":B,"note":S},["application_id","revision","services","truncated","note"])
route("/applications/{id}/provenance","get","applicationProvenance",ref("ApplicationProvenance"))
route("/mcp","post","mcpMessage",{},obj({"jsonrpc":S,"id":{},"method":S,"params":{}},["jsonrpc","method"]),scope=True)
route("/mcp","delete","closeMCPSession",{},"","204",scope=True)
paths["/mcp"]["delete"]["responses"]["204"]={"description":"Session closed"}
for method in ["post","delete"]:
    operation=paths["/mcp"][method]
    operation["description"]="Self-hosted Streamable HTTP MCP. Requires a project/environment-scoped machine bearer key. Initialize first, then retain Mcp-Session-Id and negotiated MCP-Protocol-Version. JSON responses; GET/SSE is not offered. See docs/http-mcp.md."
    operation["parameters"] += [{"name":"allow_deploy","in":"query","schema":B},{"name":"Mcp-Session-Id","in":"header","required":method=="delete","schema":S},{"name":"MCP-Protocol-Version","in":"header","schema":S}]
paths["/mcp"]["post"]["responses"]["200"]["headers"]={"Mcp-Session-Id":{"description":"Returned by initialize; required on subsequent requests","schema":S}}
