package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/store"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/client-go/util/exec"
)

type terminalSession struct {
	mu                           sync.Mutex
	id, owner, key, app, service string
	options                      cluster.TerminalOptions
	uid                          types.UID
	expires, lastInput           time.Time
	ctx                          context.Context
	cancel                       context.CancelFunc
	input                        chan []byte
	sizes                        chan remotecommand.TerminalSize
	started                      bool
	timer                        *time.Timer
}
type terminalReader struct {
	ctx     context.Context
	input   <-chan []byte
	pending []byte
}

func (r *terminalReader) Read(p []byte) (int, error) {
	if len(r.pending) == 0 {
		select {
		case <-r.ctx.Done():
			return 0, io.EOF
		case r.pending = <-r.input:
		}
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

type terminalSizes struct {
	ctx   context.Context
	sizes <-chan remotecommand.TerminalSize
}

func (s terminalSizes) Next() *remotecommand.TerminalSize {
	select {
	case <-s.ctx.Done():
		return nil
	case size := <-s.sizes:
		return &size
	}
}

type terminalWriter struct {
	ctx    context.Context
	output chan<- []byte
}

func (w terminalWriter) Write(p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		n := min(len(p), 4096)
		data := append([]byte(nil), p[:n]...)
		select {
		case <-w.ctx.Done():
			return total, w.ctx.Err()
		case w.output <- data:
		}
		total += n
		p = p[n:]
	}
	return total, nil
}

func (s *Server) registerTerminalRoutes(m *http.ServeMux) {
	s.terminals = map[string]*terminalSession{}
	m.HandleFunc("POST /api/v1/applications/{id}/services/{service}/terminal", s.createTerminal)
	m.HandleFunc("GET /api/v1/applications/{id}/services/{service}/terminal/{session}/output", s.terminalOutput)
	m.HandleFunc("POST /api/v1/applications/{id}/services/{service}/terminal/{session}/input", s.terminalInput)
	m.HandleFunc("DELETE /api/v1/applications/{id}/services/{service}/terminal/{session}", s.deleteTerminal)
}
func (s *Server) createTerminal(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:write")
	if !ok {
		return
	}
	p := who(r)
	if p.CredentialType != "browser" && p.CredentialType != "cli" {
		problem(w, 403, "human_session_required", "Sign in with the dashboard or CLI to open a terminal")
		return
	}
	var o cluster.TerminalOptions
	if !decode(w, r, &o) {
		return
	}
	if err := o.Validate(); err != nil {
		problem(w, 400, "invalid_terminal", err.Error())
		return
	}
	service := r.PathValue("service")
	if _, ok := a.Spec.Services[service]; !ok {
		problem(w, 404, "not_found", "service not found")
		return
	}
	if s.Cluster == nil {
		problem(w, 503, "cluster_unavailable", "Kubernetes is unavailable")
		return
	}
	target := cluster.Target{ApplicationID: a.ID, Project: a.Project, Environment: a.Environment, Spec: a.Spec}
	uid, err := s.Cluster.TerminalPod(r.Context(), target, service, o)
	if err != nil {
		problem(w, 409, "pod_unavailable", "Select a running container from this service")
		return
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(10*time.Minute))
	x := &terminalSession{id: store.NewID(), owner: p.ID, key: p.KeyID, app: a.ID, service: service, options: o, uid: uid, expires: time.Now().Add(10 * time.Minute), lastInput: time.Now(), ctx: ctx, cancel: cancel, input: make(chan []byte, 8), sizes: make(chan remotecommand.TerminalSize, 1)}
	x.sizes <- remotecommand.TerminalSize{Width: o.Cols, Height: o.Rows}
	s.terminalMu.Lock()
	if len(s.terminals) >= 4 {
		s.terminalMu.Unlock()
		cancel()
		problem(w, 429, "terminal_limit", "Four terminal sessions are already open; close one and retry")
		return
	}
	s.terminals[x.id] = x
	x.timer = time.AfterFunc(30*time.Second, func() {
		s.terminalMu.Lock()
		defer s.terminalMu.Unlock()
		current := s.terminals[x.id]
		if current == nil {
			return
		}
		current.mu.Lock()
		started := current.started
		current.mu.Unlock()
		if !started {
			current.cancel()
			delete(s.terminals, x.id)
		}
	})
	s.terminalMu.Unlock()
	_, err = s.Store.Pool.Exec(r.Context(), "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'terminal.open',$3,$4)", p.ID, p.KeyID, a.ID, store.JSON(map[string]string{"session": x.id, "pod": o.Pod, "container": o.Container, "service": service}))
	if err != nil {
		s.closeTerminal(x)
		failure(w, err)
		return
	}
	write(w, 201, map[string]any{"id": x.id, "expires_at": x.expires, "pod": o.Pod, "container": o.Container})
}
func (s *Server) terminalFor(w http.ResponseWriter, r *http.Request) (*terminalSession, store.Application, bool) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:write")
	if !ok {
		return nil, a, false
	}
	s.terminalMu.Lock()
	x := s.terminals[r.PathValue("session")]
	s.terminalMu.Unlock()
	p := who(r)
	if x == nil || x.app != a.ID || x.service != r.PathValue("service") || x.owner != p.ID || x.key != p.KeyID || x.ctx.Err() != nil {
		problem(w, 404, "terminal_expired", "Terminal is closed or belongs to another session; reconnect")
		return nil, a, false
	}
	return x, a, true
}
func (s *Server) closeTerminal(x *terminalSession) {
	x.cancel()
	s.terminalMu.Lock()
	defer s.terminalMu.Unlock()
	if x.timer != nil {
		x.timer.Stop()
	}
	delete(s.terminals, x.id)
}
func (s *Server) CloseTerminals() {
	s.terminalMu.Lock()
	defer s.terminalMu.Unlock()
	for id, x := range s.terminals {
		x.cancel()
		if x.timer != nil {
			x.timer.Stop()
		}
		delete(s.terminals, id)
	}
}
func (s *Server) deleteTerminal(w http.ResponseWriter, r *http.Request) {
	x, _, ok := s.terminalFor(w, r)
	if !ok {
		return
	}
	s.closeTerminal(x)
	w.WriteHeader(204)
}
func (s *Server) terminalInput(w http.ResponseWriter, r *http.Request) {
	x, _, ok := s.terminalFor(w, r)
	if !ok {
		return
	}
	var in struct {
		Data string `json:"data"`
		Cols uint16 `json:"cols"`
		Rows uint16 `json:"rows"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.Data) > 5500 {
		problem(w, 413, "input_limit", "Send at most 4096 input bytes at a time")
		return
	}
	data, err := base64.StdEncoding.DecodeString(in.Data)
	if err != nil || len(data) > 4096 {
		problem(w, 400, "invalid_input", "Terminal input must be base64, at most 4096 decoded bytes")
		return
	}
	if (in.Cols != 0 || in.Rows != 0) && (in.Cols < 20 || in.Cols > 400 || in.Rows < 5 || in.Rows > 200) {
		problem(w, 400, "invalid_size", "Terminal size must be 20–400 columns and 5–200 rows")
		return
	}
	x.mu.Lock()
	started := x.started
	x.mu.Unlock()
	if !started {
		problem(w, 409, "not_connected", "Connect the terminal output before sending input")
		return
	}
	if len(data) > 0 {
		select {
		case x.input <- data:
		case <-x.ctx.Done():
			problem(w, 410, "closed", "Terminal is closed")
			return
		default:
			problem(w, 429, "input_backpressure", "Terminal input is full; wait for the current command")
			return
		}
	}
	if in.Cols != 0 {
		size := remotecommand.TerminalSize{Width: in.Cols, Height: in.Rows}
		select {
		case x.sizes <- size:
		default:
			select {
			case <-x.sizes:
			default:
			}
			select {
			case x.sizes <- size:
			default:
			}
		}
	}
	x.mu.Lock()
	x.lastInput = time.Now()
	x.mu.Unlock()
	w.WriteHeader(204)
}
func (s *Server) terminalOutput(w http.ResponseWriter, r *http.Request) {
	x, a, ok := s.terminalFor(w, r)
	if !ok {
		return
	}
	if !s.streamSlot(w) {
		return
	}
	x.mu.Lock()
	if x.started {
		x.mu.Unlock()
		<-s.streams
		problem(w, 409, "already_connected", "A terminal can have only one output reader")
		return
	}
	x.started = true
	x.mu.Unlock()
	defer func() {
		s.closeTerminal(x)
		<-s.streams
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = s.Store.Pool.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'terminal.close',$3,$4)", x.owner, x.key, x.app, store.JSON(map[string]string{"session": x.id, "pod": x.options.Pod}))
	}()
	// Closing the output request cancels exec even if this handler is blocked in
	// a response write. Join the callback before finishing the handler.
	requestCancelled := make(chan struct{})
	stopRequest := context.AfterFunc(r.Context(), func() {
		x.cancel()
		close(requestCancelled)
	})
	defer func() {
		if !stopRequest() {
			<-requestCancelled
		}
	}()
	go s.guardStream(x.ctx, x.cancel, x.key, a, "deployments:write")
	writes := guardResponseWrites(x.ctx, w, 10*time.Second)
	defer writes.stop()
	if !writes.begin() {
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	controller := writes.controller
	if controller.Flush() != nil {
		return
	}
	output := make(chan []byte, 8)
	done := make(chan error, 1)
	go func() {
		done <- s.Cluster.Terminal(x.ctx, cluster.Target{ApplicationID: a.ID, Project: a.Project, Environment: a.Environment, Spec: a.Spec}, x.service, x.options, x.uid, &terminalReader{ctx: x.ctx, input: x.input}, terminalWriter{ctx: x.ctx, output: output}, terminalSizes{ctx: x.ctx, sizes: x.sizes})
	}()
	emit := func(value any) bool {
		if !writes.begin() {
			return false
		}
		body, _ := json.Marshal(value)
		_, err := fmt.Fprintf(w, "data: %s\n\n", body)
		if err == nil {
			err = controller.Flush()
		}
		return err == nil
	}
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-x.ctx.Done():
			return
		case data := <-output:
			if !emit(map[string]string{"type": "output", "data": base64.StdEncoding.EncodeToString(data)}) {
				return
			}
		case err := <-done:
			// Preserve queued final output before the exit frame.
			for {
				select {
				case data := <-output:
					if !emit(map[string]string{"type": "output", "data": base64.StdEncoding.EncodeToString(data)}) {
						return
					}
				default:
					goto drained
				}
			}
		drained:
			code, message := 0, "Process exited"
			if err != nil {
				code = -1
				message = "Could not run this command, or the connection ended; verify the executable exists in this container"
				var exit utilexec.ExitError
				if errors.As(err, &exit) {
					code = exit.ExitStatus()
					message = "Process exited"
				}
			}
			emit(map[string]any{"type": "exit", "code": code, "message": message})
			return
		case <-ticker.C:
			x.mu.Lock()
			idle := time.Since(x.lastInput) > 2*time.Minute
			x.mu.Unlock()
			if idle {
				emit(map[string]any{"type": "exit", "code": -1, "message": "Terminal closed after two minutes without input"})
				return
			}
			if !writes.begin() {
				return
			}
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			if controller.Flush() != nil {
				return
			}
		}
	}
}
