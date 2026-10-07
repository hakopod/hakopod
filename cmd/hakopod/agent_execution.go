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

type executionFlags struct {
	Service, Pod, Container, CommandJSON, SQLFile, ParametersJSON string
	AllowExec, AllowSQLWrite                                      bool
	MaxRows, MaxBytes, Timeout, MaxOutput                         int
	ExpectedRevision                                              int64
}

func boundedFile(path string, max int) ([]byte, error) {
	if path == "" {
		return nil, errors.New("file is required")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, int64(max+1)))
	if err != nil {
		return nil, err
	}
	if len(raw) > max {
		return nil, errors.New("file exceeds size limit")
	}
	return raw, nil
}
func runExecution(ctx context.Context, c *client, cfg config, kind, id string, f executionFlags) error {
	var arguments any
	var opts agent.Options
	var name string
	switch kind {
	case "exec":
		if cfg.Project == "" || cfg.Environment == "" {
			return errors.New("execution requires project and environment")
		}
		if !f.AllowExec {
			return errors.New("execution requires --allow-exec")
		}
		var command []string
		if err := operations.Decode([]byte(f.CommandJSON), &command); err != nil {
			return errors.New("command-json must contain an argument array")
		}
		arguments = map[string]any{"application_id": id, "service": f.Service, "pod": f.Pod, "container": f.Container, "command": command, "timeout_seconds": f.Timeout, "max_output_bytes": f.MaxOutput}
		opts.AllowExec = true
		name = "pod_exec"
	case "query":
		if cfg.Project == "" || cfg.Environment == "" {
			return errors.New("SQL requires project and environment")
		}
		sql, err := boundedFile(f.SQLFile, 65536)
		if err != nil {
			return err
		}
		parameters := []any{}
		if f.ParametersJSON != "" {
			if err := operations.Decode([]byte(f.ParametersJSON), &parameters); err != nil {
				return err
			}
		}
		arguments = map[string]any{"database_id": id, "sql": string(sql), "parameters": parameters, "write": f.AllowSQLWrite, "max_rows": f.MaxRows, "max_bytes": f.MaxBytes, "expected_revision": f.ExpectedRevision}
		opts.AllowSQL = true
		opts.AllowSQLWrite = f.AllowSQLWrite
		name = "database_query"
	default:
		return errors.New("unknown execution command")
	}
	raw, err := json.Marshal(arguments)
	if err != nil {
		return err
	}
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	server := agent.NewWithOptions(c.request, agent.Scope{Project: cfg.Project, Environment: cfg.Environment}, opts, 0)
	out, err := server.Call(bounded, name, raw)
	if err != nil {
		return err
	}
	return printJSON(out)
}
