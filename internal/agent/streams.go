package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"
)

const StreamMaxBytes = 256 << 10
const StreamMaxRecords = 100

type StreamSample struct {
	Events     []json.RawMessage `json:"events"`
	NextCursor string            `json:"next_cursor,omitempty"`
	Truncated  bool              `json:"truncated"`
}
type StreamFunc func(context.Context, string, string) (StreamSample, error)

func (s *Server) SetStream(stream StreamFunc) { s.stream = stream }

// ParseEventSample reads only bounded SSE input. Event data remains untrusted.
func ParseEventSample(raw []byte, previous string, limit int) (StreamSample, error) {
	out := StreamSample{Events: []json.RawMessage{}, NextCursor: previous}
	if limit < 1 || limit > StreamMaxRecords || len(raw) > StreamMaxBytes {
		return out, errors.New("invalid event sample bounds")
	}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), StreamMaxBytes)
	id := ""
	data := []string{}
	emit := func() error {
		if len(data) == 0 {
			return nil
		}
		body := strings.Join(data, "\n")
		if !json.Valid([]byte(body)) {
			return errors.New("invalid event JSON")
		}
		if len(out.Events) >= limit {
			out.Truncated = true
			return nil
		}
		out.Events = append(out.Events, json.RawMessage(body))
		if id != "" {
			out.NextCursor = id
		}
		id = ""
		data = nil
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if err := emit(); err != nil {
				return out, err
			}
			if out.Truncated {
				return out, nil
			}
		case strings.HasPrefix(line, "id:"):
			id = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
			if _, err := strconv.ParseInt(id, 10, 64); err != nil {
				return out, errors.New("invalid event cursor")
			}
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return out, err
	}
	return out, nil
}
func (s *Server) sampleDeploymentEvents(ctx context.Context, raw json.RawMessage) (any, error) {
	var in struct {
		ID     string `json:"deployment_id"`
		Cursor string `json:"cursor"`
		Limit  int    `json:"limit"`
	}
	if err := decodeAgent(raw, &in); err != nil {
		return nil, err
	}
	if !agentID.MatchString(in.ID) {
		return nil, errors.New("invalid deployment ID")
	}
	if in.Limit == 0 {
		in.Limit = 100
	}
	if in.Limit < 1 || in.Limit > 100 {
		return nil, errors.New("event limit must be 1 to 100")
	}
	if in.Cursor != "" {
		if n, err := strconv.ParseInt(in.Cursor, 10, 64); err != nil || n < 0 {
			return nil, errors.New("invalid event cursor")
		}
	}
	var deployment struct {
		ApplicationID string `json:"application_id"`
	}
	if err := s.client(ctx, "GET", "/deployments/"+in.ID, nil, "", &deployment); err != nil {
		return nil, err
	}
	if _, err := s.application(ctx, deployment.ApplicationID); err != nil {
		return nil, err
	}
	if s.stream == nil {
		return nil, errors.New("event sampling transport is unavailable on this connection")
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	sample, err := s.stream(bounded, "/deployments/"+in.ID+"/events", in.Cursor)
	if err != nil {
		return nil, err
	}
	if len(sample.Events) > in.Limit {
		sample.Events = sample.Events[:in.Limit]
		sample.Truncated = true
	}
	if len(sample.Events) > 0 {
		var last struct {
			ID json.Number `json:"id"`
		}
		if json.Unmarshal(sample.Events[len(sample.Events)-1], &last) == nil && last.ID != "" {
			sample.NextCursor = string(last.ID)
		}
	}
	return sample, nil
}
