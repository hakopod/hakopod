package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/hakopod/hakopod/internal/agent"
	"github.com/hakopod/hakopod/internal/operations"
	"io"
	"os"
	"time"
)

func runAPIOperation(ctx context.Context, c *client, cfg config, args []string, pathJSON, queryJSON, bodyFile, idem string, allowWrite bool, cursor, family, operation string, limit int) error {
	return runAPIAccess(ctx, c, cfg, args, pathJSON, queryJSON, bodyFile, idem, operations.Access{AllowWrite: allowWrite}, cursor, family, operation, limit)
}
func runAPIAccess(ctx context.Context, c *client, cfg config, args []string, pathJSON, queryJSON, bodyFile, idem string, access operations.Access, cursor, family, operation string, limit int) error {
	if len(args) == 2 && args[0] == "export" && args[1] == "exportUserAuditHistory" {
		var query map[string]string
		if err := operations.Decode([]byte(queryJSON), &query); err != nil {
			return err
		}
		for key := range query {
			if key != "identity_id" && key != "before" {
				return errors.New("unsupported audit export query")
			}
		}
		server := agent.NewWithOptions(c.request, agent.Scope{Project: cfg.Project, Environment: cfg.Environment}, agent.Options{Installation: access.Installation, AllowAdmin: access.AllowAdmin}, 1)
		raw, err := json.Marshal(map[string]string{"identity_id": query["identity_id"], "before": query["before"]})
		if err != nil {
			return err
		}
		out, err := server.Call(ctx, "audit_export", raw)
		if err != nil {
			return err
		}
		return printJSON(out)
	}
	if len(args) == 1 && args[0] == "operations" {
		out, err := operations.Discovery(cursor, family, operation, limit)
		if err != nil {
			return err
		}
		return printJSON(out)
	}
	if len(args) != 2 || args[0] != "call" {
		return errors.New("usage: hakopod api operations | api call OPERATION --path-json '{}' --query-json '{}' --body-file FILE --allow-write")
	}
	in := operations.Invocation{Operation: args[1], IdempotencyKey: idem}
	if pathJSON != "" {
		if err := operations.Decode([]byte(pathJSON), &in.Path); err != nil {
			return err
		}
	}
	if queryJSON != "" {
		if err := operations.Decode([]byte(queryJSON), &in.Query); err != nil {
			return err
		}
	}
	if bodyFile != "" {
		f, err := os.Open(bodyFile)
		if err != nil {
			return err
		}
		defer f.Close()
		body, err := io.ReadAll(io.LimitReader(f, operations.MaxBytes+1))
		if err != nil {
			return err
		}
		if len(body) > operations.MaxBytes {
			return errors.New("body exceeds 1 MiB")
		}
		in.Body = body
	}
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := operations.InvokeWithAccess(bounded, operations.RequestFunc(c.request), operations.Scope{Project: cfg.Project, Environment: cfg.Environment}, access, in)
	if err != nil {
		return err
	}
	return printJSON(out)
}
