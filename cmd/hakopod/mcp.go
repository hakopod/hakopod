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
