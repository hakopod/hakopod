package api

import (
	"bytes"
	"context"
	"errors"
	"github.com/hakopod/hakopod/internal/agent"
	"net/http"
	"sync"
)

type sampleWriter struct {
	header    http.Header
	mu        sync.Mutex
	body      bytes.Buffer
	status    int
	cancel    context.CancelFunc
	truncated bool
	records   int
}

func (w *sampleWriter) Header() http.Header    { return w.header }
func (w *sampleWriter) WriteHeader(status int) { w.status = status }
func (w *sampleWriter) Flush()                 {}
func (w *sampleWriter) Write(body []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.status = 200
	}
	if w.body.Len()+len(body) > agent.StreamMaxBytes || w.records >= agent.StreamMaxRecords {
		w.truncated = true
		w.cancel()
		return 0, errors.New("event sample limit reached")
	}
	w.records += bytes.Count(body, []byte("\ndata:"))
	return w.body.Write(body)
}
func mcpStreamRequester(routes http.Handler) agent.StreamFunc {
	return func(ctx context.Context, path, cursor string) (agent.StreamSample, error) {
		original, ok := ctx.Value(mcpContextKey{}).(*http.Request)
		if !ok {
			return agent.StreamSample{}, errors.New("MCP request context missing")
		}
		bounded, cancel := context.WithCancel(ctx)
		defer cancel()
		request := original.Clone(bounded)
		request.Method = "GET"
		request.URL.Path = "/api/v1" + path
		request.URL.RawQuery = ""
		request.RequestURI = request.URL.RequestURI()
		request.Body = nil
		request.ContentLength = 0
		request.Header = mcpDispatchHeaders(original)
		request.Header.Set("Accept", "text/event-stream")
		if cursor != "" {
			request.Header.Set("Last-Event-ID", cursor)
		}
		writer := &sampleWriter{header: make(http.Header), cancel: cancel}
		routes.ServeHTTP(writer, request)
		if writer.status >= 400 {
			return agent.StreamSample{}, errors.New("canonical event stream rejected the request")
		}
		sample, err := agent.ParseEventSample(writer.body.Bytes(), cursor, agent.StreamMaxRecords)
		sample.Truncated = sample.Truncated || writer.truncated || ctx.Err() != nil
		return sample, err
	}
}
