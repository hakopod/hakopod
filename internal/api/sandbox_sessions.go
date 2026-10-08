package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/hakopod/hakopod/internal/sandbox"
	"github.com/hakopod/hakopod/internal/store"
)

func (s *Server) registerSessionRoutes(m *http.ServeMux) {
	m.HandleFunc("POST /api/v1/applications/{id}/services/{service}/sessions", s.createSandboxSession)
	m.HandleFunc("GET /api/v1/applications/{id}/services/{service}/sessions", s.listSandboxSessions)
	m.HandleFunc("GET /api/v1/applications/{id}/services/{service}/sessions/{session}", s.getSandboxSession)
	m.HandleFunc("DELETE /api/v1/applications/{id}/services/{service}/sessions/{session}", s.deleteSandboxSession)
	m.HandleFunc("POST /api/v1/applications/{id}/services/{service}/sessions/{session}/heartbeat", s.heartbeatSandboxSession)
	m.HandleFunc("POST /api/v1/applications/{id}/services/{service}/sessions/{session}/call", s.callSandboxSession)
}
func sessionOwner(w http.ResponseWriter, r *http.Request) (string, bool) {
	w.Header().Set("Cache-Control", "no-store")
	value, err := sandbox.HashKey(r.Header.Get("X-Hakopod-Owner-Scope"))
	if err != nil {
		problem(w, 400, "invalid_owner", "X-Hakopod-Owner-Scope must contain a valid owner identifier")
		return "", false
	}
	return value, true
}
func (s *Server) createSandboxSession(w http.ResponseWriter, r *http.Request) {
	owner, ok := sessionOwner(w, r)
	if !ok {
		return
	}
	if s.sandboxRuntime() == nil {
		problem(w, 503, "unavailable", "Native session runtime is unavailable")
		return
	}
	var in sandbox.CreateRequest
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var extra any
	if decoder.Decode(&in) != nil || decoder.Decode(&extra) != io.EOF {
		problem(w, 400, "invalid_request", "Request must contain one valid session object")
		return
	}
	result, err := s.Store.CreateSession(r.Context(), who(r), r.PathValue("id"), r.PathValue("service"), owner, r.Header.Get("Idempotency-Key"), in)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 202, result)
}
func (s *Server) listSandboxSessions(w http.ResponseWriter, r *http.Request) {
	owner, ok := sessionOwner(w, r)
	if !ok {
		return
	}
	results, err := s.Store.ListSessions(r.Context(), who(r), r.PathValue("id"), r.PathValue("service"), owner, r.URL.Query().Get("runtime_key"))
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": results})
}
func (s *Server) getSandboxSession(w http.ResponseWriter, r *http.Request) {
	owner, ok := sessionOwner(w, r)
	if !ok {
		return
	}
	result, err := s.Store.ReadSession(r.Context(), who(r), r.PathValue("id"), r.PathValue("service"), owner, r.PathValue("session"), "sessions:read")
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, result)
}
func (s *Server) deleteSandboxSession(w http.ResponseWriter, r *http.Request) {
	owner, ok := sessionOwner(w, r)
	if !ok {
		return
	}
	result, err := s.Store.CloseSession(r.Context(), who(r), r.PathValue("id"), r.PathValue("service"), owner, r.PathValue("session"))
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 202, result)
}
func (s *Server) heartbeatSandboxSession(w http.ResponseWriter, r *http.Request) {
	owner, ok := sessionOwner(w, r)
	if !ok {
		return
	}
	result, err := s.Store.HeartbeatSession(r.Context(), who(r), r.PathValue("id"), r.PathValue("service"), owner, r.PathValue("session"), r.Header.Get("X-Hakopod-Session-Generation"))
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, result)
}

// sessionOutput bounds untrusted bytes and records whether the HTTP response started.
type sessionOutput struct {
	response http.ResponseWriter
	used     int64
	started  bool
	mu       sync.Mutex
}

func (w *sessionOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.used+int64(len(p)) > sandbox.MaxOutputBytes {
		return 0, store.ErrInput
	}
	if !w.started {
		w.response.Header().Set("Content-Type", "application/octet-stream")
		w.response.Header().Set("X-Accel-Buffering", "no")
		w.response.WriteHeader(200)
		w.started = true
	}
	n, err := w.response.Write(p)
	w.used += int64(n)
	if f, ok := w.response.(http.Flusher); ok {
		f.Flush()
	}
	return n, err
}
func (s *Server) callSandboxSession(w http.ResponseWriter, r *http.Request) {
	owner, ok := sessionOwner(w, r)
	if !ok {
		return
	}
	if s.sandboxRuntime() == nil {
		problem(w, 503, "unavailable", "Native session runtime is unavailable")
		return
	}
	select {
	case s.sessionCalls <- struct{}{}:
		defer func() { <-s.sessionCalls }()
	default:
		problem(w, 429, "capacity", "Session call capacity is full. No call started. Retry this request later.")
		return
	}
	// Authenticate before reading a large body. Two process-wide slots bound memory.
	if _, err := s.Store.ReadSession(r.Context(), who(r), r.PathValue("id"), r.PathValue("service"), owner, r.PathValue("session"), "sessions:call"); err != nil {
		failure(w, err)
		return
	}
	bodyCtx, stopBody := context.WithTimeout(r.Context(), 15*time.Second)
	stopRead := context.AfterFunc(bodyCtx, func() { r.Body.Close() })
	input, err := io.ReadAll(http.MaxBytesReader(w, r.Body, sandbox.MaxInputBytes))
	stopRead()
	stopBody()
	if err != nil {
		problem(w, 413, "invalid_input", "Session call input exceeds its byte limit or read deadline")
		return
	}
	sum := sha256.Sum256(input)
	record, err := s.Store.ClaimSessionCall(r.Context(), who(r), r.PathValue("id"), r.PathValue("service"), owner, r.PathValue("session"), r.Header.Get("X-Hakopod-Session-Generation"), r.Header.Get("Idempotency-Key"), hex.EncodeToString(sum[:]))
	if err != nil {
		failure(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), sandbox.CallTimeout)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if s.Store.CheckSessionCall(ctx, record) != nil {
					cancel()
					return
				}
			}
		}
	}()
	output := &sessionOutput{response: w}
	err = s.sandboxRuntime().CallSession(ctx, record, bytes.NewReader(input), output)
	complete := err == nil && ctx.Err() == nil
	cancel()
	<-done
	persistCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	finishErr := s.Store.FinishSessionCall(persistCtx, record, complete)
	stop()
	if !output.started {
		if !complete || finishErr != nil {
			problem(w, 502, "call_interrupted", "Session call outcome is uncertain. Do not replay this request")
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(200)
	} else if !complete || finishErr != nil {
		// Abort a partial stream. A successful HTTP EOF must not hide an execution failure.
		panic(http.ErrAbortHandler)
	}
}
