package main

import (
	"context"
	"github.com/hakopod/hakopod/internal/agent"
	"io"
	"regexp"
)

func serveAgent(ctx context.Context, c *client, cfg config, allowDeploy bool, in io.Reader, out io.Writer) error {
	var request agent.RequestFunc
	if c != nil {
		request = c.request
	}
	return agent.Serve(ctx, request, agent.Scope{Project: cfg.Project, Environment: cfg.Environment}, allowDeploy, in, out, version)
}

var agentID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

func serveAgentOptions(ctx context.Context, c *client, cfg config, deploy, write, exec, sql, sqlWrite bool, in io.Reader, out io.Writer) error {
	return agent.ServeWithOptions(ctx, c.request, agent.Scope{Project: cfg.Project, Environment: cfg.Environment}, agent.Options{AllowDeploy: deploy, AllowWrite: write, AllowExec: exec, AllowSQL: sql, AllowSQLWrite: sqlWrite}, in, out, version)
}

func serveAgentAccess(ctx context.Context, c *client, cfg config, opts agent.Options, in io.Reader, out io.Writer) error {
	return agent.ServeWithStream(ctx, c.request, agent.Scope{Project: cfg.Project, Environment: cfg.Environment}, opts, c.sampleEvents, in, out, version)
}
