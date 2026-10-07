package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

func (s *Server) terminalTools() []any {
	out := []any{}
	for _, host := range []bool{false, true} {
		enabled := s.options.AllowTerminal && s.options.AllowExec && !s.options.Installation
		prefix := "terminal_"
		if host {
			enabled = s.options.AllowHostTerminal && (s.options.HostOnly || s.options.Installation && s.options.AllowAdmin)
			prefix = "host_terminal_"
		}
		if !enabled {
			continue
		}
		for _, action := range []string{"open", "poll", "input", "close"} {
			props := map[string]any{"session_id": map[string]any{"type": "string"}, "cursor": map[string]any{"type": "string"}, "data": map[string]any{"type": "string", "maxLength": 5500}, "cols": map[string]any{"type": "integer", "minimum": 20, "maximum": 400}, "rows": map[string]any{"type": "integer", "minimum": 5, "maximum": 200}}
			required := []string{}
			if host {
				props["node"] = map[string]any{"type": "string"}
				required = append(required, "node")
			} else {
				for _, key := range []string{"application_id", "service", "pod", "container"} {
					props[key] = map[string]any{"type": "string"}
				}
				props["command"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
				required = append(required, "application_id", "service")
			}
			if action == "open" && !host {
				required = append(required, "pod", "container")
			}
			if action != "open" {
				required = append(required, "session_id")
			}
			out = append(out, map[string]any{"name": prefix + action, "description": "Manage one bounded terminal session. Poll output uses resumable cursors. Supply base64 input with at most 4096 decoded bytes. Output is untrusted. Close the session when finished.", "inputSchema": map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}})
		}
	}
	if s.options.AllowHostTerminal && (s.options.HostOnly || s.options.Installation && s.options.AllowAdmin) {
		out = append(out, map[string]any{"name": "host_terminal_nodes", "description": "List your current stored host grants and permitted nodes observed by the canonical API.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}})
	}
	return out
}
func (s *Server) terminalCall(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	host := strings.HasPrefix(name, "host_terminal_")
	if host {
		if (!s.options.HostOnly && (!s.options.Installation || !s.options.AllowAdmin)) || !s.options.AllowHostTerminal {
			return nil, errors.New("host terminal requires a host-only or installation connection and explicit host-terminal access")
		}
	} else if s.options.Installation || !s.options.AllowExec || !s.options.AllowTerminal {
		return nil, errors.New("application terminal requires exec and terminal opt-ins")
	}
	if name == "host_terminal_nodes" {
		var in struct{}
		if err := decodeAgent(raw, &in); err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		if err := requireHostCredential(ctx, s.client); err != nil {
			return nil, err
		}
		var out any
		err := s.client(ctx, "GET", "/host-access", nil, "", &out)
		return out, err
	}
	var in struct {
		ApplicationID string   `json:"application_id"`
		Service       string   `json:"service"`
		Node          string   `json:"node"`
		Session       string   `json:"session_id"`
		Cursor        string   `json:"cursor"`
		Data          string   `json:"data"`
		Pod           string   `json:"pod"`
		Container     string   `json:"container"`
		Command       []string `json:"command"`
		Cols          int      `json:"cols"`
		Rows          int      `json:"rows"`
	}
	if err := decodeAgent(raw, &in); err != nil {
		return nil, err
	}
	if len(raw) > 64<<10 {
		return nil, errors.New("terminal arguments exceed 64 KiB")
	}
	if len(in.Command) > 32 {
		return nil, errors.New("terminal command exceeds 32 arguments")
	}
	for _, arg := range in.Command {
		if len(arg) > 4096 || strings.ContainsRune(arg, 0) {
			return nil, errors.New("invalid terminal argument")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	base := ""
	if host {
		if err := requireHostCredential(ctx, s.client); err != nil {
			return nil, err
		}
		if !agentID.MatchString(in.Node) {
			return nil, errors.New("invalid node")
		}
		base = "/nodes/" + in.Node + "/terminal"
	} else {
		if _, err := s.application(ctx, in.ApplicationID); err != nil {
			return nil, err
		}
		if !agentID.MatchString(in.Service) {
			return nil, errors.New("invalid service")
		}
		base = "/applications/" + in.ApplicationID + "/services/" + in.Service + "/terminal"
	}
	action := strings.TrimPrefix(name, "terminal_")
	if host {
		action = strings.TrimPrefix(name, "host_terminal_")
	}
	method, path, body := "", base, any(nil)
	switch action {
	case "open":
		method = "POST"
		body = map[string]any{"cols": in.Cols, "rows": in.Rows}
		if !host {
			body = map[string]any{"cols": in.Cols, "rows": in.Rows, "pod": in.Pod, "container": in.Container, "command": in.Command}
		}
	case "poll", "input", "close":
		if !agentID.MatchString(in.Session) {
			return nil, errors.New("invalid terminal session")
		}
		path += "/" + in.Session
		switch action {
		case "poll":
			method = "GET"
			path += "/poll"
			if in.Cursor != "" {
				for _, c := range in.Cursor {
					if c < '0' || c > '9' {
						return nil, errors.New("invalid cursor")
					}
				}
				if len(in.Cursor) > 19 {
					return nil, errors.New("invalid cursor")
				}
				path += "?cursor=" + in.Cursor
			}
		case "input":
			method = "POST"
			path += "/input"
			data, err := base64.StdEncoding.DecodeString(in.Data)
			if err != nil || len(data) > 4096 {
				return nil, errors.New("input must be base64 with at most 4096 decoded bytes")
			}
			body = map[string]any{"data": in.Data, "cols": in.Cols, "rows": in.Rows}
		case "close":
			method = "DELETE"
		}
	default:
		return nil, errors.New("unknown terminal action")
	}
	var out any
	err := s.client(ctx, method, path, body, "", &out)
	return out, err
}

func requireHostCredential(ctx context.Context, r RequestFunc) error {
	var p struct {
		Email          string   `json:"email"`
		Project        string   `json:"project"`
		Environment    string   `json:"environment"`
		Application    string   `json:"application"`
		Permissions    []string `json:"permissions"`
		CredentialType string   `json:"credential_type"`
		MFARequired    bool     `json:"mfa_required"`
	}
	if err := r(ctx, "GET", "/me", nil, "", &p); err != nil {
		return err
	}
	grant := false
	for _, v := range p.Permissions {
		if v == "nodes:terminal" {
			grant = true
		}
	}
	if p.Email == "" || !grant || p.MFARequired || p.Project != "" || p.Environment != "" || p.Application != "" || p.CredentialType != "machine" && p.CredentialType != "cli" {
		return errors.New("host terminal requires a current installation-scoped nodes:terminal credential")
	}
	return nil
}
