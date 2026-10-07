package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/store"
	utilexec "k8s.io/client-go/util/exec"
)

const podExecPermission = "pods:exec"
const podExecOutputLimit = 64 << 10

// A process-wide limit covers all Server instances and includes cancelled
// transports until they return. No command waits in an unbounded queue.
var podExecSlots = make(chan struct{}, 4)

type podExecRequest struct {
	Pod            string   `json:"pod"`
	Container      string   `json:"container"`
	Command        []string `json:"command"`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty"`
	MaxOutputBytes int      `json:"max_output_bytes,omitempty"`
}

func (in *podExecRequest) validate() error {
	if in.Pod == "" || in.Container == "" || len(in.Command) == 0 {
		return fmt.Errorf("pod, container and command are required")
	}
	if in.TimeoutSeconds == 0 {
		in.TimeoutSeconds = 20
	}
	if in.MaxOutputBytes == 0 {
		in.MaxOutputBytes = podExecOutputLimit
	}
	if in.TimeoutSeconds < 1 || in.TimeoutSeconds > 20 {
		return fmt.Errorf("timeout_seconds must be between 1 and 20")
	}
	if in.MaxOutputBytes < 1 || in.MaxOutputBytes > podExecOutputLimit {
		return fmt.Errorf("max_output_bytes must be between 1 and 65536 per stream")
	}
	o := cluster.TerminalOptions{Pod: in.Pod, Container: in.Container, Command: in.Command}
	return o.Validate()
}
func podExecAllowed(p store.Principal, a store.Application) bool {
	if !p.Allows(podExecPermission, a.Project, a.Environment, a.Name) {
		return false
	}
	if p.CredentialType == "machine" {
		explicit := false
		for _, permission := range p.Permissions {
			if permission == podExecPermission {
				explicit = true
			}
		}
		return explicit && p.Project != "" && p.Environment != ""
	}
	return p.CredentialType == "browser" || p.CredentialType == "cli"
}

type podExecBuffer struct {
	mu        sync.Mutex
	bytes     []byte
	limit     int
	truncated bool
}

func (b *podExecBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := min(len(p), b.limit-len(b.bytes))
	b.bytes = append(b.bytes, p[:n]...)
	if n < len(p) {
		b.truncated = true
	}
	// Discard excess bytes while draining the stream to preserve exit status.
	return len(p), nil
}
func (b *podExecBuffer) snapshot() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return base64.StdEncoding.EncodeToString(b.bytes), b.truncated
}

type podExecResponse struct {
	ExecutionID     string `json:"execution_id"`
	Pod             string `json:"pod"`
	Container       string `json:"container"`
	PodUID          string `json:"pod_uid"`
	Outcome         string `json:"outcome"`
	ExitCode        *int   `json:"exit_code"`
	Reason          string `json:"reason,omitempty"`
	Stdout          string `json:"stdout_base64"`
	Stderr          string `json:"stderr_base64"`
	StdoutTruncated bool   `json:"stdout_truncated"`
	StderrTruncated bool   `json:"stderr_truncated"`
	AuditRecorded   bool   `json:"audit_recorded"`
}

func podExecOutcome(err error) (string, *int) {
	if err == nil {
		code := 0
		return "exited", &code
	}
	var exit utilexec.ExitError
	if errors.As(err, &exit) && exit.Exited() {
		code := exit.ExitStatus()
		return "exited", &code
	}
	return "unknown", nil
}

func (s *Server) registerPodExecRoutes(m *http.ServeMux) {
	m.HandleFunc("POST /api/v1/applications/{id}/services/{service}/exec", s.executePodCommand)
}
func (s *Server) executePodCommand(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), podExecPermission)
	if !ok {
		return
	}
	p := who(r)
	if !podExecAllowed(p, a) {
		problem(w, 403, "pod_exec_grant_required", "Pod commands require an explicit scoped permission for machine keys")
		return
	}
	var in podExecRequest
	if !decodeLimited(w, r, &in, 144<<10) {
		return
	}
	if err := in.validate(); err != nil {
		problem(w, 400, "invalid_pod_command", err.Error())
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
	select {
	case podExecSlots <- struct{}{}:
	default:
		problem(w, 429, "pod_exec_limit", "Four pod commands are already active")
		return
	}
	release := true
	defer func() {
		if release {
			<-podExecSlots
		}
	}()
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(in.TimeoutSeconds)*time.Second)
	defer cancel()
	check := func(ctx context.Context) error {
		checkCtx, done := context.WithTimeout(ctx, time.Second)
		defer done()
		current, err := s.freshRuntimePrincipal(r.WithContext(checkCtx))
		if err != nil || !podExecAllowed(current, a) {
			return store.ErrForbidden
		}
		return nil
	}
	if err := check(ctx); err != nil {
		failure(w, err)
		return
	}
	target := cluster.Target{ApplicationID: a.ID, Project: a.Project, Environment: a.Environment, Spec: a.Spec}
	options := cluster.TerminalOptions{Pod: in.Pod, Container: in.Container, Command: in.Command}
	uid, err := s.Cluster.TerminalPod(ctx, target, service, options)
	if err != nil {
		problem(w, 409, "pod_unavailable", "Select a running container from this service")
		return
	}
	result := podExecResponse{ExecutionID: store.NewID(), Pod: in.Pod, Container: in.Container, PodUID: string(uid), Outcome: "unknown"}
	command, _ := json.Marshal(in.Command)
	hash := sha256.Sum256(command)
	metadata := map[string]any{"execution_id": result.ExecutionID, "pod": in.Pod, "container": in.Container, "pod_uid": string(uid), "service": service, "command_hash": hex.EncodeToString(hash[:]), "outcome": "unknown", "timeout_seconds": in.TimeoutSeconds}
	audit := func(ctx context.Context, action string) error {
		_, err := s.Store.Pool.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,$3,$4,$5)", p.ID, p.KeyID, action, a.ID, store.JSON(metadata))
		return err
	}
	if err := audit(ctx, "pod.exec.start"); err != nil {
		failure(w, err)
		return
	}
	stdout := &podExecBuffer{limit: in.MaxOutputBytes}
	stderr := &podExecBuffer{limit: in.MaxOutputBytes}
	guardDone := make(chan struct{})
	go func() {
		defer close(guardDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if check(ctx) != nil {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-guardDone }()
	done := make(chan error, 1)
	release = false
	go func() {
		defer func() { <-podExecSlots }()
		done <- s.Cluster.PodExec(ctx, target, service, options, uid, stdout, stderr, check)
	}()
	select {
	case err = <-done:
		result.Outcome, result.ExitCode = podExecOutcome(err)
	case <-ctx.Done():
		result.Outcome = "unknown"
	}
	if result.Outcome == "unknown" {
		result.Reason = "The transport ended without an exit status. The process may still run. Do not retry without checking its effects."
	}
	result.Stdout, result.StdoutTruncated = stdout.snapshot()
	result.Stderr, result.StderrTruncated = stderr.snapshot()
	metadata["outcome"] = result.Outcome
	metadata["exit_code"] = result.ExitCode
	metadata["stdout_truncated"] = result.StdoutTruncated
	metadata["stderr_truncated"] = result.StderrTruncated
	auditCtx, auditDone := context.WithTimeout(context.Background(), 2*time.Second)
	result.AuditRecorded = audit(auditCtx, "pod.exec.finish") == nil
	auditDone()
	write(w, 200, result)
}

var _ io.Writer = (*podExecBuffer)(nil)
