package main

import (
	"context"
	"errors"
	"github.com/hakopod/hakopod/internal/agent"
	"io"
	"net/http"
)

func (c *client) sampleEvents(ctx context.Context, path, cursor string) (agent.StreamSample, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.url+"/api/v1"+path, nil)
	if err != nil {
		return agent.StreamSample{}, err
	}
	c.authorizeRequest(req)
	req.Header.Set("Accept", "text/event-stream")
	if cursor != "" {
		req.Header.Set("Last-Event-ID", cursor)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return agent.StreamSample{}, errors.New("event stream request failed")
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return agent.StreamSample{}, responseError(res)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, agent.StreamMaxBytes))
	if err != nil && ctx.Err() == nil {
		return agent.StreamSample{}, errors.New("event stream read failed")
	}
	out, parseErr := agent.ParseEventSample(raw, cursor, agent.StreamMaxRecords)
	out.Truncated = out.Truncated || len(raw) == agent.StreamMaxBytes || ctx.Err() != nil
	return out, parseErr
}
