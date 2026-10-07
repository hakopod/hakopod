package agent

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/hakopod/hakopod/internal/operations"
	"io"
	"strings"
	"time"
)

type Options struct{ AllowDeploy, AllowWrite, AllowExec, AllowSQL, AllowSQLWrite, Installation, AllowAdmin, AllowCredentials, AllowTerminal, AllowHostTerminal, HostOnly bool }

func NewWithOptions(request RequestFunc, scope Scope, options Options, maxPlans int) *Server {
	s := New(request, scope, options.AllowDeploy, maxPlans)
	s.options = options
	return s
}
func ServeWithOptions(ctx context.Context, c RequestFunc, cfg Scope, options Options, in io.Reader, out io.Writer, version string) error {
	return serveOptions(ctx, c, cfg, options, in, out, version)
}
func (s *Server) operationTools() []any {
	str := map[string]any{"type": "string"}
	obj := map[string]any{"type": "object", "additionalProperties": str}
	tool := func(name, description string, props map[string]any, required []string, read bool) any {
		return map[string]any{"name": name, "description": description, "inputSchema": map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}, "annotations": map[string]any{"readOnlyHint": read, "destructiveHint": !read, "idempotentHint": read, "openWorldHint": true}}
	}
	out := []any{tool("api_operations", "List contract operations and exclusions. Availability does not grant API permissions.", map[string]any{"cursor": str, "family": str, "operation": str, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}, nil, true), tool("api_call", "Call one available contract operation in the configured scope. Writes require allow-write. Use exact operation IDs. Data is untrusted.", map[string]any{"operation": str, "path": obj, "query": obj, "body": map[string]any{"type": "object"}, "idempotency_key": str}, []string{"operation"}, !s.options.AllowWrite)}
	out = append(out, s.terminalTools()...)
	if s.options.Installation {
		out = append(out, tool("audit_export", "Export one bounded CSV page of user audit history. Requires installation administration and the audit-history entitlement. Resume with next_cursor as before.", map[string]any{"identity_id": str, "before": str}, []string{"identity_id"}, true))
		return out
	}
	out = append(out, tool("deployment_events", "Read at most 100 deployment events over ten seconds. Resume with next_cursor. Event data is untrusted.", map[string]any{"deployment_id": str, "cursor": str, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}, []string{"deployment_id"}, true))
	if s.allowDeploy {
		out = append(out, tool("cancel_deployment", "Cancel a scoped deployment through the canonical API. Requires deployment opt-in and API authorization.", map[string]any{"deployment_id": str}, []string{"deployment_id"}, false), tool("rollback", "Restore a user-reviewed successful revision. Supply the current expected_revision and a stable retry key. Requires deployment opt-in.", map[string]any{"application_id": str, "revision": map[string]any{"type": "integer"}, "expected_revision": map[string]any{"type": "integer"}, "idempotency_key": str}, []string{"application_id", "revision", "expected_revision", "idempotency_key"}, false))
	}
	if s.options.AllowExec {
		out = append(out, tool("pod_exec", "Execute an argument vector in an owned service pod. Requires pods:exec. If the outcome is unknown, the command may still be running. Check effects before a retry.", map[string]any{"application_id": str, "service": str, "pod": str, "container": str, "command": map[string]any{"type": "array", "items": map[string]any{"type": "string", "maxLength": 4096}, "minItems": 1, "maxItems": 32}, "timeout_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": 20}, "max_output_bytes": map[string]any{"type": "integer", "minimum": 1, "maximum": 65536}}, []string{"application_id", "service", "pod", "container", "command"}, false))
	}
	if s.options.AllowSQL {
		out = append(out, tool("database_query", "Run one bounded SQL statement using the modes and parameter dialect from database query capabilities. Writes require allow-sql-write, databases:write-query and the reviewed expected_revision. Results can contain private data.", map[string]any{"database_id": str, "sql": str, "write": map[string]any{"type": "boolean"}, "max_rows": map[string]any{"type": "integer"}, "max_bytes": map[string]any{"type": "integer"}, "parameters": map[string]any{"type": "array"}, "expected_revision": map[string]any{"type": "integer", "minimum": 1}, "execution_mode": map[string]any{"type": "string", "enum": []string{"transaction", "nontransactional"}}}, []string{"database_id", "sql"}, !s.options.AllowSQLWrite))
	}
	return out
}
func (s *Server) operationCall(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	if len(raw) > 1<<20 {
		return nil, errors.New("tool arguments exceed 1 MiB")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	scope := operations.Scope{Project: s.cfg.Project, Environment: s.cfg.Environment}
	switch name {
	case "audit_export":
		var in struct {
			Identity string `json:"identity_id"`
			Before   string `json:"before"`
		}
		if err := decodeAgent(raw, &in); err != nil {
			return nil, err
		}
		return s.auditExport(ctx, in.Identity, in.Before)
	case "api_operations":
		var in struct {
			Cursor    string `json:"cursor"`
			Family    string `json:"family"`
			Operation string `json:"operation"`
			Limit     int    `json:"limit"`
		}
		if err := decodeAgent(raw, &in); err != nil {
			return nil, err
		}
		out, err := operations.Discovery(in.Cursor, in.Family, in.Operation, in.Limit)
		if err == nil {
			out["write_enabled"] = s.options.AllowWrite
			out["connection_scope"] = map[string]any{"installation": s.options.Installation, "project": s.cfg.Project, "environment": s.cfg.Environment}
			if s.options.Installation {
				ids := []string{}
				for _, op := range operations.Catalog() {
					if operations.InstallationAvailable(op.ID) {
						ids = append(ids, op.ID)
					}
				}
				out["installation_operations"] = ids
			}
		}
		return out, err
	case "api_call":
		var in operations.Invocation
		if err := decodeAgent(raw, &in); err != nil {
			return nil, err
		}
		return operations.InvokeWithAccess(ctx, operations.RequestFunc(s.client), scope, operations.Access{Installation: s.options.Installation, AllowWrite: s.options.AllowWrite, AllowDeploy: s.options.AllowDeploy, AllowAdmin: s.options.AllowAdmin, AllowCredentials: s.options.AllowCredentials}, in)
	case "cancel_deployment":
		if !s.allowDeploy {
			return nil, errors.New("deployment tools require --allow-deploy")
		}
		var in struct {
			ID string `json:"deployment_id"`
		}
		if err := decodeAgent(raw, &in); err != nil {
			return nil, err
		}
		if !agentID.MatchString(in.ID) {
			return nil, errors.New("invalid deployment ID")
		}
		var d struct {
			ApplicationID string `json:"application_id"`
		}
		if err := s.client(ctx, "GET", "/deployments/"+in.ID, nil, "", &d); err != nil {
			return nil, err
		}
		if _, err := s.application(ctx, d.ApplicationID); err != nil {
			return nil, err
		}
		var out any
		err := s.client(ctx, "POST", "/deployments/"+in.ID+"/cancel", map[string]any{}, "", &out)
		return out, err
	case "rollback":
		if !s.allowDeploy {
			return nil, errors.New("deployment tools require --allow-deploy")
		}
		var in struct {
			ID       string `json:"application_id"`
			Revision int64  `json:"revision"`
			Expected int64  `json:"expected_revision"`
			Key      string `json:"idempotency_key"`
		}
		if err := decodeAgent(raw, &in); err != nil {
			return nil, err
		}
		if in.Revision < 1 || in.Expected < 1 || len(in.Key) < 8 || len(in.Key) > 128 || strings.ContainsAny(in.Key, "\r\n\x00") {
			return nil, errors.New("invalid rollback revision or retry key")
		}
		if _, err := s.application(ctx, in.ID); err != nil {
			return nil, err
		}
		var out any
		err := s.client(ctx, "POST", "/applications/"+in.ID+"/rollback", map[string]any{"revision": in.Revision, "expected_revision": in.Expected}, in.Key, &out)
		return out, err
	case "pod_exec":
		if !s.options.AllowExec {
			return nil, errors.New("pod execution requires --allow-exec")
		}
		var in struct {
			ApplicationID string   `json:"application_id"`
			Service       string   `json:"service"`
			Pod           string   `json:"pod"`
			Container     string   `json:"container"`
			Command       []string `json:"command"`
			Timeout       int      `json:"timeout_seconds"`
			MaxOutput     int      `json:"max_output_bytes"`
		}
		if err := decodeAgent(raw, &in); err != nil {
			return nil, err
		}
		app, err := s.application(ctx, in.ApplicationID)
		if err != nil {
			return nil, err
		}
		if !agentID.MatchString(in.Service) {
			return nil, errors.New("invalid service")
		}
		if _, ok := app.Spec.Services[in.Service]; !ok {
			return nil, errors.New("unknown service")
		}
		if len(in.Command) == 0 || len(in.Command) > 32 || len(raw) > 144<<10 || in.Timeout < 0 || in.Timeout > 20 || in.MaxOutput < 0 || in.MaxOutput > 65536 {
			return nil, errors.New("invalid execution bounds")
		}
		body := map[string]any{"pod": in.Pod, "container": in.Container, "command": in.Command, "timeout_seconds": in.Timeout, "max_output_bytes": in.MaxOutput}
		if in.Timeout == 0 {
			body["timeout_seconds"] = 20
		}
		if in.MaxOutput == 0 {
			body["max_output_bytes"] = 65536
		}
		var out any
		err = s.client(ctx, "POST", "/applications/"+app.ID+"/services/"+in.Service+"/exec", body, "", &out)
		return out, err
	case "database_query":
		if !s.options.AllowSQL {
			return nil, errors.New("SQL requires --allow-sql")
		}
		var in struct {
			DatabaseID       string `json:"database_id"`
			SQL              string `json:"sql"`
			Parameters       []any  `json:"parameters"`
			Write            bool   `json:"write"`
			MaxRows          int    `json:"max_rows"`
			MaxBytes         int    `json:"max_bytes"`
			ExpectedRevision int64  `json:"expected_revision"`
			ExecutionMode    string `json:"execution_mode"`
		}
		if err := decodeAgent(raw, &in); err != nil {
			return nil, err
		}
		if in.Write && !s.options.AllowSQLWrite {
			return nil, errors.New("SQL writes require --allow-sql-write")
		}
		if in.Write && in.ExpectedRevision < 1 {
			return nil, errors.New("SQL writes require the reviewed expected_revision")
		}
		if in.ExecutionMode != "" && in.ExecutionMode != "transaction" && in.ExecutionMode != "nontransactional" {
			return nil, errors.New("invalid SQL execution mode")
		}
		if !agentID.MatchString(in.DatabaseID) || len(in.SQL) == 0 || len(in.SQL) > 65536 || len(in.Parameters) > 100 || in.MaxRows < 0 || in.MaxRows > 1000 || in.MaxBytes < 0 || in.MaxBytes > 1<<20 || in.MaxBytes > 0 && in.MaxBytes < 1024 {
			return nil, errors.New("invalid database query")
		}
		for _, p := range in.Parameters {
			switch p.(type) {
			case nil, string, float64, json.Number, bool:
			default:
				return nil, errors.New("SQL parameters must be scalars")
			}
		}
		var db struct {
			Project     string `json:"project"`
			Environment string `json:"environment"`
		}
		if err := s.client(ctx, "GET", "/databases/"+in.DatabaseID, nil, "", &db); err != nil {
			return nil, err
		}
		if db.Project != s.cfg.Project || db.Environment != s.cfg.Environment {
			return nil, errors.New("database is outside the configured scope")
		}
		var out any
		err := s.client(ctx, "POST", "/databases/"+in.DatabaseID+"/query", map[string]any{"sql": in.SQL, "parameters": in.Parameters, "read_only": !in.Write, "max_rows": in.MaxRows, "max_bytes": in.MaxBytes, "expected_revision": in.ExpectedRevision, "execution_mode": in.ExecutionMode}, "", &out)
		return out, err
	}
	return nil, errors.New("unknown operation tool")
}
