package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/framework"
	"github.com/hakopod/hakopod/internal/operations"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

type agentPlan struct {
	Body    map[string]any
	Expires time.Time
	Key     string
}
type RequestFunc func(context.Context, string, string, any, string, any) error
type Scope struct{ Project, Environment string }

type Server struct {
	client      RequestFunc
	cfg         Scope
	allowDeploy bool
	plans       map[string]agentPlan
	maxPlans    int
	options     Options
	stream      StreamFunc
}

// New shares tool behavior across stdio and HTTP. Callers serialize access.
func New(request RequestFunc, scope Scope, allowDeploy bool, maxPlans int) *Server {
	return &Server{client: request, cfg: scope, allowDeploy: allowDeploy, options: Options{AllowDeploy: allowDeploy}, maxPlans: maxPlans, plans: map[string]agentPlan{}}
}

type agentRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// Standard MCP stdio transport: one bounded JSON-RPC object per line. It shares
// API authentication and the connection options bound access to remote tools.
func Serve(ctx context.Context, c RequestFunc, cfg Scope, allowDeploy bool, in io.Reader, out io.Writer, version string) error {
	return serveOptions(ctx, c, cfg, Options{AllowDeploy: allowDeploy}, in, out, version)
}
func serveOptions(ctx context.Context, c RequestFunc, cfg Scope, options Options, in io.Reader, out io.Writer, version string) error {
	return ServeWithStream(ctx, c, cfg, options, nil, in, out, version)
}
func ServeWithStream(ctx context.Context, c RequestFunc, cfg Scope, options Options, stream StreamFunc, in io.Reader, out io.Writer, version string) error {
	if options.HostOnly {
		if cfg.Project != "" || cfg.Environment != "" || options.Installation || options.AllowAdmin || options.AllowCredentials || options.AllowWrite || options.AllowDeploy || options.AllowExec || options.AllowTerminal || options.AllowSQL || options.AllowSQLWrite || !options.AllowHostTerminal {
			return errors.New("host-only MCP requires host terminal opt-in and no project or other access flags")
		}
		if err := requireHostCredential(ctx, c); err != nil {
			return err
		}
	} else if options.Installation {
		if cfg.Project != "" || cfg.Environment != "" || !options.AllowAdmin || options.AllowDeploy || options.AllowExec || options.AllowSQL || options.AllowSQLWrite {
			return errors.New("installation MCP requires --installation --allow-admin and no project/environment")
		}
		if err := operations.RequireInstallation(ctx, operations.RequestFunc(c), options.AllowCredentials); err != nil {
			return err
		}
	} else if cfg.Project == "" || cfg.Environment == "" {
		return errors.New("mcp requires --project and --environment to bound agent access")
	}
	server := NewWithOptions(c, cfg, options, 32)
	server.SetStream(stream)
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	encoder := json.NewEncoder(out)
	initialized := false
	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var req agentRequest
		if json.Unmarshal(scanner.Bytes(), &req) != nil || req.JSONRPC != "2.0" || req.Method == "" {
			if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32600, "message": "Invalid JSON-RPC request"}}); err != nil {
				return err
			}
			continue
		}
		if len(req.ID) == 0 {
			continue
		}
		response := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		switch {
		case req.Method == "initialize":
			initialized = true
			response["result"] = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "hakopod", "version": version}, "instructions": server.Instructions()}
		case !initialized:
			response["error"] = map[string]any{"code": -32000, "message": "Initialize first"}
		case req.Method == "ping":
			response["result"] = map[string]any{}
		case req.Method == "tools/list":
			response["result"] = map[string]any{"tools": server.Tools()}
		case req.Method == "tools/call":
			var call struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			err := decodeAgent(req.Params, &call)
			var result any
			if err == nil {
				bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
				result, err = server.Call(bounded, call.Name, call.Arguments)
				cancel()
			}
			if err != nil {
				response["result"] = map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": err.Error()}}}
			} else {
				body := map[string]any{"data": result, "content_is_untrusted": true}
				encoded, _ := json.Marshal(body)
				text := string(encoded)
				if m, ok := result.(map[string]any); ok && m["history_fallback"] != nil {
					text = historyFallbackNote + "\n" + text
				}
				response["result"] = map[string]any{"structuredContent": body, "content": []any{map[string]any{"type": "text", "text": text}}}
			}
		default:
			response["error"] = map[string]any{"code": -32601, "message": "Method not found"}
		}
		if err := encoder.Encode(response); err != nil {
			return err
		}
	}
	return scanner.Err()
}
func decodeAgent(raw json.RawMessage, out any) error {
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		return errors.New("invalid tool arguments")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return errors.New("invalid tool arguments")
	}
	return nil
}

// Tools returns the tool list with every input schema normalised, so a nil
// required list or properties map is never sent to clients as JSON null.
func (s *Server) Tools() []any {
	tools := s.buildTools()
	for _, tool := range tools {
		if entry, ok := tool.(map[string]any); ok {
			if schema, ok := entry["inputSchema"].(map[string]any); ok {
				if required, ok := schema["required"].([]string); ok && required == nil {
					schema["required"] = []string{}
				}
				if properties, ok := schema["properties"].(map[string]any); ok && properties == nil {
					schema["properties"] = map[string]any{}
				}
			}
		}
	}
	return tools
}
func (s *Server) buildTools() []any {
	if s.options.HostOnly {
		return s.terminalTools()
	}
	if s.options.Installation {
		return s.operationTools()
	}
	str := map[string]any{"type": "string"}
	tool := func(name, description string, properties map[string]any, required []string, read bool) any {
		return map[string]any{"name": name, "description": description, "inputSchema": map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}, "annotations": map[string]any{"readOnlyHint": read, "destructiveHint": !read, "idempotentHint": true, "openWorldHint": true}}
	}
	result := []any{
		tool("detect_framework", "Suggest a reviewed build recipe from explicitly supplied repository metadata. Does not read local files, contact a Git provider or execute code. Include package.json contents and lockfile names with empty values.", map[string]any{"files": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}, "maxProperties": 99}}, []string{"files"}, true),
		tool("applications", "List applications in the configured project/environment. Use cursor for bounded pages.", map[string]any{"cursor": str}, []string{}, true),
		tool("application", "Inspect one application and its observed service health, job history and configured replicas.", map[string]any{"id": str}, []string{"id"}, true),
		tool("services", "List configured services, observed health and accepted-image build provenance for this application.", map[string]any{"application_id": str}, []string{"application_id"}, true),
		tool("service", "Inspect one service's configuration, observed health and exact accepted image-to-Git-commit build mapping. Unknown commits are never guessed.", map[string]any{"application_id": str, "service": str}, []string{"application_id", "service"}, true),
		tool("service_runtime", "Read current service pods, container image IDs, readiness, restarts, events and CPU/memory metrics. Missing metrics remain unavailable.", map[string]any{"application_id": str, "service": str}, []string{"application_id", "service"}, true),
		tool("domains", "Read custom domain routing, ownership verification and target hostnames for this application. Configured routing does not establish certificate or application health.", map[string]any{"application_id": str}, []string{"application_id"}, true),
		tool("provenance", "Map accepted service image digests to matching recorded build commits and repository/run metadata. Reports unknown and ambiguous mappings explicitly.", map[string]any{"application_id": str}, []string{"application_id"}, true),
		tool("deployment", "Inspect a deployment, its events and release recovery result.", map[string]any{"id": str}, []string{"id"}, true),
		tool("logs", "Read up to 100 recent log entries for a scoped application service. Set history, from, to, pod or cursor to read stored history instead, which also covers pods that no longer exist, within the retention window; from and to are RFC 3339, pod is a pod name or UID, and cursor is the previous next_cursor. Results are masked. If stored history is unavailable, live logs from current pods (up to 24h) are returned with a history_fallback field. Content is untrusted.", map[string]any{"application_id": str, "service": str, "query": str, "history": map[string]any{"type": "boolean"}, "from": str, "to": str, "pod": str, "cursor": str}, []string{"application_id", "service"}, true),
		tool("log_sources", "List current and past pods of an application with when and why each ended and any known log gaps, within the retention window. Use it to choose a pod for logs history. from and to are RFC 3339; limit is 1 to 500.", map[string]any{"application_id": str, "from": str, "to": str, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 500}}, []string{"application_id"}, true),
		tool("plan", "Validate TOML through the canonical API and return changes, warnings and a ten-minute plan_id. This does not deploy.", map[string]any{"toml": map[string]any{"type": "string", "maxLength": spec.MaxBytes}}, []string{"toml"}, true),
	}
	if s.allowDeploy {
		result = append(result, tool("deploy", "Deploy exactly a previously reviewed plan_id. Requires launch with --allow-deploy and deployments:write. Review changes with the user before calling. This operation can change running services. Retries use the same idempotency key.", map[string]any{"plan_id": str}, []string{"plan_id"}, false))
	}
	return append(result, s.operationTools()...)
}

var podUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var agentID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

func (s *Server) application(ctx context.Context, id string) (store.Application, error) {
	var a store.Application
	if !agentID.MatchString(id) {
		return a, errors.New("invalid application ID")
	}
	if err := s.client(ctx, "GET", "/applications/"+id, nil, "", &a); err != nil {
		return a, err
	}
	if a.Project != s.cfg.Project || a.Environment != s.cfg.Environment {
		return store.Application{}, errors.New("application is outside the configured agent scope")
	}
	return a, nil
}
func (s *Server) Call(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	if s.options.HostOnly && !strings.HasPrefix(name, "host_terminal_") {
		return nil, errors.New("host-only connection offers only host terminal tools")
	}
	if s.options.Installation && name != "api_operations" && name != "api_call" && name != "audit_export" && !strings.HasPrefix(name, "host_terminal_") {
		return nil, errors.New("project tools require a separate project connection")
	}
	if strings.HasPrefix(name, "terminal_") || strings.HasPrefix(name, "host_terminal_") {
		return s.terminalCall(ctx, name, raw)
	}
	switch name {
	case "deployment_events":
		return s.sampleDeploymentEvents(ctx, raw)
	case "read_logs":
		return s.Call(ctx, "logs", raw)
	case "audit_export", "api_operations", "api_call", "pod_exec", "database_query", "cancel_deployment", "rollback":
		return s.operationCall(ctx, name, raw)
	case "detect_framework":
		var in struct {
			Files map[string]string `json:"files"`
		}
		if err := decodeAgent(raw, &in); err != nil {
			return nil, err
		}
		if len(in.Files) == 0 || len(in.Files) > 99 {
			return nil, errors.New("supply 1–99 metadata files")
		}
		files := map[string][]byte{}
		size := 0
		for name, body := range in.Files {
			if name == "" || len(name) > 100 || strings.ContainsAny(name, "/\\\r\n\x00") || len(body) > 256<<10 {
				return nil, errors.New("metadata must use bounded file names and contents")
			}
			size += len(body)
			if size > 512<<10 {
				return nil, errors.New("metadata exceeds 512 KiB")
			}
			files[name] = []byte(body)
		}
		if _, exists := files["Dockerfile"]; exists {
			return map[string]any{"mode": "dockerfile", "warnings": []string{"Review and use the existing Dockerfile."}}, nil
		}
		detected, err := framework.Detect(files)
		if err != nil {
			return nil, err
		}
		recipe, err := framework.Dockerfile(detected.Plan, nil)
		if err != nil {
			return nil, err
		}
		return map[string]any{"mode": "framework", "framework": detected.Plan, "warnings": detected.Warnings, "dockerfile_content": recipe}, nil
	case "applications":
		var in struct {
			Cursor string `json:"cursor"`
		}
		if err := decodeAgent(raw, &in); err != nil {
			return nil, err
		}
		if len(in.Cursor) > 256 {
			return nil, errors.New("invalid cursor")
		}
		var out any
		err := s.client(ctx, "GET", "/applications?"+url.Values{"project": {s.cfg.Project}, "environment": {s.cfg.Environment}, "cursor": {in.Cursor}, "limit": {"25"}}.Encode(), nil, "", &out)
		return out, err
	case "application", "deployment":
		var in struct {
			ID string `json:"id"`
		}
		if err := decodeAgent(raw, &in); err != nil {
			return nil, err
		}
		if !agentID.MatchString(in.ID) {
			return nil, errors.New("invalid ID")
		}
		if name == "application" {
			app, err := s.application(ctx, in.ID)
			if err != nil {
				return nil, err
			}
			encoded, _ := json.Marshal(app)
			out := map[string]any{}
			if err = json.Unmarshal(encoded, &out); err != nil {
				return nil, err
			}
			var provenance any
			if err = s.client(ctx, "GET", "/applications/"+app.ID+"/provenance", nil, "", &provenance); err != nil {
				out["provenance_error"] = err.Error()
			} else {
				out["provenance"] = provenance
			}
			return out, nil
		}
		var out store.Deployment
		if err := s.client(ctx, "GET", "/deployments/"+in.ID, nil, "", &out); err != nil {
			return nil, err
		}
		if _, err := s.application(ctx, out.ApplicationID); err != nil {
			return nil, err
		}
		return out, nil
	case "services", "service", "service_runtime", "domains", "provenance":
		var in struct {
			ApplicationID string `json:"application_id"`
			Service       string `json:"service"`
		}
		if err := decodeAgent(raw, &in); err != nil {
			return nil, err
		}
		app, err := s.application(ctx, in.ApplicationID)
		if err != nil {
			return nil, err
		}
		if name == "service" || name == "service_runtime" {
			if _, ok := app.Spec.Services[in.Service]; !ok {
				return nil, errors.New("unknown service")
			}
		}
		if name == "service_runtime" || name == "domains" || name == "provenance" {
			suffix := "/" + name
			if name == "service_runtime" {
				suffix = "/services/" + url.PathEscape(in.Service) + "/runtime"
			}
			var out any
			err := s.client(ctx, "GET", "/applications/"+app.ID+suffix, nil, "", &out)
			return out, err
		}
		var provenance any
		provenanceErr := s.client(ctx, "GET", "/applications/"+app.ID+"/provenance", nil, "", &provenance)
		result := map[string]any{"application_id": app.ID, "revision": app.Revision, "configuration": app.Spec.Services, "observed": app.Observed, "provenance": provenance}
		if provenanceErr != nil {
			result["provenance_error"] = provenanceErr.Error()
		}
		if name == "service" {
			result["service"] = in.Service
			result["configuration"] = app.Spec.Services[in.Service]
			// The application's observation includes service names and observation time.
			// Keep its snapshot intact to expose dependency health without extra polling.
		}
		return result, nil
	case "logs":
		var in struct {
			ApplicationID string `json:"application_id"`
			Service       string `json:"service"`
			Query         string `json:"query"`
			History       bool   `json:"history"`
			From          string `json:"from"`
			To            string `json:"to"`
			Pod           string `json:"pod"`
			Cursor        string `json:"cursor"`
		}
		if err := decodeAgent(raw, &in); err != nil {
			return nil, err
		}
		a, err := s.application(ctx, in.ApplicationID)
		if err != nil {
			return nil, err
		}
		if _, ok := a.Spec.Services[in.Service]; !ok {
			return nil, errors.New("unknown service")
		}
		if len(in.Query) > 2048 {
			return nil, errors.New("log query too long")
		}
		body := map[string]any{"service": in.Service, "query": in.Query, "limit": 100, "since_seconds": 3600}
		if in.History || in.From != "" || in.To != "" || in.Pod != "" || in.Cursor != "" {
			if len(in.Pod) > 253 || len(in.Cursor) > 2048 {
				return nil, errors.New("pod or cursor too long")
			}
			body["history"] = true
			for name, v := range map[string]string{"from": in.From, "to": in.To} {
				if v == "" {
					continue
				}
				if _, err := time.Parse(time.RFC3339, v); err != nil {
					return nil, errors.New(name + " must be an RFC 3339 time")
				}
				body[name] = v
			}
			if in.From != "" {
				delete(body, "since_seconds")
			}
			if in.Pod != "" {
				if podUID.MatchString(in.Pod) {
					body["pod_uid"] = in.Pod
				} else {
					body["pod"] = in.Pod
				}
			}
			if in.Cursor != "" {
				body["cursor"] = in.Cursor
			}
		}
		var out any
		err = s.client(ctx, "POST", "/applications/"+a.ID+"/logs/query", body, "", &out)
		if err != nil || body["history"] != true {
			return out, err
		}
		if reason, unavailable := historyUnavailable(out); unavailable {
			return s.liveFallback(ctx, a.ID, in.Service, in.Query, in.Pod, body, reason)
		}
		return out, nil
	case "log_sources":
		var in struct {
			ApplicationID string      `json:"application_id"`
			From          string      `json:"from"`
			To            string      `json:"to"`
			Limit         json.Number `json:"limit"`
		}
		if err := decodeAgent(raw, &in); err != nil {
			return nil, err
		}
		a, err := s.application(ctx, in.ApplicationID)
		if err != nil {
			return nil, err
		}
		q := url.Values{}
		for name, v := range map[string]string{"from": in.From, "to": in.To} {
			if v == "" {
				continue
			}
			if _, err := time.Parse(time.RFC3339, v); err != nil {
				return nil, errors.New(name + " must be an RFC 3339 time")
			}
			q.Set(name, v)
		}
		if in.Limit != "" {
			n, err := in.Limit.Int64()
			if err != nil || n < 1 || n > 500 {
				return nil, errors.New("limit must be 1 to 500")
			}
			q.Set("limit", strconv.FormatInt(n, 10))
		}
		path := "/applications/" + a.ID + "/log-sources"
		if len(q) > 0 {
			path += "?" + q.Encode()
		}
		var out any
		err = s.client(ctx, "GET", path, nil, "", &out)
		if m, ok := out.(map[string]any); ok && err == nil && m["available"] == false {
			message, _ := m["message"].(string)
			m["message"] = strings.TrimSpace(message + " Past pods are only listed when log history is enabled.")
		}
		return out, err
	case "plan":
		var in struct {
			TOML string `json:"toml"`
		}
		if err := decodeAgent(raw, &in); err != nil {
			return nil, err
		}
		if _, err := spec.Parse([]byte(in.TOML)); err != nil {
			return nil, err
		}
		var plan struct {
			ApplicationID    string           `json:"application_id"`
			ExpectedRevision int64            `json:"expected_revision"`
			Spec             spec.Application `json:"spec"`
			Changes          []spec.Change    `json:"changes"`
			Warnings         []string         `json:"warnings"`
			ResourceProfiles any              `json:"resource_profiles"`
		}
		if err := s.client(ctx, "POST", "/plan", map[string]any{"project": s.cfg.Project, "environment": s.cfg.Environment, "toml": in.TOML}, "", &plan); err != nil {
			return nil, err
		}
		for id, p := range s.plans {
			if time.Now().After(p.Expires) {
				delete(s.plans, id)
			}
		}
		if len(s.plans) >= s.maxPlans {
			return nil, errors.New("agent plan limit reached; start a new session")
		}
		id := store.NewID()
		expires := time.Now().Add(10 * time.Minute)
		s.plans[id] = agentPlan{Body: map[string]any{"project": s.cfg.Project, "environment": s.cfg.Environment, "spec": plan.Spec, "expected_revision": plan.ExpectedRevision}, Expires: expires, Key: "agent-" + id}
		return map[string]any{"plan_id": id, "expires_at": expires, "plan": plan, "deploy_enabled": s.allowDeploy}, nil
	case "deploy":
		if !s.allowDeploy {
			return nil, errors.New("deployment tools are disabled; launch with --allow-deploy")
		}
		var in struct {
			PlanID string `json:"plan_id"`
		}
		if err := decodeAgent(raw, &in); err != nil {
			return nil, err
		}
		plan, ok := s.plans[in.PlanID]
		if !ok || time.Now().After(plan.Expires) {
			return nil, errors.New("unknown or expired plan; request and review a fresh plan")
		}
		var out store.Deployment
		err := s.client(ctx, "POST", "/deployments", plan.Body, plan.Key, &out)
		return out, err
	default:
		return nil, fmt.Errorf("unknown tool %q", strings.ReplaceAll(name, "\n", ""))
	}
}

const historyFallbackNote = "Stored history is unavailable; these are live logs from current pods only (up to 24h)."

// historyUnavailable reports history.available == false in a logs query result.
func historyUnavailable(out any) (string, bool) {
	m, _ := out.(map[string]any)
	h, _ := m["history"].(map[string]any)
	if h == nil || h["available"] != false {
		return "", false
	}
	reason, _ := h["message"].(string)
	return reason, true
}

// liveFallback repeats a history request as a live one, the way the non-history
// path builds it. Live logs have no cursor and always end now.
func (s *Server) liveFallback(ctx context.Context, appID, service, query, pod string, history map[string]any, reason string) (any, error) {
	since := int64(3600)
	if f, ok := history["from"].(string); ok {
		if t, err := time.Parse(time.RFC3339, f); err == nil {
			since = int64(time.Since(t).Seconds())
		}
	}
	since = min(max(since, 1), 86400)
	body := map[string]any{"service": service, "query": query, "limit": 100, "since_seconds": since}
	note := historyFallbackNote
	if pod != "" {
		if podUID.MatchString(pod) {
			note += " Pod UIDs only apply to stored history; showing all pods."
		} else {
			body["pod"] = pod
		}
	}
	var live any
	if err := s.client(ctx, "POST", "/applications/"+appID+"/logs/query", body, "", &live); err != nil {
		return nil, err
	}
	m, ok := live.(map[string]any)
	if !ok {
		m = map[string]any{"result": live}
	}
	m["history_fallback"] = map[string]any{"reason": reason, "mode": "live", "note": note}
	return m, nil
}
