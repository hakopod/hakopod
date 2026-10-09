package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/store"
	"k8s.io/apimachinery/pkg/util/validation"
)

// These slots also cover cancelled transports until the fixed helper deadline.
// There is no queue and no arbitrary command or SQL in this endpoint.
var bindingTestSlots = make(chan struct{}, 4)

type bindingTestRequest struct {
	ExpectedRevision int64  `json:"expected_revision"`
	Pod              string `json:"pod,omitempty"`
}

func (in bindingTestRequest) validate() error {
	if in.ExpectedRevision < 1 {
		return fmt.Errorf("expected_revision must identify the saved application revision")
	}
	if in.Pod != "" && len(validation.IsDNS1123Subdomain(in.Pod)) != 0 {
		return fmt.Errorf("pod must name a running pod in this service")
	}
	return nil
}

func (s *Server) testServiceBinding(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:write")
	if !ok {
		return
	}
	var in bindingTestRequest
	if !decodeLimited(w, r, &in, 2048) {
		return
	}
	if err := in.validate(); err != nil {
		problem(w, 400, "invalid_binding_test", err.Error())
		return
	}
	if in.ExpectedRevision != a.Revision {
		problem(w, 409, "revision_changed", "The application changed. Refresh its bindings before testing.")
		return
	}
	service, variable := r.PathValue("service"), r.PathValue("variable")
	svc, exists := a.Spec.Services[service]
	if _, declared := svc.Bindings[variable]; !exists || !declared {
		problem(w, 404, "binding_not_found", "This service has no binding with that variable name.")
		return
	}
	if s.Cluster == nil {
		problem(w, 503, "cluster_unavailable", "The application runtime is unavailable.")
		return
	}
	select {
	case bindingTestSlots <- struct{}{}:
	default:
		problem(w, 429, "binding_test_limit", "Four connection tests are already active. Try again shortly.")
		return
	}
	releasedByWorker := false
	defer func() {
		if !releasedByWorker {
			<-bindingTestSlots
		}
	}()
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	check := func(ctx context.Context) error {
		checkCtx, stop := context.WithTimeout(ctx, time.Second)
		defer stop()
		p, err := s.freshRuntimePrincipal(r.WithContext(checkCtx))
		if err != nil || !p.Allows("deployments:write", a.Project, a.Environment, a.Name) {
			return store.ErrForbidden
		}
		var current bool
		if err = s.Store.Pool.QueryRow(checkCtx, "SELECT EXISTS(SELECT 1 FROM applications WHERE id=$1 AND revision=$2)", a.ID, a.Revision).Scan(&current); err != nil {
			return err
		}
		if !current {
			return store.ErrConflict
		}
		return nil
	}
	if err := check(ctx); err != nil {
		failure(w, err)
		return
	}
	p := who(r)
	metadata := map[string]any{"execution_id": store.NewID(), "service": service, "variable": variable, "revision": a.Revision, "pod": in.Pod}
	audit := func(ctx context.Context, action string) error {
		_, err := s.Store.Pool.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,$3,$4,$5)", p.ID, p.KeyID, action, a.ID, store.JSON(metadata))
		return err
	}
	if err := audit(ctx, "binding.test.start"); err != nil {
		failure(w, err)
		return
	}
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
	type completion struct {
		value cluster.BindingTestResult
		err   error
	}
	done := make(chan completion, 1)
	releasedByWorker = true
	go func() {
		value, err := s.Cluster.TestServiceBinding(ctx, cluster.Target{ApplicationID: a.ID, Project: a.Project, Environment: a.Environment, Revision: a.Revision, Spec: a.Spec}, service, variable, in.Pod, check)
		done <- completion{value, err}
		if ctx.Err() != nil || value.Outcome == "unavailable" {
			// The transport may close before the helper exits. Its own request
			// deadline starts after inventory and authorization. Retain this
			// slot for its full maximum lifetime after transport completion.
			timer := time.NewTimer(20 * time.Second)
			<-timer.C
		}
		<-bindingTestSlots
	}()
	var completed completion
	select {
	case completed = <-done:
	case <-ctx.Done():
		metadata["outcome"] = "interrupted"
		auditCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
		_ = audit(auditCtx, "binding.test.finish")
		stop()
		problem(w, 409, "binding_test_interrupted", "The connection test stopped before verification. Refresh the application and try again.")
		return
	}
	if completed.err != nil {
		metadata["outcome"] = "interrupted"
	} else {
		metadata["outcome"], metadata["pod"], metadata["pod_uid"] = completed.value.Outcome, completed.value.Pod, completed.value.PodUID
		codes := make([]string, 0, len(completed.value.Stages))
		for _, stage := range completed.value.Stages {
			codes = append(codes, stage.Code)
		}
		metadata["stage_codes"] = codes
	}
	auditCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	err := audit(auditCtx, "binding.test.finish")
	stop()
	if completed.err != nil {
		failure(w, completed.err)
		return
	}
	if err != nil {
		problem(w, 503, "binding_test_audit_unavailable", "The test finished, but its audit record could not be saved. Refresh and try again.")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	write(w, 200, completed.value)
}
