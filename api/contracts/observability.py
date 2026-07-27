schemas["LogEntry"] = obj({"timestamp": T, "pod": S, "service": S, "container": S, "message": S, "severity": S, "fields": mapping({})}, ["timestamp", "pod", "service", "container", "message", "severity", "fields"])
schemas["LogQuery"] = obj({"service": S, "pod": S, "container": S, "query": S, "since_seconds": I, "limit": I, "tail": I, "previous": B}, ["service"])
schemas["LogQueryResult"] = obj({"entries": array(ref("LogEntry")), "histogram": array(obj({"timestamp": T, "count": I}, ["timestamp", "count"])), "scanned": I, "matched": I, "truncated": B, "pods": I, "window_seconds": I, "warnings": array(S)}, ["entries", "histogram", "scanned", "matched", "truncated", "pods", "window_seconds", "warnings"])
route("/applications/{id}/logs/query", "post", "queryLogs", ref("LogQueryResult"), ref("LogQuery"))
schemas["TerminalInput"] = obj({"pod": S, "container": S, "command": array(S), "cols": I, "rows": I}, ["pod"])
schemas["TerminalSession"] = obj({"id": S, "pod": S, "container": S, "expires_at": T}, ["id", "pod", "container", "expires_at"])
base = "/applications/{id}/services/{service}/terminal"
route(base, "post", "createTerminal", ref("TerminalSession"), ref("TerminalInput"), "201")
route(base+"/{session}/output", "get", "terminalOutput", S)
paths[base+"/{session}/output"]["get"]["responses"]["200"]["content"] = {"text/event-stream": {"schema": S}}
paths[base+"/{session}/output"]["get"]["description"] = "One SSE reader; data JSON is {type:output,data:base64} or {type:exit,code:number,message:string}. Human deployment writer only. Four sessions, ten-minute lifetime, two-minute input-idle timeout; authority rechecked every five seconds."
route(base+"/{session}/input", "post", "terminalInput", obj({}), obj({"data": S, "cols": I, "rows": I}), "204")
route(base+"/{session}", "delete", "deleteTerminal", obj({}), status="204")
for endpoint in [base,base+"/{session}/output",base+"/{session}/input",base+"/{session}"]:
    for item in paths[endpoint].values():
        item["parameters"].append({"name":"service","in":"path","required":True,"schema":S})
        if "{session}" in endpoint: item["parameters"].append({"name":"session","in":"path","required":True,"schema":S})
        if "204" in item["responses"]: item["responses"]["204"].pop("content",None)
